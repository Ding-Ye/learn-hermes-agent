package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecOptions are the input shape every backend accepts. Hermes calls
// these execution hints; missing fields fall back to backend defaults.
type ExecOptions struct {
	Command string
	Cwd     string
	Timeout time.Duration
	Env     map[string]string // additional environment vars
}

// ExecResult is the *uniform* output shape. Every backend returns this
// dictionary regardless of whether it ran a local subprocess or shelled
// into a Docker container 1000 miles away.
type ExecResult struct {
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
	Backend  string        `json:"backend"`
	Cwd      string        `json:"cwd,omitempty"`
}

// Environment is the abstraction. Implementations differ in *where* the
// shell runs; the contract is identical.
type Environment interface {
	Name() string
	Execute(ctx context.Context, opts ExecOptions) (*ExecResult, error)
	Close() error
}

// =====================================================================
// LocalEnvironment — subprocess on the host.
// =====================================================================

type LocalEnvironment struct{}

func NewLocalEnvironment() *LocalEnvironment { return &LocalEnvironment{} }

func (e *LocalEnvironment) Name() string { return "local" }

func (e *LocalEnvironment) Execute(ctx context.Context, opts ExecOptions) (*ExecResult, error) {
	if opts.Command == "" {
		return nil, fmt.Errorf("local: empty command")
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", opts.Command)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	if len(opts.Env) > 0 {
		envs := make([]string, 0, len(opts.Env))
		for k, v := range opts.Env {
			envs = append(envs, k+"="+v)
		}
		cmd.Env = envs
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
			err = nil // surfaces as ExitCode != 0, not as a Go error
		}
	}
	return &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exit,
		Duration: dur,
		Backend:  "local",
		Cwd:      opts.Cwd,
	}, err
}

func (e *LocalEnvironment) Close() error { return nil }

// =====================================================================
// Factory
// =====================================================================

// NewEnvironment parses a backend spec and returns the right Environment.
// Specs:
//
//	local            — subprocess on the host (default)
//	docker:<image>   — `docker run --rm <image> bash -c <command>`
//
// Real hermes also supports ssh, modal, daytona, singularity, vercel —
// each implements the same Environment shape.
func NewEnvironment(spec string) (Environment, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "local" {
		return NewLocalEnvironment(), nil
	}
	if strings.HasPrefix(spec, "docker:") {
		image := strings.TrimPrefix(spec, "docker:")
		if image == "" {
			return nil, fmt.Errorf("docker: missing image")
		}
		return NewDockerEnvironment(image), nil
	}
	return nil, fmt.Errorf("unknown backend: %q (supported: local, docker:<image>)", spec)
}

// for tests / docs to import the symbol uniformly
var _ Environment = (*LocalEnvironment)(nil)
