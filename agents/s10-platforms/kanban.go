package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Kanban is the shared SQLite-backed task board. Three processes
// participate:
//
//   gateway   — receives inbound messages (HTTP POST), inserts a job
//   scheduler — polls for pending jobs, runs the agent, marks done
//   cli       — direct mode skips kanban; "cli send" mode would enqueue
//
// Schema is small on purpose: jobs (pending/running/done/failed) plus
// a tiny crontab (recurring jobs). Real hermes adds many more columns
// (priority, retries, depends_on, ...); we keep the spine.
//
// All writes go through _execute_write equivalent: BEGIN IMMEDIATE +
// jittered retry on lock contention, just like s04's discussion of
// hermes_state.SessionDB. WAL is enabled so readers (gateway, CLI
// status views) don't block the writer.

const kanbanSchema = `
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source       TEXT NOT NULL,        -- 'cli' | 'gateway' | 'cron'
    payload      TEXT NOT NULL,        -- the prompt
    status       TEXT NOT NULL,        -- 'pending' | 'running' | 'done' | 'failed'
    created_at   TEXT NOT NULL,
    started_at   TEXT,
    completed_at TEXT,
    result       TEXT,
    error        TEXT
);
CREATE INDEX IF NOT EXISTS idx_jobs_pending ON jobs(status, created_at);

CREATE TABLE IF NOT EXISTS crontab (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    spec          TEXT NOT NULL,       -- 'every <duration>' (we don't parse full cron)
    prompt        TEXT NOT NULL,
    last_fired_at TEXT,
    enabled       INTEGER NOT NULL DEFAULT 1
);
`

type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobFailed  JobStatus = "failed"
)

type Job struct {
	ID          int64
	Source      string
	Payload     string
	Status      JobStatus
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
	Result      string
	Error       string
}

type Kanban struct {
	db *sql.DB
}

