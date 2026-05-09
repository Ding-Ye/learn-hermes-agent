package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DockerEnvironment runs each command in a fresh `docker run --rm` of the
// configured image. It's the simplest possible "isolated workspace"
// backend, and a useful demo of how the Environment abstraction lets
// the agent operate outside the host without the loop knowing.
//
// Caveats vs production:
//   - we spin up a new container per command (slow). hermes keeps a
//     long-running container per session and uses `docker exec` for
//     follow-on commands.
//   - no volume mounts, no network config, no resource limits.
//   - assumes `docker` is on PATH and the daemon is reachable.
type DockerEnvironment struct {
	image string
}

func NewDockerEnvironment(image string) *DockerEnvironment {
	return &DockerEnvironment{image: image}
}

func (e *DockerEnvironment) Name() string { return "docker:" + e.image }

func (e *DockerEnvironment) Execute(ctx context.Context, opts ExecOptions) (*ExecResult, error) {
	if opts.Command == "" {
		return nil, fmt.Errorf("docker: empty command")
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	args := []string{"run", "--rm"}
	if opts.Cwd != "" {
		args = append(args, "-w", opts.Cwd)
	}
	for k, v := range opts.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, e.image, "bash", "-c", opts.Command)

	cmd := exec.CommandContext(ctx, "docker", args...)
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
			err = nil
		} else if isDockerMissing(err, stderr.String()) {
			// Common failure modes worth surfacing helpfully: docker
			// daemon not running, image pull fails, permission denied.
			return nil, fmt.Errorf("docker unavailable: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
	}
	return &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exit,
		Duration: dur,
		Backend:  e.Name(),
		Cwd:      opts.Cwd,
	}, err
}

func (e *DockerEnvironment) Close() error { return nil }

func isDockerMissing(err error, stderr string) bool {
	if _, ok := err.(*exec.Error); ok {
		return true // exec: "docker": not found
	}
	if strings.Contains(stderr, "Cannot connect to the Docker daemon") {
		return true
	}
	return false
}
