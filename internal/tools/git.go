package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/output"
)

// The git tools run git with fixed arguments, so they are read-only and need
// no approval in any mode, unlike git through run_command (whose arguments
// and repository config, e.g. core.pager or core.fsmonitor, can run
// arbitrary programs).

const (
	gitTimeout      = 30 * time.Second
	gitMaxOutput    = 1 << 20
	defaultLogCount = 20
	maxLogCount     = 200
)

// gitSafety overrides the repository settings that would make read-only
// commands run other programs, write to the repository, use the network or
// change the output format the filters below parse. Command-line -c wins over
// the repository's config and is inherited by git's subprocesses.
var gitSafety = []string{
	"--no-pager",
	"-c", "core.fsmonitor=false",
	"-c", "diff.autoRefreshIndex=false", // diff would otherwise rewrite the index
	"-c", "protocol.allow=never", // no fetching, e.g. of a partial clone's missing objects
	"-c", "core.quotePath=false",
	"-c", "status.relativePaths=true",
	"-c", "color.ui=false", "-c", "color.diff=false", "-c", "color.status=false",
}

// diffSafety keeps diffs from running external diff drivers, textconv filters
// and inline submodule diffs (a child git that would not get these flags),
// and fixes the format: paths relative to the directory, a/ and b/ prefixes,
// no renames (so a protected file shows under its own name).
var diffSafety = []string{
	"--no-ext-diff", "--no-textconv", "--submodule=short", "--no-color",
	"--relative", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/",
}

// gitEnv completes the safety settings: optional locks would let status
// rewrite the index, and a partial clone would fetch missing objects.
var gitEnv = []string{"GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1"}

type gitStatusInput struct {
	Path string `json:"path,omitempty" jsonschema:"directory in the repository, absolute or relative to default_dir (default: default_dir); only changes under it are shown"`
}

type gitDiffInput struct {
	Path     string `json:"path,omitempty" jsonschema:"file or directory to limit the diff to (default: default_dir)"`
	Staged   bool   `json:"staged,omitempty" jsonschema:"show staged changes (git diff --cached) instead of unstaged ones"`
	Base     string `json:"base,omitempty" jsonschema:"revision to compare with, e.g. HEAD or main (default: the index, or HEAD with staged)"`
	Target   string `json:"target,omitempty" jsonschema:"second revision: compare base with target instead of with the working tree"`
	StatOnly bool   `json:"stat_only,omitempty" jsonschema:"only list changed files with added/deleted line counts"`
}

type gitLogInput struct {
	Path     string `json:"path,omitempty" jsonschema:"file or directory to show the history of (default: default_dir)"`
	Revision string `json:"revision,omitempty" jsonschema:"revision or range, e.g. main or HEAD~5..HEAD (default: HEAD)"`
	MaxCount int    `json:"max_count,omitempty" jsonschema:"number of commits, 1-200 (default 20)"`
	Patch    bool   `json:"patch,omitempty" jsonschema:"include each commit's diff"`
}

func registerGit(s *mcp.Server, d *Deps) {
	addTool(s, d, &mcp.Tool{
		Name:        "git_status",
		Title:       "Git status",
		Description: "Show the branch and changed, staged and untracked files (git status --short --branch). Read-only; needs no approval.",
		Annotations: readOnlyAnn,
	}, func(in gitStatusInput) map[string]any { return map[string]any{"path": in.Path} }, d.gitStatus)

	addTool(s, d, &mcp.Tool{
		Name:  "git_diff",
		Title: "Git diff",
		Description: "Show changes as a unified diff: unstaged changes by default, staged ones with staged, or against base (and target) revisions. " +
			"Read-only; needs no approval. Files protected by --deny-glob are left out.",
		Annotations: readOnlyAnn,
	}, func(in gitDiffInput) map[string]any {
		return map[string]any{"path": in.Path, "staged": in.Staged, "base": in.Base, "target": in.Target}
	}, d.gitDiff)

	addTool(s, d, &mcp.Tool{
		Name:  "git_log",
		Title: "Git log",
		Description: "List recent commits (hash, date, author, subject), optionally for a path or revision range and with their diffs. " +
			"Read-only; needs no approval.",
		Annotations: readOnlyAnn,
	}, func(in gitLogInput) map[string]any {
		return map[string]any{"path": in.Path, "revision": in.Revision, "max_count": in.MaxCount, "patch": in.Patch}
	}, d.gitLog)
}

// gitTarget turns the path parameter into the directory git runs in and the
// pathspec to limit it to, both inside the allowed directories. A path that
// does not exist (a deleted file) is taken as a file.
func (d *Deps) gitTarget(p string) (dir, pathspec string, err error) {
	if p == "" {
		p = "."
	}
	r, err := d.Guard.ResolveRead(p)
	if err != nil {
		return "", "", err
	}
	defer r.Release()
	fi, err := r.Root.Stat(r.Rel)
	switch {
	case err == nil && fi.IsDir():
		return r.Abs, ".", nil
	case err == nil || errors.Is(err, fs.ErrNotExist):
		if r.Rel == "." {
			return "", "", fmt.Errorf("%s does not exist", r.Abs)
		}
		return filepath.Dir(r.Abs), filepath.Base(r.Abs), nil
	}
	return "", "", err
}

