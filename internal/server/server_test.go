package server

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/config"
)

type harness struct {
	t    *testing.T
	dir  string
	sess *mcp.ClientSession
}

func newHarness(t *testing.T, mode string, mutate func(*config.Config)) *harness {
	t.Helper()
	return newHarnessWith(t, mode, mutate, nil, nil)
}

func newHarnessWith(t *testing.T, mode string, mutate func(*config.Config), copts *mcp.ClientOptions, sopts *mcp.ClientSessionOptions) *harness {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = mode
	cfg.AllowedDirs = []string{dir}
	cfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, copts)
	cs, err := client.Connect(ctx, ct, sopts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &harness{t: t, dir: dir, sess: cs}
}

// approver is a client that answers sh-mcp's approval prompts.
type approver struct {
	mu       sync.Mutex
	accept   bool
	messages []string
}

func (a *approver) options() *mcp.ClientOptions {
	return &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.messages = append(a.messages, req.Params.Message)
		if a.accept {
			return &mcp.ElicitResult{Action: "accept"}, nil
		}
		return &mcp.ElicitResult{Action: "decline"}, nil
	}}
}

func (a *approver) set(accept bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.accept = accept
	a.messages = nil
}

func (a *approver) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.messages...)
}

func TestAskModeApproval(t *testing.T) {
	// The latest protocol answers approvals by multi round-trip requests;
	// 2025-11-25 (what Gemini speaks) gets an elicitation from the server.
	for _, version := range []string{"", "2025-11-25"} {
		t.Run("protocol="+version, func(t *testing.T) {
			a := &approver{}
			h := newHarnessWith(t, config.ModeAsk, func(c *config.Config) { c.AllowedCommands = []string{"go"} },
				a.options(), &mcp.ClientSessionOptions{ProtocolVersion: version})

			a.set(false)
			h.fail("write_file", map[string]any{"path": "x.txt", "content": "hi"}, "did not approve")
			if _, err := os.Stat(filepath.Join(h.dir, "x.txt")); err == nil {
				t.Error("declined write_file created the file")
			}
			if asked := a.asked(); len(asked) != 1 || !strings.Contains(asked[0], "x.txt") || !strings.Contains(asked[0], "hi") {
				t.Errorf("prompt: %q", asked)
			}

			a.set(true)
			h.ok("write_file", map[string]any{"path": "x.txt", "content": "hi"})
			out := h.ok("edit_file", map[string]any{"path": "x.txt", "edits": edits("hi", "hello")})
			if !strings.Contains(out, "+hello") {
				t.Errorf("edit: %s", out)
			}
			if asked := a.asked(); len(asked) != 2 || !strings.Contains(asked[1], "+hello") {
				t.Errorf("edit prompt should show the diff: %q", asked)
			}

			a.set(false)
			h.ok("read_file", map[string]any{"path": "x.txt"})
			h.ok("run_command", map[string]any{"command": "go", "args": []string{"version"}})
			if asked := a.asked(); len(asked) != 0 {
				t.Errorf("reads and --allow-command programs need no approval: %q", asked)
			}
			h.fail("run_shell", map[string]any{"script": "echo hi"}, "did not approve")
			if asked := a.asked(); len(asked) != 1 || !strings.Contains(asked[0], "echo hi") {
				t.Errorf("shell prompt: %q", asked)
			}
		})
	}
}

