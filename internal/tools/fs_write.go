package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/security"
)

const utf8BOM = "\xEF\xBB\xBF"

type writeFileInput struct {
	Path       string `json:"path" jsonschema:"file path, absolute or relative to default_dir"`
	Content    string `json:"content" jsonschema:"the complete new file content"`
	CreateDirs bool   `json:"create_dirs,omitempty" jsonschema:"create missing parent directories"`
}

type edit struct {
	OldText    string `json:"old_text" jsonschema:"exact text to replace, including indentation; must match exactly once unless replace_all is set"`
	NewText    string `json:"new_text" jsonschema:"replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace every occurrence instead of requiring a unique match"`
}

type editFileInput struct {
	Path  string `json:"path" jsonschema:"file path, absolute or relative to default_dir"`
	Edits []edit `json:"edits" jsonschema:"replacements applied in order, each to the result of the previous one; if any fails nothing is written"`
}

type deleteFileInput struct {
	Path      string `json:"path" jsonschema:"file or directory, absolute or relative to default_dir"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"delete a non-empty directory with everything in it"`
}

type moveFileInput struct {
	Source      string `json:"source" jsonschema:"file or directory to move or rename"`
	Destination string `json:"destination" jsonschema:"new path; its parent directory must exist"`
	Overwrite   bool   `json:"overwrite,omitempty" jsonschema:"replace an existing destination file"`
}

func registerWrite(s *mcp.Server, d *Deps) {
	addTool(s, d, &mcp.Tool{
		Name:  "write_file",
		Title: "Write file",
		Description: "Create a file or replace its entire content. The write is atomic (temporary file + rename) and keeps the existing file's permissions. " +
			"Prefer edit_file for changes to existing files.",
		Annotations: writeAnn,
	}, func(in writeFileInput) map[string]any {
		return map[string]any{"path": in.Path, "bytes": len(in.Content), "sha256": shortHash([]byte(in.Content))}
	}, d.writeFile)

	addTool(s, d, &mcp.Tool{
		Name:  "edit_file",
		Title: "Edit file",
		Description: "Apply one or more exact text replacements to a file and return a diff. Each old_text must match exactly once (unless replace_all); " +
			"if any edit fails, the file is left unchanged. Line endings are matched to the file's style (LF or CRLF) and a UTF-8 BOM is preserved.",
		Annotations: writeAnn,
	}, func(in editFileInput) map[string]any {
		return map[string]any{"path": in.Path, "edits": len(in.Edits)}
	}, d.editFile)

	addTool(s, d, &mcp.Tool{
		Name:        "delete_file",
		Title:       "Delete file",
		Description: "Delete a file, a symlink or an empty directory; set recursive to delete a directory with its contents. Allowed root directories cannot be deleted.",
		Annotations: writeAnn,
	}, func(in deleteFileInput) map[string]any {
		return map[string]any{"path": in.Path, "recursive": in.Recursive}
	}, d.deleteFile)

	addTool(s, d, &mcp.Tool{
		Name:        "move_file",
		Title:       "Move file",
		Description: "Move or rename a file or directory within the same allowed directory.",
		Annotations: writeAnn,
	}, func(in moveFileInput) map[string]any {
		return map[string]any{"source": in.Source, "destination": in.Destination, "overwrite": in.Overwrite}
	}, d.moveFile)
}

func (d *Deps) writeFile(_ context.Context, req *mcp.CallToolRequest, in writeFileInput) (string, error) {
	r, err := d.Guard.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if r.Rel == "." {
		return "", fmt.Errorf("%s is an allowed root directory", r.Abs)
	}
	if int64(len(in.Content)) > d.Cfg.MaxFileBytes {
		return "", fmt.Errorf("content is %d bytes; limit is %d (--max-file-bytes)", len(in.Content), d.Cfg.MaxFileBytes)
	}
	verb := "Create"
	if _, err := r.Root.Lstat(r.Rel); err == nil {
		verb = "Overwrite"
	}
	msg := fmt.Sprintf("%s %s? (%d bytes, %d lines)\n---\n%s", verb, r.Abs, len(in.Content), countLines(in.Content), in.Content)
	if err := d.approve(req, msg); err != nil {
		return "", err
	}
	if in.CreateDirs {
		if err := r.Root.MkdirAll(filepath.Dir(r.Rel), 0o755); err != nil {
			return "", err
		}
	}
	created, err := atomicWrite(r.Root, r.Rel, []byte(in.Content))
	if err != nil {
		return "", err
	}
	done := "overwrote"
	if created {
		done = "created"
	}
	return fmt.Sprintf("%s %s (%d bytes, %d lines)", done, r.Abs, len(in.Content), countLines(in.Content)), nil
}