// commitRev resolves a revision to a commit id. Only commits are accepted:
// comparing trees or blobs (HEAD:dir) gives paths relative to them, which
// --deny-glob could not be checked against.
func (d *Deps) commitRev(ctx context.Context, dir, name, v string) (string, error) {
	if strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t\r\n") {
		return "", fmt.Errorf("%s %q is not a revision", name, v)
	}
	out, err := d.runGit(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", v+"^{commit}")
	id := strings.TrimSpace(out)
	if err != nil || id == "" {
		return "", fmt.Errorf("%s %q is not a commit", name, v)
	}
	return id, nil
}

// logRevision resolves a revision or range (A..B, A...B, ^A) for git log,
// accepting only commits on either side.
func (d *Deps) logRevision(ctx context.Context, dir, v string) (string, error) {
	for _, sep := range []string{"...", ".."} {
		a, b, ok := strings.Cut(v, sep)
		if !ok {
			continue
		}
		var ids [2]string
		for i, side := range []string{a, b} {
			if side == "" {
				continue // an empty side means HEAD
			}
			id, err := d.commitRev(ctx, dir, "revision", side)
			if err != nil {
				return "", err
			}
			ids[i] = id
		}
		return ids[0] + sep + ids[1], nil
	}
	neg := ""
	if strings.HasPrefix(v, "^") {
		neg, v = "^", v[1:]
	}
	id, err := d.commitRev(ctx, dir, "revision", v)
	if err != nil {
		return "", err
	}
	return neg + id, nil
}

func (d *Deps) gitStatus(ctx context.Context, _ *mcp.CallToolRequest, in gitStatusInput) (string, error) {
	dir, spec, err := d.gitTarget(in.Path)
	if err != nil {
		return "", err
	}
	out, err := d.runGit(ctx, dir, "status", "--short", "--branch", "--untracked-files=all", "--no-renames", "--", spec)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	shown, hidden := 0, 0
	for _, line := range strings.SplitAfter(out, "\n") {
		p, ok := statusPath(line)
		if ok && d.Guard.IsDenied(filepath.Join(dir, filepath.FromSlash(p))) {
			hidden++
			continue
		}
		if ok {
			shown++
		}
		b.WriteString(line)
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "(%d file(s) protected by --deny-glob not shown)\n", hidden)
	} else if shown == 0 {
		b.WriteString("(no changes)\n")
	}
	return gitResult(b.String(), "(no changes)"), nil
}

// statusPath returns the path of a git status --short --no-renames line, or
// false for the branch line.
func statusPath(line string) (string, bool) {
	line = strings.TrimRight(line, "\n")
	if len(line) < 4 || strings.HasPrefix(line, "## ") {
		return "", false
	}
	return unquoteGitPath(line[3:]), true
}

// unquoteGitPath undoes git's C-style quoting of unusual file names.
func unquoteGitPath(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

func (d *Deps) gitDiff(ctx context.Context, _ *mcp.CallToolRequest, in gitDiffInput) (string, error) {
	dir, spec, err := d.gitTarget(in.Path)
	if err != nil {
		return "", err
	}
	args := append([]string{"diff"}, diffSafety...)
	if in.StatOnly {
		args = append(args, "--numstat")
	}
	if in.Staged {
		args = append(args, "--cached")
	}
	args = append(args, "--end-of-options")
	if in.Target != "" && in.Base == "" {
		return "", errors.New("target needs base")
	}
	for _, rev := range []struct{ name, v string }{{"base", in.Base}, {"target", in.Target}} {
		if rev.v == "" {
			continue
		}
		id, err := d.commitRev(ctx, dir, rev.name, rev.v)
		if err != nil {
			return "", err
		}
		args = append(args, id)
	}
	out, err := d.runGit(ctx, dir, append(args, "--", spec)...)
	if err != nil {
		return "", err
	}
	if in.StatOnly {
		out = d.filterNumstat(dir, out)
	} else {
		out = d.filterDiff(dir, out)
	}
	return gitResult(out, "(no differences)"), nil
}

func (d *Deps) gitLog(ctx context.Context, _ *mcp.CallToolRequest, in gitLogInput) (string, error) {
	dir, spec, err := d.gitTarget(in.Path)
	if err != nil {
		return "", err
	}
	n := in.MaxCount
	if n <= 0 {
		n = defaultLogCount
	}
	n = min(n, maxLogCount)
	args := []string{"log", "--no-show-signature", "--date=short", "--format=commit %h %ad %an%n    %s", "-n", strconv.Itoa(n)}
	if in.Patch {
		args = append(args, "-p")
		args = append(args, diffSafety...)
	}
	args = append(args, "--end-of-options")
	if in.Revision != "" {
		rev, err := d.logRevision(ctx, dir, in.Revision)
		if err != nil {
			return "", err
		}
		args = append(args, rev)
	}
	out, err := d.runGit(ctx, dir, append(args, "--", spec)...)
	if err != nil {
		return "", err
	}
	if in.Patch {
		out = d.filterDiff(dir, out)
	}
	return gitResult(out, "(no commits)"), nil
}

// filterDiff removes the sections of files protected by --deny-glob from a
// diff or a log -p. Paths are relative to dir because of --relative. A section
// whose header cannot be parsed is left out too: failing open would show a
// protected file whenever git prints something unexpected.
func (d *Deps) filterDiff(dir, diff string) string {
	var b strings.Builder
	skipping := false
	for _, line := range strings.SplitAfter(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff "):
			paths, ok := diffHeaderPaths(line)
			skipping = !ok
			for _, p := range paths {
				if d.Guard.IsDenied(filepath.Join(dir, filepath.FromSlash(p))) {
					skipping = true
				}
			}
			switch {
			case !ok:
				b.WriteString("(a diff section that could not be checked against --deny-glob was left out)\n")
			case skipping:
				b.WriteString("(diff of a file protected by --deny-glob not shown)\n")
			}
		case strings.HasPrefix(line, "commit "):
			skipping = false
		}
		if !skipping {
			b.WriteString(line)
		}
	}
	return b.String()
}