func TestAskModeWithoutElicitation(t *testing.T) {
	// Clients that cannot show prompts rely on their own confirmation, which
	// sh-mcp cannot observe: the call runs.
	h := newHarness(t, config.ModeAsk, nil)
	h.ok("write_file", map[string]any{"path": "y.txt", "content": "hi"})
	if out := h.ok("get_environment", nil); !strings.Contains(out, "client's own confirmation") {
		t.Errorf("get_environment should explain approvals: %s", out)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLogsClientCapabilities(t *testing.T) {
	cfg := config.Default()
	cfg.AllowedDirs = []string{t.TempDir()}
	if err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	srv, err := New(cfg, "test", slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "elicit-client", Version: "1.2.3"}, &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "decline"}, nil
		},
	})
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), `"msg":"client connected"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no client log line:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []string{`"client":"elicit-client"`, `"client_version":"1.2.3"`, `"elicitation":`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %s:\n%s", want, logs.String())
		}
	}
}

func (h *harness) result(name string, args map[string]any) *mcp.CallToolResult {
	h.t.Helper()
	res, err := h.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

func (h *harness) call(name string, args map[string]any) (string, bool) {
	h.t.Helper()
	res := h.result(name, args)
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func (h *harness) ok(name string, args map[string]any) string {
	h.t.Helper()
	text, isErr := h.call(name, args)
	if isErr {
		h.t.Fatalf("%s(%v) failed: %s", name, args, text)
	}
	return text
}

func (h *harness) fail(name string, args map[string]any, want string) {
	h.t.Helper()
	text, isErr := h.call(name, args)
	if !isErr {
		h.t.Fatalf("%s(%v) succeeded, want error containing %q; got %s", name, args, want, text)
	}
	if !strings.Contains(text, want) {
		h.t.Fatalf("%s error = %q, want it to contain %q", name, text, want)
	}
}

func (h *harness) writeRaw(rel, content string) {
	h.t.Helper()
	p := filepath.Join(h.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) readRaw(rel string) string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *harness) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(h.dir, rel))
	return err == nil
}

func edits(pairs ...string) []map[string]any {
	var out []map[string]any
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]any{"old_text": pairs[i], "new_text": pairs[i+1]})
	}
	return out
}

func TestToolsPerMode(t *testing.T) {
	cases := []struct {
		mode    string
		mutate  func(*config.Config)
		want    []string
		notWant []string
	}{
		{config.ModeRead, nil, []string{"read_file", "search_files", "get_environment"}, []string{"write_file", "run_command", "get_job_output"}},
		{config.ModeAsk, nil, []string{"write_file", "edit_file", "run_command", "run_shell", "kill_job", "start_mcp_server"}, nil},
		{config.ModeFull, nil, []string{"write_file", "edit_file", "delete_file", "move_file", "run_command", "run_shell", "list_jobs"}, nil},
	}
	for _, c := range cases {
		h := newHarness(t, c.mode, c.mutate)
		res, err := h.sess.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]*mcp.Tool{}
		for _, tool := range res.Tools {
			got[tool.Name] = tool
			if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || tool.Annotations.OpenWorldHint == nil {
				t.Errorf("%s: annotations must set every hint", tool.Name)
			}
			if c.mode == config.ModeRead && !tool.Annotations.ReadOnlyHint {
				t.Errorf("read mode exposes %s", tool.Name)
			}
		}
		for _, w := range c.want {
			if got[w] == nil {
				t.Errorf("mode %s: missing %s", c.mode, w)
			}
		}
		for _, w := range c.notWant {
			if got[w] != nil {
				t.Errorf("mode %s: should not have %s", c.mode, w)
			}
		}
	}
}

func TestReadWriteEdit(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)

	out := h.ok("write_file", map[string]any{"path": "a/b/hello.txt", "content": "one\ntwo\nthree\n", "create_dirs": true})
	if !strings.Contains(out, "created") {
		t.Errorf("write: %s", out)
	}
	out = h.ok("read_file", map[string]any{"path": "a/b/hello.txt"})
	if !strings.Contains(out, "     2\ttwo") || !strings.Contains(out, "lines 1-3 of 3") {
		t.Errorf("read: %s", out)
	}
	out = h.ok("read_file", map[string]any{"path": "a/b/hello.txt", "start_line": 2, "end_line": 2})
	if strings.Contains(out, "one") || !strings.Contains(out, "two") || !strings.Contains(out, "start_line=3") {
		t.Errorf("range read: %s", out)
	}

	out = h.ok("edit_file", map[string]any{"path": "a/b/hello.txt", "edits": edits("two", "TWO\n2", "three", "3")})
	if !strings.Contains(out, "-two") || !strings.Contains(out, "+TWO") || !strings.Contains(out, "+3") || !strings.Contains(out, "2 replacement(s)") {
		t.Errorf("edit diff: %s", out)
	}
	if got := h.readRaw("a/b/hello.txt"); got != "one\nTWO\n2\n3\n" {
		t.Errorf("after edit: %q", got)
	}

	// All or nothing: the second edit fails, so the first is not applied.
	h.fail("edit_file", map[string]any{"path": "a/b/hello.txt", "edits": edits("one", "1", "missing", "x")}, "edits[1]: old_text not found")
	if got := h.readRaw("a/b/hello.txt"); got != "one\nTWO\n2\n3\n" {
		t.Errorf("failed edit changed the file: %q", got)
	}

	h.writeRaw("dup.txt", "x\nx\n")
	h.fail("edit_file", map[string]any{"path": "dup.txt", "edits": edits("x", "y")}, "matches 2 places")
	h.ok("edit_file", map[string]any{"path": "dup.txt", "edits": []map[string]any{{"old_text": "x", "new_text": "y", "replace_all": true}}})
	if got := h.readRaw("dup.txt"); got != "y\ny\n" {
		t.Errorf("replace_all: %q", got)
	}
}

func TestEditPreservesCRLFAndBOM(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	h.writeRaw("win.txt", "\xEF\xBB\xBFfirst\r\nsecond\r\nthird\r\n")
	h.ok("edit_file", map[string]any{"path": "win.txt", "edits": edits("first\nsecond", "first\nmiddle\nsecond")})
	if got := h.readRaw("win.txt"); got != "\xEF\xBB\xBFfirst\r\nmiddle\r\nsecond\r\nthird\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestWritePreservesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	h := newHarness(t, config.ModeFull, nil)
	h.writeRaw("run.sh", "echo hi\n")
	os.Chmod(filepath.Join(h.dir, "run.sh"), 0o755)
	h.ok("write_file", map[string]any{"path": "run.sh", "content": "echo bye\n"})
	fi, _ := os.Stat(filepath.Join(h.dir, "run.sh"))
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

func TestDeleteAndMove(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	h.writeRaw("a.txt", "a")
	h.writeRaw("dir/b.txt", "b")
	h.writeRaw("dir/sub/c.txt", "c")

	h.ok("move_file", map[string]any{"source": "a.txt", "destination": "dir/a2.txt"})
	if h.exists("a.txt") || h.readRaw("dir/a2.txt") != "a" {
		t.Error("move did not happen")
	}
	h.fail("move_file", map[string]any{"source": "dir/b.txt", "destination": "dir/a2.txt"}, "already exists")
	h.ok("move_file", map[string]any{"source": "dir/b.txt", "destination": "dir/a2.txt", "overwrite": true})
	if h.readRaw("dir/a2.txt") != "b" {
		t.Error("overwrite did not happen")
	}
	h.fail("move_file", map[string]any{"source": "dir", "destination": "dir/sub/dir"}, "into itself")

	h.fail("delete_file", map[string]any{"path": "dir"}, "not empty")
	h.fail("delete_file", map[string]any{"path": "."}, "cannot be deleted")
	h.writeRaw("dir/.env", "SECRET=1")
	h.fail("delete_file", map[string]any{"path": "dir", "recursive": true}, "protected by --deny-glob")
	os.Remove(filepath.Join(h.dir, "dir", ".env"))
	h.ok("delete_file", map[string]any{"path": "dir", "recursive": true})
	if h.exists("dir") {
		t.Error("directory still exists")
	}
}

func TestReadImage(t *testing.T) {
	h := newHarness(t, config.ModeRead, nil)
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	h.writeRaw("pic.png", buf.String())
	res := h.result("read_file", map[string]any{"path": "pic.png"})
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("read image failed: %+v", res)
	}
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" || !bytes.Equal(img.Data, buf.Bytes()) {
		t.Errorf("unexpected content: %#v", res.Content[0])
	}
	h.writeRaw("bin.dat", "a\x00b")
	h.fail("read_file", map[string]any{"path": "bin.dat"}, "binary")
}

func TestPathPolicy(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	h.fail("read_file", map[string]any{"path": "../../etc/passwd"}, "outside the allowed directories")
	h.fail("write_file", map[string]any{"path": ".env", "content": "x"}, "protected by --deny-glob")
	h.writeRaw(".env", "SECRET=1")
	h.fail("read_file", map[string]any{"path": ".env"}, "protected by --deny-glob")
	out := h.ok("list_dir", map[string]any{"path": ".", "include_hidden": true})
	if strings.Contains(out, ".env") {
		t.Errorf("list_dir shows denied file: %s", out)
	}
	h.fail("run_command", map[string]any{"command": "go", "args": []string{"version"}, "cwd": filepath.Dir(h.dir)}, "outside the allowed directories")
}

func TestListAndSearch(t *testing.T) {
	h := newHarness(t, config.ModeRead, nil)
	h.writeRaw(".gitignore", "build/\n*.log\n!keep.log\n")
	h.writeRaw("src/main.go", "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")
	h.writeRaw("src/util.go", "package main\n\n// Hello helper\nfunc helper() {}\n")
	h.writeRaw("build/out.go", "func hello() {}\n")
	h.writeRaw("debug.log", "hello\n")
	h.writeRaw("keep.log", "hello\n")
	h.writeRaw("bin.dat", "hello\x00world")

	out := h.ok("list_dir", map[string]any{"depth": 2})
	if !strings.Contains(out, "src/") || !strings.Contains(out, "src/main.go") || strings.Contains(out, ".gitignore") {
		t.Errorf("list_dir: %s", out)
	}

	out = h.ok("search_files", map[string]any{"pattern": "hello", "case_insensitive": true})
	for _, want := range []string{"src/main.go:4:", "src/util.go:3:", "keep.log:1:"} {
		if !strings.Contains(out, want) {
			t.Errorf("search missing %s:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"build/out.go", "debug.log", "bin.dat"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("search should skip %s:\n%s", unwanted, out)
		}
	}
	out = h.ok("search_files", map[string]any{"pattern": "hello", "respect_gitignore": false, "glob": "*.go"})
	if !strings.Contains(out, "build/out.go") || strings.Contains(out, "keep.log") {
		t.Errorf("search without gitignore: %s", out)
	}
	out = h.ok("search_files", map[string]any{"glob": "src/**/*.go"})
	if !strings.Contains(out, "src/main.go") || !strings.Contains(out, "src/util.go") {
		t.Errorf("file search: %s", out)
	}
	out = h.ok("search_files", map[string]any{"pattern": "println", "context_lines": 1, "path": "src"})
	if !strings.Contains(out, "main.go-3-func main() {") || !strings.Contains(out, "main.go:4:") {
		t.Errorf("context: %s", out)
	}
}

func TestRunCommand(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	out := h.ok("run_command", map[string]any{"command": "go", "args": []string{"env", "GOOS"}})
	if !strings.Contains(out, "exit_code: 0") || !strings.Contains(out, runtime.GOOS) {
		t.Errorf("run_command: %s", out)
	}
	out = h.ok("run_command", map[string]any{"command": "go", "args": []string{"no-such-subcommand"}})
	if strings.Contains(out, "exit_code: 0") || !strings.Contains(out, "unknown command") {
		t.Errorf("non-zero exit: %s", out)
	}
	h.fail("run_command", map[string]any{"command": "no-such-binary-xyz"}, "not found")
	if out := h.ok("list_jobs", nil); out != "(no jobs)" {
		t.Errorf("finished commands should not stay listed: %s", out)
	}
}

func sleepScript(sec int) string {
	s := strconv.Itoa(sec)
	if runtime.GOOS == "windows" {
		// ping runs as a grandchild of the shell and inherits the output pipe.
		return "Write-Output started; ping -n " + s + " 127.0.0.1 > $null; ping -n " + s + " 127.0.0.1"
	}
	return "echo started; sleep " + s + " & sleep " + s
}

func TestRunShellAndTimeoutKillsTree(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	script := "echo hello && echo oops 1>&2"
	if runtime.GOOS == "windows" {
		script = "Write-Output 'hello 日本語'; [Console]::Error.WriteLine('oops')"
	}
	out := h.ok("run_shell", map[string]any{"script": script})
	if !strings.Contains(out, "hello") || !strings.Contains(out, "oops") {
		t.Errorf("run_shell: %s", out)
	}
	if runtime.GOOS == "windows" && !strings.Contains(out, "日本語") {
		t.Errorf("non-ASCII output garbled: %s", out)
	}

	start := time.Now()
	out = h.ok("run_shell", map[string]any{"script": sleepScript(30), "timeout_sec": 2, "wait_sec": 20})
	elapsed := time.Since(start)
	if !strings.Contains(out, "timeout_sec 2 reached") {
		t.Errorf("expected timeout: %s", out)
	}
	// Without killing the tree, the grandchild would hold the pipe until
	// WaitDelay (3s) expires on top of the 2s timeout.
	if elapsed > 4500*time.Millisecond {
		t.Errorf("timeout took %s; process tree not killed?\n%s", elapsed, out)
	}
}

func TestBackgroundJobs(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	out := h.ok("run_shell", map[string]any{"script": sleepScript(30), "wait_sec": 1, "timeout_sec": 0})
	if !strings.Contains(out, "job-1 is still running") || !strings.Contains(out, "no timeout") {
		t.Fatalf("expected a background job: %s", out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out = h.ok("get_job_output", map[string]any{"job_id": "job-1", "wait_sec": 1})
		if strings.Contains(out, "started") || time.Now().After(deadline) {
			break
		}
	}
	if !strings.Contains(out, "started") || !strings.Contains(out, "state: running") {
		t.Fatalf("job output: %s", out)
	}
	next := regexp.MustCompile(`next_offset: (\d+)`).FindStringSubmatch(out)[1]
	n, _ := strconv.Atoi(next)
	out = h.ok("get_job_output", map[string]any{"job_id": "job-1", "offset": n, "wait_sec": 0})
	if !strings.Contains(out, "(no new output)") {
		t.Errorf("incremental read: %s", out)
	}
	if out := h.ok("list_jobs", nil); !strings.Contains(out, "job-1 state: running") {
		t.Errorf("list_jobs: %s", out)
	}
	out = h.ok("kill_job", map[string]any{"job_id": "job-1"})
	if !strings.Contains(out, "state: killed") {
		t.Errorf("kill_job: %s", out)
	}

	out = h.ok("run_shell", map[string]any{"script": sleepScript(30), "wait_sec": 0, "timeout_sec": 1})
	if !strings.Contains(out, "job-2 is still running") {
		t.Fatalf("wait_sec 0 should return at once: %s", out)
	}
	out = h.ok("get_job_output", map[string]any{"job_id": "job-2", "wait_sec": 10})
	if !strings.Contains(out, "state: timed_out") {
		t.Errorf("job timeout: %s", out)
	}
}

func TestRunningJobOutputIsContiguous(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	script := "head -c 100000 /dev/zero | tr '\\0' x; echo; sleep 30"
	if runtime.GOOS == "windows" {
		script = "Write-Output ('x' * 100000); ping -n 30 127.0.0.1 > $null"
	}
	out := h.ok("run_shell", map[string]any{"script": script, "wait_sec": 3, "timeout_sec": 60})
	t.Cleanup(func() { h.call("kill_job", map[string]any{"job_id": "job-1"}) })
	m := regexp.MustCompile(`offset=(\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("expected a running job: %.300s", out)
	}
	xs := func(s string) int {
		_, body, _ := strings.Cut(s, "--- output ---\n")
		return strings.Count(body, "x")
	}
	shown := xs(out)
	next, _ := strconv.Atoi(m[1])
	if next != shown {
		t.Errorf("next offset %d but %d bytes of output shown: the rest would be skipped", next, shown)
	}
	rest := h.ok("get_job_output", map[string]any{"job_id": "job-1", "offset": next, "wait_sec": 0})
	if total := shown + xs(rest); total != 100000 {
		t.Errorf("read %d of 100000 bytes across both calls", total)
	}
}

