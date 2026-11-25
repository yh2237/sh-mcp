package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yh2237/sh-mcp/internal/config"
	"github.com/yh2237/sh-mcp/internal/security"
)

func TestSearchMaxResultsClampsToUpperLimit(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := 0; i < 250; i++ {
		content.WriteString("needle\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	guard, err := security.NewGuard([]string{dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()

	d := &Deps{Cfg: &config.Config{Mode: config.ModeRead}, Guard: guard}
	out, err := d.search(context.Background(), nil, searchInput{
		Pattern:    "needle",
		MaxResults: maxSearchResults + 500,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "250 results in 1 files") {
		t.Fatalf("expected all 250 results when max_results exceeds upper limit, got:\n%s", out)
	}
}
