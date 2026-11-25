package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultReadLines = 2000
	maxLineChars     = 2000
	binarySniffBytes = 8000
	maxReadOutput    = 256 << 10
)

const (
	maxReadPaths      = 20
	maxMultiReadBytes = 512 << 10
)

type readFileInput struct {
	Path      string   `json:"path,omitempty" jsonschema:"file path, absolute or relative to default_dir"`
	Paths     []string `json:"paths,omitempty" jsonschema:"several files to read in one call instead of path (up to 20); start_line and end_line apply to each"`
	StartLine int      `json:"start_line,omitempty" jsonschema:"first line to return, 1-based (default 1)"`
	EndLine   int      `json:"end_line,omitempty" jsonschema:"last line to return, inclusive (default start_line+1999)"`
}

type listDirInput struct {
	Path          string `json:"path,omitempty" jsonschema:"directory, absolute or relative to default_dir (default: default_dir)"`
	Depth         int    `json:"depth,omitempty" jsonschema:"how many levels to descend, 1-10 (default 1)"`
	IncludeHidden bool   `json:"include_hidden,omitempty" jsonschema:"include entries whose name starts with '.'"`
}

func registerRead(s *mcp.Server, d *Deps) {
	addContentTool(s, d, &mcp.Tool{
		Name:  "read_file",
		Title: "Read file",
		Description: "Read a text file with line numbers (cat -n style). Returns at most 2000 lines per file and truncates lines longer than 2000 characters; " +
			"use start_line/end_line to page through larger files. PNG, JPEG, GIF and WebP images are returned as images. " +
			"Pass paths instead of path to read several files at once; a file that cannot be read is reported without failing the others.",
		Annotations: readOnlyAnn,
	}, func(in readFileInput) map[string]any {
		return map[string]any{"path": in.Path, "paths": in.Paths, "start_line": in.StartLine, "end_line": in.EndLine}
	}, d.readFile)

	addTool(s, d, &mcp.Tool{
		Name:        "list_dir",
		Title:       "List directory",
		Description: "List a directory as a tree. Each line is: type (d=dir, f=file, l=symlink), size in bytes, modification time, path relative to the listed directory (directories end with '/'). Capped at 1000 entries.",
		Annotations: readOnlyAnn,
	}, func(in listDirInput) map[string]any {
		return map[string]any{"path": in.Path, "depth": in.Depth}
	}, d.listDir)
}

// checkRegular rejects directories and special files. It must run before a
// file is opened: opening a FIFO with no writer blocks indefinitely.
func checkRegular(abs string, info fs.FileInfo) error {
	switch {
	case info.IsDir():
		return fmt.Errorf("%s is a directory", abs)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", abs)
	}
	return nil
}

// readLimit reads r to the end, failing if it holds more than limit bytes
// (a file can grow after its size was checked).
func readLimit(r io.Reader, abs string, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes (--max-file-bytes)", abs, limit)
	}
	return data, nil
}

func (d *Deps) readFile(ctx context.Context, _ *mcp.CallToolRequest, in readFileInput) ([]mcp.Content, error) {
	switch {
	case len(in.Paths) == 0:
		if in.Path == "" {
			return nil, errors.New("path or paths is required")
		}
		return d.readOne(ctx, in.Path, in.StartLine, in.EndLine, maxReadOutput)
	case in.Path != "":
		return nil, errors.New("give either path or paths, not both")
	case len(in.Paths) > maxReadPaths:
		return nil, fmt.Errorf("at most %d paths can be read at once", maxReadPaths)
	}
	// The files share one output budget, so many large files cannot flood the
	// context; each still gets at least a small share.
	var out []mcp.Content
	budget := maxMultiReadBytes
	for i, p := range in.Paths {
		if budget <= 0 {
			out = append(out, &mcp.TextContent{Text: fmt.Sprintf("skipped %d file(s) (%s, ...): output limit reached; read them in another call",
				len(in.Paths)-i, p)})
			break
		}
		share := max(budget/(len(in.Paths)-i), 4<<10)
		content, err := d.readOne(ctx, p, in.StartLine, in.EndLine, share)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			out = append(out, &mcp.TextContent{Text: fmt.Sprintf("error: %s: %v", p, err)})
			continue
		}
		for _, c := range content {
			switch c := c.(type) {
			case *mcp.TextContent:
				budget -= len(c.Text)
			case *mcp.ImageContent:
				budget -= len(c.Data)
			}
		}
		out = append(out, content...)
	}
	return out, nil
}

