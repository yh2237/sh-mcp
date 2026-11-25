//go:build unix

package server

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/yh2237/sh-mcp/internal/config"
)

func TestSpecialFilesAreRefused(t *testing.T) {
	h := newHarness(t, config.ModeFull, nil)
	if err := syscall.Mkfifo(filepath.Join(h.dir, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// Opening a FIFO with no writer would block forever.
	h.fail("read_file", map[string]any{"path": "pipe"}, "not a regular file")
	h.fail("edit_file", map[string]any{"path": "pipe", "edits": edits("a", "b")}, "not a regular file")
	h.ok("search_files", map[string]any{"pattern": "x", "path": "pipe"})
}
