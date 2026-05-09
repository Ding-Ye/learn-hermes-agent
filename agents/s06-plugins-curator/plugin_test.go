package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestMemory(t *testing.T) *SQLiteMemory {
	t.Helper()
	mem, err := NewSQLiteMemory(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// silentLogger collects nothing — for tests we usually don't care.
func silentLogger() *Logger { return &Logger{Out: func(string) {}} }

// recordingLogger captures lines for assertions.
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (rl *recordingLogger) attach() *Logger {
	return &Logger{Out: func(s string) {
		rl.mu.Lock()
		defer rl.mu.Unlock()
		rl.lines = append(rl.lines, s)
	}}
}
func (rl *recordingLogger) all() []string {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	out := make([]string, len(rl.lines))
	copy(out, rl.lines)
	return out
}

func TestPluginManager_DispatchOrder(t *testing.T) {
	rl := &recordingLogger{}
	pm := NewPluginManager(rl.attach())
	mem := newTestMemory(t)
	host := &Host{Registry: NewRegistry(), Memory: mem, Logger: rl.attach()}
	a := NewLoggingPlugin()
	b := NewLoggingPlugin()
	pm.Register(a)
	pm.Register(b)
	if err := pm.Init(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	pm.DispatchSessionStart(context.Background(), "s1")
	pm.DispatchSessionEnd(context.Background(), "s1")
	pm.Close()
	// Two plugins, every event hit exactly twice.
	got := rl.all()
	count := 0
	for _, ln := range got {
		if strings.Contains(ln, "session start s1") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 'session start' lines, got %d (lines=%v)", count, got)
	}
}

// errPlugin returns an error from OnSessionStart; the manager must keep
// calling subsequent plugins regardless.
type errPlugin struct{ called int }

func (e *errPlugin) Name() string                                          { return "errplugin" }
func (e *errPlugin) Init(ctx context.Context, host *Host) error            { return nil }
func (e *errPlugin) OnSessionStart(ctx context.Context, sid string) error  { e.called++; return errIntent("boom") }
func (e *errPlugin) OnSessionEnd(ctx context.Context, sid string) error    { return nil }
func (e *errPlugin) Close() error                                          { return nil }

func errIntent(s string) error { return &intentErr{s} }

type intentErr struct{ msg string }

func (e *intentErr) Error() string { return e.msg }

func TestPluginManager_PluginErrorDoesNotStopOthers(t *testing.T) {
	rl := &recordingLogger{}
	pm := NewPluginManager(rl.attach())
	mem := newTestMemory(t)
	host := &Host{Registry: NewRegistry(), Memory: mem, Logger: rl.attach()}

	bad := &errPlugin{}
	good := NewLoggingPlugin()
	pm.Register(bad)
	pm.Register(good)
	_ = pm.Init(context.Background(), host)
	pm.DispatchSessionStart(context.Background(), "s1")

	if bad.called != 1 {
		t.Fatalf("bad plugin should have been called once, got %d", bad.called)
	}
	saw := false
	for _, ln := range rl.all() {
		if strings.Contains(ln, "[plugin:logging] session start s1") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("good plugin should still have run after bad one errored")
	}
}

func TestSQLiteMemory_TouchAndArchive(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()
	id, err := mem.Save(ctx, Memory{Content: "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	// Force last_activity_at into the past so the row is "stale" by 1 hour.
	if _, err := mem.db.Exec(
		`UPDATE memories SET last_activity_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}

	stale, err := mem.ListStale(ctx, time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].ID != id {
		t.Fatalf("expected stale to contain our id, got %+v", stale)
	}

	if err := mem.Archive(ctx, []int64{id}); err != nil {
		t.Fatal(err)
	}
	results, _ := mem.Search(ctx, "hello", 5)
	if len(results) != 0 {
		t.Fatalf("archived row should disappear from search, got %d hits", len(results))
	}
}

func TestSearchHitsBumpLastActivity(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()
	id, _ := mem.Save(ctx, Memory{Content: "alpha gamma"})
	// Push it back in time.
	_, _ = mem.db.Exec(`UPDATE memories SET last_activity_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339Nano), id)
	if _, err := mem.Search(ctx, "alpha", 5); err != nil {
		t.Fatal(err)
	}
	// Now the row should not be stale by a 1h threshold.
	stale, _ := mem.ListStale(ctx, time.Hour, 10)
	for _, m := range stale {
		if m.ID == id {
			t.Fatalf("search hit should have bumped last_activity_at; row still stale")
		}
	}
}

func TestCurator_ArchivesStaleAndKeepsFresh(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()
	stale, _ := mem.Save(ctx, Memory{Content: "stale fact"})
	_, _ = mem.Save(ctx, Memory{Content: "fresh fact"})

	// Push only the stale one back.
	_, _ = mem.db.Exec(
		`UPDATE memories SET last_activity_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339Nano), stale)

	rl := &recordingLogger{}
	host := &Host{Registry: NewRegistry(), Memory: mem, Logger: rl.attach()}
	c := NewCuratorPlugin(time.Hour, 100)
	if err := c.Init(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err := c.OnSessionStart(ctx, "sX"); err != nil {
		t.Fatal(err)
	}

	r1, _ := mem.Search(ctx, "stale", 5)
	if len(r1) != 0 {
		t.Fatalf("stale memory should be archived and absent from search, got %d", len(r1))
	}
	r2, _ := mem.Search(ctx, "fresh", 5)
	if len(r2) != 1 {
		t.Fatalf("fresh memory should still be present, got %d", len(r2))
	}
}
