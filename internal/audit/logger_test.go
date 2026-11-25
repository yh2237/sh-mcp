package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoggerRecordAndReopen(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.jsonl")

	l, err := Open(logPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	l.Record(Entry{
		Tool:     "read_file",
		Args:     map[string]any{"path": "README.md"},
		Duration: 12 * time.Millisecond,
		OK:       true,
		Client:   "static-token",
		Session:  "sess-1",
	})

	rotated := filepath.Join(dir, "audit.jsonl.1")
	if runtime.GOOS != "windows" {
		if err := os.Rename(logPath, rotated); err != nil {
			t.Fatalf("Rename: %v", err)
		}
	}
	if err := l.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}

	l.Record(Entry{
		Tool: "write_file",
		OK:   false,
		Err:  "denied",
	})

	if runtime.GOOS != "windows" {
		first := readLogLines(t, rotated, 1)[0]
		if first["tool"] != "read_file" || first["ok"] != true || first["client"] != "static-token" || first["session"] != "sess-1" {
			t.Errorf("unexpected first record: %v", first)
		}
		second := readLogLines(t, logPath, 1)[0]
		if second["tool"] != "write_file" || second["ok"] != false || second["error"] != "denied" {
			t.Errorf("unexpected second record after reopen: %v", second)
		}
		return
	}
	records := readLogLines(t, logPath, 2)
	if records[0]["tool"] != "read_file" || records[0]["ok"] != true || records[0]["client"] != "static-token" || records[0]["session"] != "sess-1" {
		t.Errorf("unexpected first record: %v", records[0])
	}
	if records[1]["tool"] != "write_file" || records[1]["ok"] != false || records[1]["error"] != "denied" {
		t.Errorf("unexpected second record after reopen: %v", records[1])
	}
}

func TestNilLogger(t *testing.T) {
	var l *Logger
	l.Record(Entry{Tool: "read_file", OK: true})
	if err := l.Reopen(); err != nil {
		t.Errorf("Reopen on nil: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close on nil: %v", err)
	}
}

func readLogLines(t *testing.T, path string, want int) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != want {
		t.Fatalf("expected %d line(s) in %s, got %d: %q", want, path, len(lines), string(data))
	}
	out := make([]map[string]any, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &out[i]); err != nil {
			t.Fatalf("invalid JSON at line %d in %s: %v", i+1, path, err)
		}
	}
	return out
}
