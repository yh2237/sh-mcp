package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yh2237/sh-mcp/internal/config"
)

func gitRepo(t *testing.T, h *harness) func(args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = h.dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	return git
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitTools(t *testing.T) {
	h := newHarness(t, config.ModeRead, nil)
	git := gitRepo(t, h)
	writeTestFile(t, h.dir, "a.txt", "one\n")
	writeTestFile(t, h.dir, ".env", "TOKEN=old\n")
	git("add", "-f", "a.txt", ".env")
	git("commit", "-q", "-m", "first commit")
	writeTestFile(t, h.dir, "a.txt", "one\ntwo\n")
	writeTestFile(t, h.dir, ".env", "TOKEN=new-secret\n")
	writeTestFile(t, h.dir, "new.txt", "x\n")

	out := h.ok("git_status", nil)
	if !strings.Contains(out, "## main") || !strings.Contains(out, "a.txt") || !strings.Contains(out, "new.txt") {
		t.Errorf("status: %s", out)
	}
	if strings.Contains(out, " .env") || !strings.Contains(out, "protected by --deny-glob") {
		t.Errorf("status should hide .env: %s", out)
	}

	out = h.ok("git_diff", nil)
	if !strings.Contains(out, "+two") || strings.Contains(out, "secret") || strings.Contains(out, "TOKEN") {
		t.Errorf("diff should show a.txt but not .env: %s", out)
	}
	if out := h.ok("git_diff", map[string]any{"stat_only": true}); !strings.Contains(out, "a.txt") || strings.Contains(out, ".env") {
		t.Errorf("numstat: %s", out)
	}
	if out := h.ok("git_diff", map[string]any{"path": "a.txt", "base": "HEAD"}); !strings.Contains(out, "+two") {
		t.Errorf("diff of one file against HEAD: %s", out)
	}

	out = h.ok("git_log", map[string]any{"patch": true})
	if !strings.Contains(out, "first commit") || !strings.Contains(out, "+one") || strings.Contains(out, "TOKEN") {
		t.Errorf("log -p should hide .env: %s", out)
	}
	h.fail("git_diff", map[string]any{"base": "--output=x"}, "not a revision")
	h.fail("git_log", map[string]any{"revision": "-p"}, "not a revision")
}

func TestGitToolsIgnoreRepoCommands(t *testing.T) {
	h := newHarness(t, config.ModeRead, nil)
	git := gitRepo(t, h)
	writeTestFile(t, h.dir, "a.txt", "one\n")
	git("add", "a.txt")
	git("commit", "-q", "-m", "c")
	writeTestFile(t, h.dir, "a.txt", "two\n")
	// A repository's own config could make read-only git commands run
	// programs; the git tools must not.
	git("config", "core.fsmonitor", "echo x > fsmonitor-ran")
	git("config", "diff.evil.textconv", "echo x > textconv-ran; cat")
	git("config", "diff.external", "echo x > external-ran")
	writeTestFile(t, h.dir, ".gitattributes", "*.txt diff=evil\n")

	h.ok("git_status", nil)
	h.ok("git_diff", nil)
	h.ok("git_log", map[string]any{"patch": true})
	for _, marker := range []string{"fsmonitor-ran", "textconv-ran", "external-ran"} {
		if _, err := os.Stat(filepath.Join(h.dir, marker)); err == nil {
			t.Errorf("repository config ran a command (%s)", marker)
		}
	}
}