func (d *Deps) editFile(_ context.Context, req *mcp.CallToolRequest, in editFileInput) (string, error) {
	if len(in.Edits) == 0 {
		return "", errors.New("edits is empty")
	}
	r, err := d.Guard.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	info, err := r.Root.Stat(r.Rel)
	if err != nil {
		return "", err
	}
	if err := checkRegular(r.Abs, info); err != nil {
		return "", err
	}
	if info.Size() > d.Cfg.MaxFileBytes {
		return "", fmt.Errorf("%s is %d bytes; limit is %d (--max-file-bytes)", r.Abs, info.Size(), d.Cfg.MaxFileBytes)
	}
	f, err := r.Root.Open(r.Rel)
	if err != nil {
		return "", err
	}
	raw, err := readLimit(f, r.Abs, d.Cfg.MaxFileBytes)
	f.Close()
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(raw[:min(len(raw), binarySniffBytes)], 0) >= 0 {
		return "", fmt.Errorf("%s looks like a binary file", r.Abs)
	}

	content := string(raw)
	bom := strings.HasPrefix(content, utf8BOM)
	content = strings.TrimPrefix(content, utf8BOM)
	crlf := isCRLF(content)

	var diff strings.Builder
	total := 0
	for i, e := range in.Edits {
		where := ""
		if len(in.Edits) > 1 {
			where = fmt.Sprintf("edits[%d]: ", i)
		}
		if e.OldText == "" {
			return "", fmt.Errorf("%sold_text is empty; use write_file to create or replace a whole file", where)
		}
		if e.OldText == e.NewText {
			return "", fmt.Errorf("%sold_text and new_text are identical", where)
		}
		oldText, newText := e.OldText, e.NewText
		if crlf {
			oldText, newText = toCRLF(oldText), toCRLF(newText)
		}
		n := strings.Count(content, oldText)
		switch {
		case n == 0:
			return "", fmt.Errorf("%sold_text not found in %s; nothing was changed. Re-read the file and copy the text exactly, including indentation and whitespace", where, r.Abs)
		case n > 1 && !e.ReplaceAll:
			return "", fmt.Errorf("%sold_text matches %d places in %s; nothing was changed. Include more surrounding lines to make it unique, or set replace_all", where, n, r.Abs)
		}
		diff.WriteString(replacementDiff(content, oldText, newText, n))
		content = strings.ReplaceAll(content, oldText, newText)
		total += n
	}
	if bom {
		content = utf8BOM + content
	}
	if int64(len(content)) > d.Cfg.MaxFileBytes {
		return "", fmt.Errorf("result would be %d bytes; limit is %d (--max-file-bytes)", len(content), d.Cfg.MaxFileBytes)
	}
	if err := d.approve(req, fmt.Sprintf("Edit %s? (%d replacement(s))\n%s", r.Abs, total, diff.String())); err != nil {
		return "", err
	}
	if _, err := atomicWrite(r.Root, r.Rel, []byte(content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s: %d replacement(s)\n%s", r.Abs, total, diff.String()), nil
}

func (d *Deps) deleteFile(_ context.Context, req *mcp.CallToolRequest, in deleteFileInput) (string, error) {
	r, err := d.Guard.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if r.Rel == "." {
		return "", fmt.Errorf("%s is an allowed root directory and cannot be deleted", r.Abs)
	}
	fi, err := r.Root.Lstat(r.Rel)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		if err := d.approve(req, "Delete "+r.Abs+"?"); err != nil {
			return "", err
		}
		if err := r.Root.Remove(r.Rel); err != nil {
			return "", err
		}
		return "deleted " + r.Abs, nil
	}
	if !in.Recursive {
		if err := d.approve(req, "Delete the empty directory "+r.Abs+"?"); err != nil {
			return "", err
		}
		if err := r.Root.Remove(r.Rel); err != nil {
			return "", fmt.Errorf("%s is a directory that is not empty (set recursive to delete its contents): %w", r.Abs, err)
		}
		return "deleted empty directory " + r.Abs, nil
	}
	// A recursive delete must not remove files the file tools may not touch.
	count, err := d.checkTreeUnprotected(r, "delete them yourself or leave the directory")
	if err != nil {
		return "", err
	}
	if err := d.approve(req, fmt.Sprintf("Delete the directory %s and everything in it (%d entries)?", r.Abs, count)); err != nil {
		return "", err
	}
	if err := r.Root.RemoveAll(r.Rel); err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted directory %s (%d entries)", r.Abs, count), nil
}

func (d *Deps) moveFile(_ context.Context, req *mcp.CallToolRequest, in moveFileInput) (string, error) {
	src, err := d.Guard.Resolve(in.Source)
	if err != nil {
		return "", err
	}
	dst, err := d.Guard.Resolve(in.Destination)
	if err != nil {
		return "", err
	}
	if src.Rel == "." || dst.Rel == "." {
		return "", errors.New("allowed root directories cannot be moved or replaced")
	}
	if src.RootDir != dst.RootDir {
		return "", fmt.Errorf("cannot move between allowed directories (%s -> %s); copy with write_file instead", src.RootDir, dst.RootDir)
	}
	sfi, err := src.Root.Lstat(src.Rel)
	if err != nil {
		return "", err
	}
	if sfi.IsDir() {
		// Moving a directory would carry protected files to a path that a
		// location-specific --deny-glob no longer covers.
		if _, err := d.checkTreeUnprotected(src, "move them yourself or leave the directory"); err != nil {
			return "", err
		}
	}
	if fi, err := dst.Root.Lstat(dst.Rel); err == nil {
		if !in.Overwrite {
			return "", fmt.Errorf("%s already exists (set overwrite to replace it)", dst.Abs)
		}
		if fi.IsDir() {
			return "", fmt.Errorf("%s is an existing directory and cannot be overwritten", dst.Abs)
		}
	}
	if strings.HasPrefix(filepath.ToSlash(dst.Rel)+"/", filepath.ToSlash(src.Rel)+"/") {
		return "", errors.New("cannot move a directory into itself")
	}
	if err := d.approve(req, fmt.Sprintf("Move %s to %s?", src.Abs, dst.Abs)); err != nil {
		return "", err
	}
	if err := src.Root.Rename(src.Rel, dst.Rel); err != nil {
		return "", err
	}
	return fmt.Sprintf("moved %s -> %s", src.Abs, dst.Abs), nil
}

// checkTreeUnprotected fails if anything under the directory r is protected
// by --deny-glob, and otherwise returns how many entries the tree has.
func (d *Deps) checkTreeUnprotected(r *security.Resolved, hint string) (int, error) {
	count := 0
	var denied []string
	err := fs.WalkDir(r.Root.FS(), filepath.ToSlash(r.Rel), func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if d.isDeniedEntry(filepath.Join(r.RootDir, filepath.FromSlash(p)), e) {
			denied = append(denied, p)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(denied) > 0 {
		return 0, fmt.Errorf("%s contains files protected by --deny-glob (%s); %s", r.Abs, strings.Join(denied[:min(len(denied), 5)], ", "), hint)
	}
	return count, nil
}

// atomicWrite replaces rel with data via a temporary file in the same
// directory and a rename, preserving the permission bits of an existing
// file. If rel is a symlink, the link target is written in place instead so
// the link survives. It reports whether the file was newly created.
func atomicWrite(root *os.Root, rel string, data []byte) (created bool, err error) {
	mode := fs.FileMode(0o644)
	fi, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		created = true
	case err != nil:
		return false, err
	case fi.IsDir():
		return false, fmt.Errorf("%s is a directory", rel)
	case fi.Mode()&fs.ModeSymlink != 0:
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return false, err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return false, err
		}
		return false, f.Close()
	default:
		mode = fi.Mode().Perm()
	}

	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".sh-mcp-"+hex.EncodeToString(rnd[:])+".tmp")
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return false, err
	}
	cleanup := func() { _ = root.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return false, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return false, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return false, err
	}
	// OpenFile's mode is filtered by the umask; restore the original bits.
	_ = root.Chmod(tmp, mode)
	if err := root.Rename(tmp, rel); err != nil {
		cleanup()
		return false, err
	}
	return created, nil
}