// readOne reads one file, returning at most maxOut bytes of text.
func (d *Deps) readOne(ctx context.Context, p string, startLine, endLine, maxOut int) ([]mcp.Content, error) {
	r, err := d.Guard.Resolve(p)
	if err != nil {
		return nil, err
	}
	info, err := r.Root.Stat(r.Rel)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; use list_dir", r.Abs)
	}
	if err := checkRegular(r.Abs, info); err != nil {
		return nil, err
	}
	f, err := r.Root.Open(r.Rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64<<10)
	head, _ := br.Peek(binarySniffBytes)
	if mime := imageType(head); mime != "" {
		if info.Size() > d.Cfg.MaxFileBytes {
			return nil, fmt.Errorf("%s is a %d byte image; the limit is %d (--max-file-bytes)", r.Abs, info.Size(), d.Cfg.MaxFileBytes)
		}
		data, err := readLimit(br, r.Abs, d.Cfg.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		return []mcp.Content{
			&mcp.ImageContent{Data: data, MIMEType: mime},
			&mcp.TextContent{Text: fmt.Sprintf("[%s | %s, %d bytes]", r.Abs, mime, len(data))},
		}, nil
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, fmt.Errorf("%s looks like a binary file (%d bytes)", r.Abs, info.Size())
	}

	start := max(startLine, 1)
	end := endLine
	if end <= 0 {
		end = start + defaultReadLines - 1
	}
	if end < start {
		return nil, fmt.Errorf("end_line (%d) is before start_line (%d)", end, start)
	}
	if end-start+1 > defaultReadLines {
		end = start + defaultReadLines - 1
	}

	// The total line count is reported for files up to --max-file-bytes; for
	// larger ones (logs) reading stops after the requested range.
	countAll := info.Size() <= d.Cfg.MaxFileBytes
	var out strings.Builder
	lineNo, lastShown := 0, 0
	outputFull, eof := false, false
	for {
		if lineNo%4096 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		line, err := readLine(br, maxLineChars)
		if err == io.EOF && line == nil {
			eof = true
			break
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		lineNo++
		if lineNo > end && !countAll {
			break // this line shows that more follow
		}
		if lineNo >= start && lineNo <= end && !outputFull {
			s := strings.ToValidUTF8(string(line), "�")
			if out.Len()+len(s) > maxOut {
				outputFull = true
			} else {
				fmt.Fprintf(&out, "%6d\t%s\n", lineNo, s)
				lastShown = lineNo
			}
		}
		if err == io.EOF {
			eof = true
			break
		}
	}
	shown := fmt.Sprintf("lines %d-%d of %d", start, lastShown, lineNo)
	if !eof {
		shown = fmt.Sprintf("lines %d-%d (total not counted: file is larger than %d bytes)", start, lastShown, d.Cfg.MaxFileBytes)
	}
	switch {
	case lineNo == 0:
		out.WriteString("(empty file)\n")
		shown = "0 lines"
	case lastShown == 0:
		fmt.Fprintf(&out, "(no lines in range; file has %d lines)\n", lineNo)
		shown = fmt.Sprintf("%d lines", lineNo)
	}
	fmt.Fprintf(&out, "[%s | %s | modified %s]", r.Abs, shown, info.ModTime().Format(time.RFC3339))
	if lastShown > 0 && lastShown < lineNo {
		fmt.Fprintf(&out, "\n(more lines follow; continue with start_line=%d)", lastShown+1)
	}
	return []mcp.Content{&mcp.TextContent{Text: out.String()}}, nil
}

func imageType(head []byte) string {
	switch mime := http.DetectContentType(head); mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mime
	}
	return ""
}

// The rest of an over-long line is skipped without buffering it, so a huge
// single-line file cannot exhaust memory.
func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	truncated := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !truncated {
			room := limit - len(line)
			if len(chunk) > room {
				line = append(line, chunk[:room]...)
				truncated = true
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(line) == 0 && !truncated {
			return nil, io.EOF
		}
		line = bytes.TrimRight(line, "\r\n")
		if truncated {
			line = append(line, " …[line truncated]"...)
		}
		return line, err
	}
}

func (d *Deps) listDir(_ context.Context, _ *mcp.CallToolRequest, in listDirInput) (string, error) {
	p := in.Path
	if p == "" {
		p = "."
	}
	r, err := d.Guard.Resolve(p)
	if err != nil {
		return "", err
	}
	info, err := r.Root.Stat(r.Rel)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", r.Abs)
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 1
	}
	depth = min(depth, 10)

	const maxEntries = 1000
	fsys := r.Root.FS()
	base := filepath.ToSlash(r.Rel)
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n", r.Abs)
	count := 0
	truncated := false

	var walk func(dir string, level int) error
	walk = func(dir string, level int) error {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if count >= maxEntries {
				truncated = true
				return nil
			}
			name := e.Name()
			if !in.IncludeHidden && strings.HasPrefix(name, ".") {
				continue
			}
			child := path.Join(dir, name)
			abs := filepath.Join(r.RootDir, filepath.FromSlash(child))
			if d.isDeniedEntry(abs, e) {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			rel := strings.TrimPrefix(strings.TrimPrefix(child, base), "/")
			if base == "." {
				rel = child
			}
			kind, size := "f", fmt.Sprint(fi.Size())
			switch {
			case e.Type()&fs.ModeSymlink != 0:
				kind = "l"
			case e.IsDir():
				kind, size, rel = "d", "-", rel+"/"
			}
			fmt.Fprintf(&out, "%s %10s %s %s\n", kind, size, fi.ModTime().Format("2006-01-02 15:04"), rel)
			count++
			if e.IsDir() && level < depth {
				if err := walk(child, level+1); err != nil {
					fmt.Fprintf(&out, "  (cannot read %s: %v)\n", rel, err)
				}
			}
		}
		return nil
	}
	if err := walk(base, 1); err != nil {
		return "", err
	}
	if count == 0 {
		out.WriteString("(empty)\n")
	}
	if truncated {
		fmt.Fprintf(&out, "(truncated at %d entries; list a subdirectory or lower depth)\n", maxEntries)
	}
	return out.String(), nil
}

func (d *Deps) isDeniedEntry(abs string, e fs.DirEntry) bool {
	if e.Type()&fs.ModeSymlink != 0 {
		return d.Guard.IsDenied(abs)
	}
	return d.Guard.DeniedName(abs)
}
