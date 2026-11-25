package tools

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/security"
)

const (
	searchLineChars      = 400
	defaultSearchResults = 200
	maxSearchResults     = 2000
)

type searchInput struct {
	Pattern          string `json:"pattern,omitempty" jsonschema:"RE2 regular expression to search file contents for. Omit to list file names matching glob instead"`
	Path             string `json:"path,omitempty" jsonschema:"directory (or single file) to search, absolute or relative to default_dir (default: default_dir)"`
	Glob             string `json:"glob,omitempty" jsonschema:"only search files whose path relative to path matches, e.g. '*.go' (any depth when it has no '/') or 'src/**/*.ts'"`
	CaseInsensitive  bool   `json:"case_insensitive,omitempty"`
	ContextLines     int    `json:"context_lines,omitempty" jsonschema:"lines of context around each match, 0-10"`
	MaxResults       int    `json:"max_results,omitempty" jsonschema:"maximum matching lines (or file names) to return"`
	RespectGitignore *bool  `json:"respect_gitignore,omitempty" jsonschema:"skip files ignored by .gitignore (default true)"`
}

func registerSearch(s *mcp.Server, d *Deps) {
	addTool(s, d, &mcp.Tool{
		Name:  "search_files",
		Title: "Search files",
		Description: "Search file contents with a regular expression (RE2 syntax), or list files by glob when pattern is omitted. " +
			"Output is grep-style: 'path:line:text' for matches and 'path-line-text' for context lines. " +
			"Skips .git, binary files, files larger than the server's max_file_bytes and (by default) .gitignore'd paths.",
		Annotations: readOnlyAnn,
	}, func(in searchInput) map[string]any {
		return map[string]any{"path": in.Path, "pattern": in.Pattern, "glob": in.Glob}
	}, d.search)
}