func isCRLF(s string) bool {
	crlf := strings.Count(s, "\r\n")
	return crlf > 0 && crlf >= strings.Count(s, "\n")-crlf
}

func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func replacementDiff(content, old, new string, n int) string {
	const ctx, maxHunks = 3, 20
	var b strings.Builder
	shift := 0 // line-number offset of the new file after earlier hunks
	pos := 0
	for k := 0; k < n; k++ {
		idx := strings.Index(content[pos:], old)
		if idx < 0 {
			break
		}
		idx += pos
		end := idx + len(old)
		pos = end
		if k >= maxHunks {
			fmt.Fprintf(&b, "… %d more replacement(s) not shown\n", n-maxHunks)
			break
		}
		// Compare everything from the start of the first touched line to the
		// end of the file, then drop the identical trailing lines.
		lineStart := strings.LastIndex(content[:idx], "\n") + 1
		oldLocal := splitLines(content[lineStart:])
		newLocal := splitLines(content[lineStart:idx] + new + content[end:])
		i, j := len(oldLocal), len(newLocal)
		for i > 0 && j > 0 && oldLocal[i-1] == newLocal[j-1] {
			i--
			j--
		}
		before := splitLines(content[:lineStart])
		pre := before[max(0, len(before)-ctx):]
		post := oldLocal[i:min(i+ctx, len(oldLocal))]
		startOld := len(before) - len(pre) + 1
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", startOld, len(pre)+i+len(post), startOld+shift, len(pre)+j+len(post))
		for _, l := range pre {
			fmt.Fprintf(&b, " %s\n", l)
		}
		for _, l := range oldLocal[:i] {
			fmt.Fprintf(&b, "-%s\n", l)
		}
		for _, l := range newLocal[:j] {
			fmt.Fprintf(&b, "+%s\n", l)
		}
		for _, l := range post {
			fmt.Fprintf(&b, " %s\n", l)
		}
		shift += j - i
	}
	return b.String()
}

// A trailing newline does not produce an empty final line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}
