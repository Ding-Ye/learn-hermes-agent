package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLocalEnvironment_EchoStdout(t *testing.T) {
	e := NewLocalEnvironment()
	res, err := e.Execute(context.Background(), ExecOptions{Command: "echo hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Fatalf("stdout %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if res.Backend != "local" {
		t.Fatalf("backend %q", res.Backend)
	}
}

func TestLocalEnvironment_NonZeroExit(t *testing.T) {
	e := NewLocalEnvironment()
	res, err := e.Execute(context.Background(), ExecOptions{Command: "exit 7"})
	if err != nil {
		t.Fatalf("Execute (non-zero exit should not Go-error): %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit code %d, want 7", res.ExitCode)
	}
}

func TestLocalEnvironment_StderrCaptured(t *testing.T) {
	e := NewLocalEnvironment()
	res, _ := e.Execute(context.Background(), ExecOptions{Command: "echo oops 1>&2"})
	if !strings.Contains(res.Stderr, "oops") {
		t.Fatalf("stderr capture failed: %q", res.Stderr)
	}
}

func TestLocalEnvironment_TimeoutCancelsCommand(t *testing.T) {
	e := NewLocalEnvironment()
	start := time.Now()
	res, err := e.Execute(context.Background(), ExecOptions{
		Command: "sleep 5",
		Timeout: 100 * time.Millisecond,
	})
	dur := time.Since(start)
	if dur > 2*time.Second {
		t.Fatalf("timeout did not fire; ran for %v", dur)
	}
	if err == nil && res.ExitCode == 0 {
		t.Fatalf("expected timeout failure, got success: %+v", res)
	}
}

func TestNewEnvironment_FactoryDispatches(t *testing.T) {
	cases := []struct {
		spec     string
		wantName string
		wantErr  bool
	}{
		{"local", "local", false},
		{"", "local", false},
		{"docker:alpine:3.19", "docker:alpine:3.19", false},
		{"docker:", "", true},
		{"unknown:foo", "", true},
	}
	for _, c := range cases {
		env, err := NewEnvironment(c.spec)
		gotErr := err != nil
		if gotErr != c.wantErr {
			t.Fatalf("spec=%q got err=%v want err=%v", c.spec, err, c.wantErr)
		}
		if !c.wantErr && env.Name() != c.wantName {
			t.Fatalf("spec=%q name=%q want %q", c.spec, env.Name(), c.wantName)
		}
	}
}

func TestDockerEnvironment_DegradesGracefullyWhenNoDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err == nil {
		t.Skip("docker is installed; this test only runs when it isn't")
	}
	e := NewDockerEnvironment("alpine:3.19")
	_, err := e.Execute(context.Background(), ExecOptions{Command: "echo hi"})
	if err == nil {
		t.Fatalf("expected error when docker is missing")
	}
}

// TerminalTool integration: round-trip through the tool interface so we
// know the JSON shape the LLM sees.
func TestTerminalTool_ExecuteReturnsJSON(t *testing.T) {
	tool := NewTerminalTool(NewLocalEnvironment())
	out, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"stdout"`) {
		t.Fatalf("expected JSON with stdout field, got: %s", out)
	}
	if !strings.Contains(out, `"local"`) {
		t.Fatalf("expected backend=local in output: %s", out)
	}
}