func TestMoveRefusesProtectedTree(t *testing.T) {
	h := newHarness(t, config.ModeFull, func(c *config.Config) { c.DenyGlobs = append(c.DenyGlobs, "**/vault/**") })
	if err := os.MkdirAll(filepath.Join(h.dir, "vault", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "vault", "sub", "notes.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.dir, "box", "vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "box", "vault", "key.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.fail("move_file", map[string]any{"source": "box", "destination": "public"}, "protected by --deny-glob")
	if _, err := os.Stat(filepath.Join(h.dir, "box", "vault", "key.txt")); err != nil {
		t.Errorf("refused move changed the tree: %v", err)
	}
	h.ok("write_file", map[string]any{"path": "plain/a.txt", "content": "a", "create_dirs": true})
	h.ok("move_file", map[string]any{"source": "plain", "destination": "moved"})
}

func TestSearchCRLFAndFileGlob(t *testing.T) {
	h := newHarness(t, config.ModeRead, nil)
	if err := os.WriteFile(filepath.Join(h.dir, "log.txt"), []byte("start\r\nready\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := h.ok("search_files", map[string]any{"pattern": "^ready$"}); !strings.Contains(out, "log.txt:2:ready") {
		t.Errorf("CRLF line should match ^ready$: %s", out)
	}
	if out := h.ok("search_files", map[string]any{"pattern": "ready", "path": "log.txt", "glob": "*.go"}); !strings.Contains(out, "(no matches)") {
		t.Errorf("glob should also filter a single file: %s", out)
	}
}

func TestReadLargeFileStopsAfterRange(t *testing.T) {
	h := newHarness(t, config.ModeRead, func(c *config.Config) { c.MaxFileBytes = 100 })
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "big.log"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := h.ok("read_file", map[string]any{"path": "big.log", "end_line": 3})
	if !strings.Contains(out, "line 3") || strings.Contains(out, "line 4\n") || !strings.Contains(out, "total not counted") || !strings.Contains(out, "start_line=4") {
		t.Errorf("large file: %s", out)
	}
	if out := h.ok("read_file", map[string]any{"path": "big.log", "start_line": 199}); !strings.Contains(out, "lines 199-200 of 200") {
		t.Errorf("reading to the end counts the lines: %s", out)
	}
}