func (d *Deps) search(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (string, error) {
	p := in.Path
	if p == "" {
		p = "."
	}
	r, err := d.Guard.Resolve(p)
	if err != nil {
		return "", err
	}
	var re *regexp.Regexp
	if in.Pattern != "" {
		expr := in.Pattern
		if in.CaseInsensitive {
			expr = "(?i)" + expr
		}
		if re, err = regexp.Compile(expr); err != nil {
			return "", fmt.Errorf("invalid pattern: %w", err)
		}
	} else if in.Glob == "" {
		return "", fmt.Errorf("pattern or glob is required")
	}
	maxResults := in.MaxResults
	if maxResults <= 0 || maxResults > maxSearchResults {
		maxResults = defaultSearchResults
	}
	ctxLines := min(max(in.ContextLines, 0), 10)
	gitignore := true
	if in.RespectGitignore != nil {
		gitignore = *in.RespectGitignore
	}

	fsys := r.Root.FS()
	base := filepath.ToSlash(r.Rel)
	info, err := fs.Stat(fsys, base)
	if err != nil {
		return "", err
	}

	// Load .gitignore files from the root down to the search base so rules in
	// parent directories apply.
	var ig ignorer
	ignorers := map[string]ignorer{}
	if gitignore && info.IsDir() {
		dir := "."
		ig = ig.withFile(fsys, dir)
		if base != "." {
			for _, part := range strings.Split(base, "/") {
				dir = path.Join(dir, part)
				ig = ig.withFile(fsys, dir)
			}
		}
		ignorers[base] = ig
	}

	s := &searcher{d: d, re: re, ctxLines: ctxLines, max: maxResults, rootDir: r.RootDir, base: base}
	if !info.IsDir() {
		if in.Glob == "" || matchSearchGlob(in.Glob, path.Base(base)) {
			s.searchFile(fsys, base, path.Base(base))
		}
		return s.result(r.Abs), nil
	}

	err = fs.WalkDir(fsys, base, func(p string, e fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.full() {
			return fs.SkipAll
		}
		if err != nil {
			if p == base {
				return err
			}
			return nil // unreadable entry: skip
		}
		if p == base {
			return nil
		}
		abs := filepath.Join(r.RootDir, filepath.FromSlash(p))
		if d.isDeniedEntry(abs, e) {
			if e.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		parentIg := ignorers[path.Dir(p)]
		if e.IsDir() {
			if e.Name() == ".git" || (gitignore && parentIg.ignored(p, true)) {
				return fs.SkipDir
			}
			if gitignore {
				ignorers[p] = parentIg.withFile(fsys, p)
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		if gitignore && parentIg.ignored(p, false) {
			return nil
		}
		rel := strings.TrimPrefix(p, base+"/")
		if base == "." {
			rel = p
		}
		if in.Glob != "" && !matchSearchGlob(in.Glob, rel) {
			return nil
		}
		s.searchFile(fsys, p, rel)
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return "", err
	}
	return s.result(r.Abs), nil
}

func matchSearchGlob(glob, rel string) bool {
	if !strings.Contains(glob, "/") {
		return security.MatchGlob(glob, path.Base(rel))
	}
	return security.MatchGlob(glob, rel)
}

type searcher struct {
	d        *Deps
	re       *regexp.Regexp
	ctxLines int
	max      int
	rootDir  string
	base     string

	out     strings.Builder
	results int
	files   int
	skipped int
}

func (s *searcher) full() bool { return s.results >= s.max }

func (s *searcher) searchFile(fsys fs.FS, p, rel string) {
	if s.re == nil {
		fmt.Fprintf(&s.out, "%s\n", rel)
		s.results++
		return
	}
	info, err := fs.Stat(fsys, p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > s.d.Cfg.MaxFileBytes {
		s.skipped++
		return
	}
	f, err := fsys.Open(p)
	if err != nil {
		s.skipped++
		return
	}
	data, err := readLimit(f, p, s.d.Cfg.MaxFileBytes)
	f.Close()
	if err != nil {
		s.skipped++
		return
	}
	if bytes.IndexByte(data[:min(len(data), binarySniffBytes)], 0) >= 0 {
		return
	}
	lines := strings.Split(strings.ToValidUTF8(string(data), "�"), "\n")
	for i, line := range lines {
		// Match CRLF files line by line like LF ones ("^ready$").
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	lastPrinted := -1
	matchedFile := false
	for i, line := range lines {
		if s.full() {
			break
		}
		if !s.re.MatchString(line) {
			continue
		}
		if !matchedFile {
			matchedFile = true
			s.files++
		}
		from := max(i-s.ctxLines, lastPrinted+1)
		if s.ctxLines > 0 && lastPrinted >= 0 && from > lastPrinted+1 {
			s.out.WriteString("--\n")
		}
		for j := from; j < i; j++ {
			s.printLine(rel, j, '-', lines[j])
		}
		s.printLine(rel, i, ':', line)
		s.results++
		lastPrinted = i
		for j := i + 1; j <= min(i+s.ctxLines, len(lines)-1); j++ {
			if s.re.MatchString(lines[j]) {
				break // printed as a match on the next iteration
			}
			s.printLine(rel, j, '-', lines[j])
			lastPrinted = j
		}
	}
	if matchedFile && s.ctxLines > 0 {
		s.out.WriteString("--\n")
	}
}

func (s *searcher) printLine(rel string, idx int, sep byte, text string) {
	text = truncate(text, searchLineChars)
	fmt.Fprintf(&s.out, "%s%c%d%c%s\n", rel, sep, idx+1, sep, text)
}

func (s *searcher) result(searchDir string) string {
	var b strings.Builder
	if s.results == 0 {
		b.WriteString("(no matches)\n")
	} else {
		b.WriteString(s.out.String())
	}
	fmt.Fprintf(&b, "[searched %s | %d results", searchDir, s.results)
	if s.re != nil {
		fmt.Fprintf(&b, " in %d files", s.files)
	}
	if s.skipped > 0 {
		fmt.Fprintf(&b, " | %d files skipped (too large or unreadable)", s.skipped)
	}
	b.WriteString("]")
	if s.full() {
		fmt.Fprintf(&b, "\n(result limit %d reached; narrow the search or raise max_results)", s.max)
	}
	return b.String()
}