// diffHeaderPaths extracts the paths of a "diff --git a/x b/y" line, or of a
// combined diff of a conflict ("diff --cc x", "diff --combined x").
func diffHeaderPaths(line string) ([]string, bool) {
	line = strings.TrimSuffix(line, "\n")
	for _, prefix := range []string{"diff --cc ", "diff --combined "} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return []string{unquoteGitPath(rest)}, rest != ""
		}
	}
	rest, ok := strings.CutPrefix(line, "diff --git ")
	if !ok {
		return nil, false
	}
	if strings.HasPrefix(rest, `"`) || strings.HasSuffix(rest, `"`) {
		// At least one name is quoted: "a/x" "b/y", "a/x" b/y or a/x "b/y".
		var out []string
		for _, f := range splitQuotedPair(rest) {
			p := unquoteGitPath(f)
			if !strings.HasPrefix(p, "a/") && !strings.HasPrefix(p, "b/") {
				return nil, false
			}
			out = append(out, p[2:])
		}
		return out, len(out) == 2
	}
	// --no-renames makes both names equal: "a/<p> b/<p>".
	if len(rest)%2 == 1 {
		half := (len(rest) - 1) / 2
		a, b := rest[:half], rest[half+1:]
		if rest[half] == ' ' && strings.HasPrefix(a, "a/") && strings.HasPrefix(b, "b/") && a[2:] == b[2:] {
			return []string{a[2:]}, true
		}
	}
	return nil, false
}

// splitQuotedPair splits the two (possibly quoted) names of a diff header.
func splitQuotedPair(s string) []string {
	var out []string
	for s != "" {
		if strings.HasPrefix(s, `"`) {
			end := 1
			for end < len(s) && s[end] != '"' {
				if s[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(s) {
				return nil
			}
			out = append(out, s[:end+1])
			s = strings.TrimPrefix(s[end+1:], " ")
			continue
		}
		f, rest, _ := strings.Cut(s, " ")
		out = append(out, f)
		s = rest
	}
	return out
}

func (d *Deps) filterNumstat(dir, out string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(out, "\n") {
		fields := strings.SplitN(strings.TrimRight(line, "\n"), "\t", 3)
		if len(fields) == 3 && d.Guard.IsDenied(filepath.Join(dir, filepath.FromSlash(unquoteGitPath(fields[2])))) {
			continue // --no-renames: one path per line
		}
		b.WriteString(line)
	}
	return b.String()
}

// capWriter keeps the first max bytes and discards the rest without failing
// the writer, so git is not killed by a broken pipe.
type capWriter struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
	}
	if w.buf.Len() >= w.max {
		w.over = true
	}
	return len(p), nil
}

func (d *Deps) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.Git, append(append([]string(nil), gitSafety...), args...)...)
	cmd.Dir = dir
	cmd.Env = append(d.Cmd.Env(), gitEnv...)
	stdout := &capWriter{max: gitMaxOutput}
	stderr := &capWriter{max: 8 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git did not finish within %s", gitTimeout)
		}
		msg := strings.TrimSpace(output.Text(stderr.buf.Bytes()))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git: %s", msg)
	}
	text := output.Text(stdout.buf.Bytes())
	if stdout.over {
		text += "\n(output truncated; narrow it down with path, max_count or stat_only)\n"
	}
	return text, nil
}

// gitResult shortens a git output for the caller. It must run after the
// --deny-glob filtering: cutting the middle first could drop a protected
// file's header but keep its content.
func gitResult(text, empty string) string {
	if strings.TrimSpace(text) == "" {
		return empty
	}
	return string(output.TruncateMiddle([]byte(text), maxResultBytes))
}
