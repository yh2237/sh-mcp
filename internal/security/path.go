package security

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var (
	ErrOutsideRoots = errors.New("path is outside the allowed directories (--allow-dir)")
	ErrDenied       = errors.New("path is protected by --deny-glob")
)

// Guard confines file access to a set of root directories. All file
// operations must go through the *os.Root returned by Resolve, which rejects
// ".." and symlink escapes atomically at open time (no check-then-use race).
type Guard struct {
	roots  []rootEntry // sorted longest dir first so nested roots win
	dirs   []string    // allowed dirs in configured order (dirs[0] is def)
	deny   []string
	except []string
	def    string
}

type rootEntry struct {
	dir  string
	root *os.Root
}

type Resolved struct {
	Abs     string   // cleaned absolute path as requested (not symlink-resolved)
	RootDir string   // the --allow-dir entry that contains Abs
	Root    *os.Root // operations must use Root with Rel
	Rel     string   // path relative to RootDir; "." for the root itself
	close   func()
}

// Release closes the root opened by ResolveOutside; it does nothing for paths
// inside the allowed directories.
func (r *Resolved) Release() {
	if r != nil && r.close != nil {
		r.close()
	}
}

// NewGuard confines access to dirs. A deny glob starting with "!" exempts
// matching paths from the other deny globs.
func NewGuard(dirs, denyGlobs []string) (*Guard, error) {
	if len(dirs) == 0 {
		return nil, errors.New("no allowed directories")
	}
	g := &Guard{def: filepath.Clean(dirs[0])}
	for _, pat := range denyGlobs {
		if ex, ok := strings.CutPrefix(pat, "!"); ok {
			g.except = append(g.except, ex)
		} else {
			g.deny = append(g.deny, pat)
		}
	}
	for _, d := range dirs {
		d = filepath.Clean(d)
		r, err := os.OpenRoot(d)
		if err != nil {
			g.Close()
			return nil, fmt.Errorf("open allowed dir %s: %w", d, err)
		}
		g.dirs = append(g.dirs, d)
		g.roots = append(g.roots, rootEntry{dir: d, root: r})
	}
	sort.SliceStable(g.roots, func(i, j int) bool { return len(g.roots[i].dir) > len(g.roots[j].dir) })
	return g, nil
}

func (g *Guard) Close() {
	for _, r := range g.roots {
		r.root.Close()
	}
}

func (g *Guard) DefaultDir() string { return g.def }

func (g *Guard) Dirs() []string {
	return append([]string(nil), g.dirs...)
}

// Abs returns the cleaned absolute form of p (relative paths are relative to
// the default directory).
func (g *Guard) Abs(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is empty")
	}
	if err := checkPlatformPath(p); err != nil {
		return "", err
	}
	abs := p
	if !filepath.IsAbs(abs) {
		if filepath.VolumeName(abs) != "" {
			return "", fmt.Errorf("%s: drive-relative paths are not supported", p)
		}
		abs = filepath.Join(g.def, abs)
	}
	return filepath.Clean(abs), nil
}

func (g *Guard) Resolve(p string) (*Resolved, error) {
	abs, err := g.Abs(p)
	if err != nil {
		return nil, err
	}
	for _, r := range g.roots {
		rel, err := filepath.Rel(r.dir, abs)
		if err != nil || !(rel == "." || filepath.IsLocal(rel)) {
			continue
		}
		if g.isDenied(abs) {
			return nil, fmt.Errorf("%s: %w", abs, ErrDenied)
		}
		return &Resolved{Abs: abs, RootDir: r.dir, Root: r.root, Rel: rel}, nil
	}
	return nil, fmt.Errorf("%s: %w %v", abs, ErrOutsideRoots, g.Dirs())
}

// ResolveOutside is Resolve for a path the user allowed reading although it
// is outside the allowed directories. It opens a root at the directory (or
// at a file's parent), so symlinks cannot lead out of what was approved, and
// the deny globs still apply. Call Release when done.
func (g *Guard) ResolveOutside(p string) (*Resolved, error) {
	r, err := g.Resolve(p)
	if !errors.Is(err, ErrOutsideRoots) {
		return r, err
	}
	abs, err := g.Abs(p)
	if err != nil {
		return nil, err
	}
	if g.isDenied(abs) {
		return nil, fmt.Errorf("%s: %w", abs, ErrDenied)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	dir, rel := abs, "."
	if !info.IsDir() {
		dir, rel = filepath.Dir(abs), filepath.Base(abs)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Resolved{Abs: abs, RootDir: dir, Root: root, Rel: rel, close: func() { root.Close() }}, nil
}

func (g *Guard) IsDenied(abs string) bool { return g.isDenied(abs) }

// DeniedName matches abs against the deny globs without resolving symlinks.
// Directory walkers use it for regular entries (whose name is their real
// name) and fall back to IsDenied for symlinks, avoiding a syscall per file.
func (g *Guard) DeniedName(abs string) bool {
	n := filepath.ToSlash(abs)
	return matchAny(g.deny, n) && !matchAny(g.except, n)
}

func matchAny(patterns []string, name string) bool {
	for _, pat := range patterns {
		if MatchGlob(pat, name) {
			return true
		}
	}
	return false
}

func (g *Guard) isDenied(abs string) bool {
	if len(g.deny) == 0 {
		return false
	}
	if g.DeniedName(abs) {
		return true
	}
	if real, err := realPath(abs); err == nil && real != abs {
		return g.DeniedName(real)
	}
	return false
}

// realPath resolves symlinks (and on Windows, 8.3 short names) in abs. For
// paths that do not exist yet, the deepest existing ancestor is resolved and
// the remainder appended.
func realPath(abs string) (string, error) {
	rest := ""
	cur := abs
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return real, nil
			}
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// checkPlatformPath rejects path syntax that can alias other files on
// Windows: UNC / device paths and NTFS alternate data streams.
func checkPlatformPath(p string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `//`) {
		return fmt.Errorf("%s: UNC and device paths are not supported", p)
	}
	vol := filepath.VolumeName(p)
	if strings.Contains(p[len(vol):], ":") {
		return fmt.Errorf("%s: ':' is not allowed in paths (alternate data streams)", p)
	}
	return nil
}