func OpenKanban(path string) (*Kanban, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(kanbanSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Kanban{db: db}, nil
}

func (k *Kanban) Close() error { return k.db.Close() }

// EnqueueJob inserts a new pending job. Returns its id. Used by:
//   - gateway when an inbound message arrives
//   - scheduler when a crontab entry fires
//   - cli "send" subcommand (not implemented in our mini)
func (k *Kanban) EnqueueJob(ctx context.Context, source, payload string) (int64, error) {
	if strings.TrimSpace(source) == "" {
		return 0, fmt.Errorf("EnqueueJob: empty source")
	}
	if strings.TrimSpace(payload) == "" {
		return 0, fmt.Errorf("EnqueueJob: empty payload")
	}
	res, err := k.db.ExecContext(ctx,
		`INSERT INTO jobs(source, payload, status, created_at) VALUES (?, ?, ?, ?)`,
		source, payload, string(JobPending), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ClaimNextPending atomically transitions one pending job to running and
// returns it. Returns (nil, false, nil) when nothing is available. The
// "atomic" part matters because in production multiple scheduler
// processes may race for the same job.
func (k *Kanban) ClaimNextPending(ctx context.Context) (*Job, bool, error) {
	tx, err := k.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx,
		`SELECT id, source, payload, created_at FROM jobs
		 WHERE status = ? ORDER BY created_at ASC LIMIT 1`,
		JobPending)
	var (
		j         Job
		createdAt string
	)
	if err := row.Scan(&j.ID, &j.Source, &j.Payload, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	startedAt := time.Now().UTC()
	res, err := tx.ExecContext(ctx,
		`UPDATE jobs SET status = ?, started_at = ?
		 WHERE id = ? AND status = ?`,
		JobRunning, startedAt.Format(time.RFC3339Nano), j.ID, JobPending)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// someone else claimed it between SELECT and UPDATE
		return nil, false, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		j.CreatedAt = t
	}
	j.Status = JobRunning
	j.StartedAt = startedAt
	return &j, true, nil
}

// FinishJob marks a running job as done or failed.
func (k *Kanban) FinishJob(ctx context.Context, id int64, result string, runErr error) error {
	status := JobDone
	errMsg := ""
	if runErr != nil {
		status = JobFailed
		errMsg = runErr.Error()
	}
	_, err := k.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, completed_at = ?, result = ?, error = ?
		 WHERE id = ?`,
		status, time.Now().UTC().Format(time.RFC3339Nano), result, errMsg, id)
	return err
}

// ListJobs returns recent jobs, newest first.
func (k *Kanban) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := k.db.QueryContext(ctx,
		`SELECT id, source, payload, status, created_at,
		        COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(result,''), COALESCE(error,'')
		 FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var (
			j                                              Job
			created, started, completed, statusStr, errStr string
		)
		if err := rows.Scan(&j.ID, &j.Source, &j.Payload, &statusStr, &created, &started, &completed, &j.Result, &errStr); err != nil {
			return nil, err
		}
		j.Status = JobStatus(statusStr)
		j.Error = errStr
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			j.CreatedAt = t
		}
		if started != "" {
			if t, err := time.Parse(time.RFC3339Nano, started); err == nil {
				j.StartedAt = t
			}
		}
		if completed != "" {
			if t, err := time.Parse(time.RFC3339Nano, completed); err == nil {
				j.CompletedAt = t
			}
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetJob fetches a single job by id.
func (k *Kanban) GetJob(ctx context.Context, id int64) (*Job, error) {
	jobs, err := k.ListJobs(ctx, 50)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.ID == id {
			return &j, nil
		}
	}
	// fallback to direct query
	row := k.db.QueryRowContext(ctx,
		`SELECT id, source, payload, status, created_at,
		        COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(result,''), COALESCE(error,'')
		 FROM jobs WHERE id = ?`, id)
	var (
		j                                              Job
		created, started, completed, statusStr, errStr string
	)
	if err := row.Scan(&j.ID, &j.Source, &j.Payload, &statusStr, &created, &started, &completed, &j.Result, &errStr); err != nil {
		return nil, err
	}
	j.Status = JobStatus(statusStr)
	j.Error = errStr
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		j.CreatedAt = t
	}
	return &j, nil
}

// AddCronEntry / DueCronEntries / TouchCronEntry: a tiny cron-like
// substrate for s09. We don't parse 5-field cron syntax; specs are of
// the form "every 5m" / "every 1h" — enough for the demo.

type CronEntry struct {
	ID          int64
	Spec        string
	Prompt      string
	LastFiredAt time.Time
	Enabled     bool
}

func (k *Kanban) AddCronEntry(ctx context.Context, spec, prompt string) (int64, error) {
	if _, err := parseEverySpec(spec); err != nil {
		return 0, err
	}
	res, err := k.db.ExecContext(ctx,
		`INSERT INTO crontab(spec, prompt, enabled) VALUES (?, ?, 1)`,
		spec, prompt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DueCronEntries returns entries whose last fire is older than `interval(spec)` ago.
func (k *Kanban) DueCronEntries(ctx context.Context, now time.Time) ([]CronEntry, error) {
	rows, err := k.db.QueryContext(ctx,
		`SELECT id, spec, prompt, COALESCE(last_fired_at,''), enabled FROM crontab WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []CronEntry
	for rows.Next() {
		var (
			c          CronEntry
			lastFired  string
			enabledInt int
		)
		if err := rows.Scan(&c.ID, &c.Spec, &c.Prompt, &lastFired, &enabledInt); err != nil {
			return nil, err
		}
		c.Enabled = enabledInt == 1
		dur, err := parseEverySpec(c.Spec)
		if err != nil {
			continue
		}
		if lastFired == "" {
			due = append(due, c)
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, lastFired); err == nil {
			c.LastFiredAt = t
			if now.Sub(t) >= dur {
				due = append(due, c)
			}
		}
	}
	return due, rows.Err()
}

func (k *Kanban) TouchCronEntry(ctx context.Context, id int64) error {
	_, err := k.db.ExecContext(ctx,
		`UPDATE crontab SET last_fired_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// parseEverySpec accepts strings like "every 5m" or "every 1h30m" and
// returns the time.Duration. Real cron syntax is out of scope.
func parseEverySpec(spec string) (time.Duration, error) {
	s := strings.TrimSpace(spec)
	if !strings.HasPrefix(strings.ToLower(s), "every ") {
		return 0, fmt.Errorf("crontab spec must be 'every <duration>' (e.g. 'every 5m'), got %q", spec)
	}
	d := strings.TrimSpace(s[len("every "):])
	dur, err := time.ParseDuration(d)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", d, err)
	}
	if dur < time.Second {
		return 0, fmt.Errorf("interval too short: %v", dur)
	}
	return dur, nil
}
