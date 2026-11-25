package security

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"**/.env", "/home/u/proj/.env", true},
		{"**/.env", "C:/Users/u/.env", true},
		{"**/.env", "/home/u/proj/.env.local", false},
		{"**/.env.*", "/home/u/proj/.env.local", true},
		{"**/*.pem", "/a/b/c/server.pem", true},
		{"**/*id_rsa*", "/home/u/.ssh/id_rsa.pub", true},
		{"src/**/*.go", "src/a/b/c.go", true},
		{"src/**/*.go", "src/c.go", true},
		{"src/**/*.go", "lib/c.go", false},
		{"*.go", "a.go", true},
		{"*.go", "dir/a.go", false},
		{"node_modules", "node_modules", true},
		{"**", "anything/at/all", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.name); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pat, c.name, got, c.want)
		}
	}
}

func newGuard(t *testing.T) (*Guard, string) {
	t.Helper()
	dir := t.TempDir()
	// Resolve the temp dir itself (macOS /var -> /private/var, Windows 8.3 names).
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := NewGuard([]string{root}, []string{"**/.env", "**/*.pem"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g, root
}

func TestGuardResolve(t *testing.T) {
	g, root := newGuard(t)

	r, err := g.Resolve("sub/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if r.Abs != filepath.Join(root, "sub", "file.txt") || r.Rel != filepath.Join("sub", "file.txt") {
		t.Errorf("got Abs=%s Rel=%s", r.Abs, r.Rel)
	}
	if r, err := g.Resolve(root); err != nil || r.Rel != "." {
		t.Errorf("root itself: %v %v", r, err)
	}

	for _, p := range []string{"../outside.txt", "sub/../../outside.txt", filepath.Dir(root)} {
		if _, err := g.Resolve(p); !errors.Is(err, ErrOutsideRoots) {
			t.Errorf("Resolve(%q) err = %v, want ErrOutsideRoots", p, err)
		}
	}
	for _, p := range []string{".env", "sub/.env", "sub/key.pem"} {
		if _, err := g.Resolve(p); !errors.Is(err, ErrDenied) {
			t.Errorf("Resolve(%q) err = %v, want ErrDenied", p, err)
		}
	}
	if runtime.GOOS == "windows" {
		for _, p := range []string{`sub\file.txt:secret`, `\\server\share\x`, `\\?\C:\x`} {
			if _, err := g.Resolve(p); err == nil {
				t.Errorf("Resolve(%q) succeeded, want error", p)
			}
		}
	}
}

func TestGuardDenyExceptions(t *testing.T) {
	root := t.TempDir()
	g, err := NewGuard([]string{root}, []string{"**/.env", "**/.env.*", "!**/.env.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for name, want := range map[string]bool{".env": true, ".env.local": true, ".env.example": false, "sub/.env.example": false} {
		if got := g.IsDenied(filepath.Join(root, name)); got != want {
			t.Errorf("IsDenied(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestEnvPassthroughPrefix(t *testing.T) {
	t.Setenv("SHMCPTEST_A", "1")
	t.Setenv("SHMCPTEST_B", "2")
	t.Setenv("OTHER_SHMCPTEST", "3")
	t.Setenv("GOPROXY", "direct")
	t.Setenv("SH_MCP_AUTH_TOKEN", "secret")
	p, _ := NewCommandPolicy(nil, []string{"SHMCPTEST_*", "SH_MCP_*"}, []string{"SH_MCP_AUTH_TOKEN"})
	got := map[string]bool{}
	for _, kv := range p.Env() {
		k, _, _ := strings.Cut(kv, "=")
		got[strings.ToUpper(k)] = true
	}
	for k, want := range map[string]bool{"SHMCPTEST_A": true, "SHMCPTEST_B": true, "GOPROXY": true, "OTHER_SHMCPTEST": false, "SH_MCP_AUTH_TOKEN": false} {
		if got[k] != want {
			t.Errorf("%s passed = %v, want %v", k, got[k], want)
		}
	}
}

func TestGuardCaseInsensitiveDeny(t *testing.T) {
	if !caseInsensitiveFS {
		t.Skip("case-sensitive filesystem")
	}
	g, _ := newGuard(t)
	if _, err := g.Resolve("sub/KEY.PEM"); !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestGuardSymlinks(t *testing.T) {
	g, root := newGuard(t)
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, "innocent.txt")); err != nil {
		t.Fatal(err)
	}

	// The path is lexically inside the root; os.Root must refuse to follow
	// the link out of it.
	r, err := g.Resolve("escape/secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Root.ReadFile(r.Rel); err == nil {
		t.Error("read through symlink escaped the root")
	}

	// A link to a denied file is denied by its target name.
	if _, err := g.Resolve("innocent.txt"); !errors.Is(err, ErrDenied) {
		t.Errorf("symlink to .env: err = %v, want ErrDenied", err)
	}
}

func TestCommandPolicy(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, missing := NewCommandPolicy([]string{"go", "definitely-not-a-command-xyz"}, nil, nil)
	if len(missing) != 1 {
		t.Errorf("missing = %v", missing)
	}
	goPath, err := p.Resolve("go", ".")
	if err != nil {
		t.Fatalf("resolve go: %v", err)
	}
	if !p.PreApproved(goPath) {
		t.Errorf("go should be pre-approved")
	}
	exePath, err := p.Resolve(exe, ".")
	if err != nil {
		t.Fatalf("resolve %s: %v", exe, err)
	}
	if p.PreApproved(exePath) {
		t.Errorf("unlisted command should not be pre-approved")
	}
	if _, err := p.Resolve("definitely-not-a-command-xyz", "."); err == nil {
		t.Errorf("missing command resolved")
	}
}

func TestEnvExcludesSecrets(t *testing.T) {
	t.Setenv("SH_MCP_AUTH_TOKEN", "secret")
	t.Setenv("PATH", os.Getenv("PATH"))
	p, _ := NewCommandPolicy(nil, []string{"SH_MCP_AUTH_TOKEN"}, []string{"SH_MCP_AUTH_TOKEN"})
	for _, kv := range p.Env() {
		if len(kv) > 17 && kv[:18] == "SH_MCP_AUTH_TOKEN=" {
			t.Fatal("secret leaked into child environment")
		}
	}
}

func TestEnvPythonUTF8(t *testing.T) {
	t.Setenv("PYTHONUTF8", "") // restored after the test
	os.Unsetenv("PYTHONUTF8")
	p, _ := NewCommandPolicy(nil, nil, nil)
	if got := slices.Contains(p.Env(), "PYTHONUTF8=1"); got != (runtime.GOOS == "windows") {
		t.Errorf("PYTHONUTF8=1 set: %v on %s", got, runtime.GOOS)
	}
	// The owner's own setting wins.
	t.Setenv("PYTHONUTF8", "0")
	p, _ = NewCommandPolicy(nil, nil, nil)
	if env := p.Env(); !slices.Contains(env, "PYTHONUTF8=0") || slices.Contains(env, "PYTHONUTF8=1") {
		t.Errorf("owner's PYTHONUTF8 not kept: %v", env)
	}
}

func TestGuardDirsPreserveOrder(t *testing.T) {
	base := t.TempDir()
	zdir := filepath.Join(base, "z-dir")
	adir := filepath.Join(base, "a-dir")
	for _, d := range []string{zdir, adir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	g, err := NewGuard([]string{zdir, adir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	want := []string{zdir, adir}
	if got := g.Dirs(); !slices.Equal(got, want) {
		t.Errorf("Dirs() = %v, want %v", got, want)
	}
	if g.DefaultDir() != zdir {
		t.Errorf("DefaultDir() = %q, want %q", g.DefaultDir(), zdir)
	}
}

func TestGuardReadDirs(t *testing.T) {
	base := t.TempDir()
	rw := filepath.Join(base, "rw")
	ro := filepath.Join(base, "ro")
	nestedRW := filepath.Join(ro, "writable")
	for _, d := range []string{rw, ro, nestedRW} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	g, err := NewGuardWithReadDirs([]string{rw, nestedRW}, []string{ro}, []string{"**/.env"})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if got := g.ReadDirs(); !slices.Equal(got, []string{ro}) {
		t.Errorf("ReadDirs() = %v, want [%s]", got, ro)
	}
	if _, err := g.ResolveRead(filepath.Join(ro, "note.txt")); err != nil {
		t.Errorf("ResolveRead in ro dir failed: %v", err)
	}
	if _, err := g.Resolve(filepath.Join(ro, "note.txt")); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Resolve in ro dir: err = %v, want ErrReadOnly", err)
	}
	if _, err := g.ResolveRead(filepath.Join(ro, ".env")); !errors.Is(err, ErrDenied) {
		t.Errorf("ResolveRead denied file in ro dir: err = %v, want ErrDenied", err)
	}
	if _, err := g.Resolve(filepath.Join(nestedRW, "ok.txt")); err != nil {
		t.Errorf("Resolve in nested rw dir inside ro dir failed: %v", err)
	}
}
