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

// gitSafety disables the repository settings that make read-only commands
// run other programs. Command-line -c overrides the repository's config and
// is inherited by git's subprocesses.
var gitSafety = []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "-c", "color.ui=false"}

// diffSafety keeps diffs from running external diff drivers and textconv
// filters configured in the repository.
var diffSafety = []string{"--no-ext-diff", "--no-textconv", "--relative"}

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
	r, err := d.Guard.Resolve(p)
	if err != nil {
		return "", "", err
	}
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

// revision rejects values git could read as options.
func revision(name, v string) error {
	if strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("%s %q is not a revision", name, v)
	}
	return nil
}

func (d *Deps) gitStatus(ctx context.Context, _ *mcp.CallToolRequest, in gitStatusInput) (string, error) {
	dir, spec, err := d.gitTarget(in.Path)
	if err != nil {
		return "", err
	}
	out, err := d.runGit(ctx, dir, "status", "--short", "--branch", "--untracked-files=all", "--", spec)
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

// statusPath returns the (new) path of a git status --short line, or false
// for the branch line.
func statusPath(line string) (string, bool) {
	line = strings.TrimRight(line, "\n")
	if len(line) < 4 || strings.HasPrefix(line, "## ") {
		return "", false
	}
	p := line[3:]
	if _, after, ok := strings.Cut(p, " -> "); ok {
		p = after
	}
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
	}
	return p, true
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
		if err := revision(rev.name, rev.v); err != nil {
			return "", err
		}
		args = append(args, rev.v)
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
		if err := revision("revision", in.Revision); err != nil {
			return "", err
		}
		args = append(args, in.Revision)
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
// diff or a log -p. Paths are relative to dir because of --relative.
func (d *Deps) filterDiff(dir, diff string) string {
	var b strings.Builder
	skipping := false
	for _, line := range strings.SplitAfter(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			skipping = false
			for _, p := range diffHeaderPaths(line) {
				if d.Guard.IsDenied(filepath.Join(dir, filepath.FromSlash(p))) {
					skipping = true
					fmt.Fprintf(&b, "(diff of %s not shown: protected by --deny-glob)\n", p)
					break
				}
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

// diffHeaderPaths extracts the a/ and b/ paths of a "diff --git" line.
func diffHeaderPaths(line string) []string {
	rest := strings.TrimSuffix(strings.TrimPrefix(line, "diff --git "), "\n")
	if strings.HasPrefix(rest, `"`) {
		// Quoted names: "a/x" "b/y".
		var out []string
		for _, f := range strings.SplitAfter(rest, `" `) {
			if u, err := strconv.Unquote(strings.TrimSpace(f)); err == nil {
				out = append(out, strings.TrimPrefix(strings.TrimPrefix(u, "a/"), "b/"))
			}
		}
		return out
	}
	a, b, ok := strings.Cut(rest, " b/")
	if !ok {
		return nil
	}
	return []string{strings.TrimPrefix(a, "a/"), b}
}

func (d *Deps) filterNumstat(dir, out string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(out, "\n") {
		fields := strings.SplitN(strings.TrimRight(line, "\n"), "\t", 3)
		if len(fields) == 3 {
			p := fields[2]
			if _, after, ok := strings.Cut(p, " => "); ok {
				p = strings.NewReplacer("{", "", "}", "").Replace(after)
			}
			if d.Guard.IsDenied(filepath.Join(dir, filepath.FromSlash(p))) {
				continue
			}
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
	// Optional locks would let status rewrite the index.
	cmd.Env = append(d.Cmd.Env(), "GIT_OPTIONAL_LOCKS=0")
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
