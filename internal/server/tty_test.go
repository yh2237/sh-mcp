//go:build unix

package server

import (
	"strings"
	"testing"
	"time"

	"github.com/yh2237/sh-mcp/internal/config"
)

func TestTTYSession(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	out := h.ok("run_command", map[string]any{"command": "sh", "tty": true, "timeout_sec": 60, "wait_sec": 5})
	t.Cleanup(func() { h.call("kill_job", map[string]any{"job_id": "job-1"}) })
	if !strings.Contains(out, "running in a terminal") {
		t.Fatalf("expected a terminal session: %s", out)
	}
	stdin := func(input string) string {
		t.Helper()
		return h.ok("write_stdin", map[string]any{"job_id": "job-1", "input": input})
	}

	if out := stdin("echo tty-$((40+2))\n"); !strings.Contains(out, "tty-42") || strings.Contains(out, "\x1b") || strings.Contains(out, "\r") {
		t.Errorf("command output: %q", out)
	}
	if out := stdin("printf 'name? '; read n; echo hello-$n\n"); !strings.Contains(out, "name?") {
		t.Errorf("prompt: %q", out)
	}
	if out := stdin("bob\n"); !strings.Contains(out, "hello-bob") {
		t.Errorf("answer to the prompt: %q", out)
	}

	// Ctrl-C stops the foreground command, not the shell.
	h.ok("write_stdin", map[string]any{"job_id": "job-1", "input": "sleep 30\n", "wait_sec": 1})
	start := time.Now()
	stdin("\u0003")
	if out := stdin("echo after-interrupt\n"); !strings.Contains(out, "after-interrupt") || time.Since(start) > 10*time.Second {
		t.Errorf("Ctrl-C did not interrupt sleep: %q", out)
	}

	out = h.ok("write_stdin", map[string]any{"job_id": "job-1", "input": "exit\n", "wait_sec": 5})
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out, "state: exited") && time.Now().Before(deadline) {
		out = h.ok("write_stdin", map[string]any{"job_id": "job-1", "wait_sec": 1})
	}
	if !strings.Contains(out, "state: exited") {
		t.Errorf("shell should have exited: %s", out)
	}
}

func TestInterruptWithoutTTY(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	out := h.ok("run_shell", map[string]any{"script": "sleep 30", "wait_sec": 0, "timeout_sec": 60})
	if !strings.Contains(out, "job-1 is still running") {
		t.Fatalf("expected a background job: %s", out)
	}
	h.fail("write_stdin", map[string]any{"job_id": "job-1", "input": "hello\n"}, "tty=true")
	h.ok("write_stdin", map[string]any{"job_id": "job-1", "input": "\u0003", "wait_sec": 0})
	out = h.ok("get_job_output", map[string]any{"job_id": "job-1", "wait_sec": 10})
	if strings.Contains(out, "state: running") {
		t.Errorf("Ctrl-C should end the job: %s", out)
	}
}
