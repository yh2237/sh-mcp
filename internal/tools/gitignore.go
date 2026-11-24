package tools

import (
	"bufio"
	"bytes"
	"io/fs"
	"path"
	"strings"

	"github.com/yh2237/sh-mcp/internal/security"
)

// ignoreRule is one .gitignore line. This covers the common subset of
// gitignore semantics: comments, negation, anchoring, directory-only rules
// and "**". It does not read global excludes or .git/info/exclude.
type ignoreRule struct {
	base    string // slash path of the directory holding the .gitignore ("." for root)
	pattern string
	neg     bool
	dirOnly bool
}

type ignorer []ignoreRule

func (ig ignorer) withFile(fsys fs.FS, dir string) ignorer {
	data, err := fs.ReadFile(fsys, path.Join(dir, ".gitignore"))
	if err != nil {
		return ig
	}
	out := ig[:len(ig):len(ig)] // force copy on append; siblings share the prefix
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{base: dir}
		if strings.HasPrefix(line, "!") {
			r.neg = true
			line = line[1:]
		}
		line = strings.TrimPrefix(line, `\`)
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		// A slash anywhere but the end anchors the pattern to this directory;
		// otherwise it matches at any depth.
		if strings.Contains(line, "/") {
			r.pattern = strings.TrimPrefix(line, "/")
		} else {
			r.pattern = "**/" + line
		}
		out = append(out, r)
	}
	return out
}

// The last matching rule wins.
func (ig ignorer) ignored(rel string, isDir bool) bool {
	res := false
	for _, r := range ig {
		if r.dirOnly && !isDir {
			continue
		}
		sub := rel
		if r.base != "." {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			sub = rel[len(r.base)+1:]
		}
		if security.MatchGlob(r.pattern, sub) {
			res = !r.neg
		}
	}
	return res
}
