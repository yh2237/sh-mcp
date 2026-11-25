package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestGitToolsKeepProtectedFilesHidden(t *testing.T) {
	h := newHarness(t, config.ModeRead, func(c *config.Config) { c.DenyGlobs = append(c.DenyGlobs, "**/vault/**") })
	git := gitRepo(t, h)
	if err := os.MkdirAll(filepath.Join(h.dir, "vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, h.dir, ".env", "TOKEN=base\n")
	writeTestFile(t, h.dir, "vault/settings.json", "secret-v1\n")
	git("add", "-f", ".env", "vault/settings.json")
	git("commit", "-q", "-m", "base")
	writeTestFile(t, h.dir, "vault/settings.json", "secret-v2\n")
	git("commit", "-q", "-am", "v2")

	// Comparing trees drops the vault/ prefix from the paths.
	h.fail("git_diff", map[string]any{"base": "HEAD~1:vault", "target": "HEAD:vault"}, "not a commit")
	h.fail("git_log", map[string]any{"revision": "HEAD:vault", "patch": true}, "not a commit")
	if out := h.ok("git_log", map[string]any{"revision": "HEAD~1..HEAD", "patch": true}); strings.Contains(out, "secret") {
		t.Errorf("log range shows a protected file: %s", out)
	}

	// A prefix setting must not defeat the header parsing.
	git("config", "diff.noprefix", "true")
	writeTestFile(t, h.dir, ".env", "TOKEN=noprefix\n")
	if out := h.ok("git_diff", nil); strings.Contains(out, "TOKEN") {
		t.Errorf("diff.noprefix exposed .env: %s", out)
	}
	git("checkout", "-q", "--", ".env")

	// After renaming .env, its old name and its deletion stay hidden. The new
	// file is not protected (read_file could read it too), so it may show.
	git("mv", ".env", "notes.txt")
	for _, tool := range []string{"git_status", "git_diff"} {
		args := map[string]any{}
		if tool == "git_diff" {
			args["staged"] = true
		}
		if out := h.ok(tool, args); strings.Contains(out, ".env") || !strings.Contains(out, "notes.txt") {
			t.Errorf("%s after renaming .env: %q", tool, out)
		}
	}
	git("reset", "-q", "--hard")

	// A merge conflict in .env shows as a combined diff ("diff --cc").
	git("checkout", "-q", "-b", "other", "HEAD~1")
	writeTestFile(t, h.dir, ".env", "TOKEN=theirs\n")
	git("commit", "-q", "-am", "theirs")
	git("checkout", "-q", "main")
	writeTestFile(t, h.dir, ".env", "TOKEN=ours\n")
	git("commit", "-q", "-am", "ours")
	cmd := exec.Command("git", "merge", "-q", "other")
	cmd.Dir = h.dir
	_ = cmd.Run() // conflicts on purpose
	if out := h.ok("git_diff", nil); strings.Contains(out, "TOKEN") {
		t.Errorf("conflict diff exposed .env: %s", out)
	}
}

func TestGitDiffDoesNotWriteIndex(t *testing.T) {
	h := newHarness(t, config.ModeRead, nil)
	git := gitRepo(t, h)
	writeTestFile(t, h.dir, "a.txt", "one\n")
	git("add", "a.txt")
	git("commit", "-q", "-m", "c")
	index := filepath.Join(h.dir, ".git", "index")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(index, old, old); err != nil {
		t.Fatal(err)
	}
	// Same content, new timestamp: git diff would refresh the index.
	if err := os.Chtimes(filepath.Join(h.dir, "a.txt"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	h.ok("git_diff", nil)
	h.ok("git_status", nil)
	if fi, err := os.Stat(index); err != nil || !fi.ModTime().Equal(old) {
		t.Errorf("a read-only git tool rewrote the index")
	}
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
