package tools

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/config"
	"github.com/yh2237/sh-mcp/internal/security"
)

func TestReadLineTruncatesOnUTF8Boundary(t *testing.T) {
	br := bufio.NewReader(strings.NewReader(strings.Repeat("あ", 10) + "\n"))
	line, err := readLine(br, 10)
	if err != nil {
		t.Fatalf("readLine: %v", err)
	}
	if !utf8.Valid(line) {
		t.Fatalf("truncated line is not valid UTF-8: %q", line)
	}
	want := "あああ …[line truncated]"
	if string(line) != want {
		t.Fatalf("got %q, want %q", string(line), want)
	}
}

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

func TestRelayContentMergesAdjacentText(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "first"},
			&mcp.TextContent{Text: "second"},
			&mcp.ImageContent{Data: []byte("img"), MIMEType: "image/png"},
			&mcp.TextContent{Text: "third"},
			&mcp.TextContent{Text: "fourth"},
		},
	}
	got := relayContent(res)
	if len(got) != 3 {
		t.Fatalf("got %d content items, want 3", len(got))
	}
	if t0, ok := got[0].(*mcp.TextContent); !ok || t0.Text != "first\n\nsecond" {
		t.Fatalf("item[0] = %#v, want merged first+second", got[0])
	}
	if _, ok := got[1].(*mcp.ImageContent); !ok {
		t.Fatalf("item[1] = %#v, want ImageContent", got[1])
	}
	if t2, ok := got[2].(*mcp.TextContent); !ok || t2.Text != "third\n\nfourth" {
		t.Fatalf("item[2] = %#v, want merged third+fourth", got[2])
	}
}

func TestIsSubPathCaseSensitivity(t *testing.T) {
	if !isSubPath("dir/sub", "dir") {
		t.Error("expected dir/sub to be subpath of dir")
	}
	if isSubPath("dir2/sub", "dir") {
		t.Error("dir2/sub must not be subpath of dir")
	}
	wantCaseInsensitive := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	if got := isSubPath("dir/sub", "Dir"); got != wantCaseInsensitive {
		t.Errorf("isSubPath(dir/sub, Dir) = %v, want %v on %s", got, wantCaseInsensitive, runtime.GOOS)
	}
}
