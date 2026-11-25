package config

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, args []string, env map[string]string) (*Config, error) {
	t.Helper()
	return Parse(args, func(k string) string { return env[k] }, io.Discard)
}

const token = "0123456789abcdef"

func TestDefaults(t *testing.T) {
	c, err := parse(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport != TransportStdio || c.Mode != ModeRead || len(c.AllowedDirs) != 1 || c.CanWrite() || c.CanExec() {
		t.Errorf("defaults should be read-only stdio in the working directory: %+v", c)
	}
}

func TestTransportFollowsListenAndPublicURL(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"SH_MCP_AUTH_TOKEN": token}
	c, err := parse(t, []string{"--allow-dir", dir, "--public-url", "https://mcp.example.com/"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport != TransportHTTP || c.Listen != DefaultListen || c.PublicURL.String() != "https://mcp.example.com" {
		t.Errorf("--public-url alone: %s %s %v", c.Transport, c.Listen, c.PublicURL)
	}
	c, err = parse(t, []string{"--allow-dir", dir, "--listen", "0.0.0.0:9000"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport != TransportHTTP || c.Listen != "0.0.0.0:9000" || c.PublicURL != nil {
		t.Errorf("--listen alone: %s %s %v", c.Transport, c.Listen, c.PublicURL)
	}
}

func TestFlagsOverrideEnv(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"SH_MCP_LISTEN":     "127.0.0.1:1111",
		"SH_MCP_ALLOW_DIR":  "/from/env/a;/from/env/b",
		"SH_MCP_MODE":       "read",
		"SH_MCP_SHELL":      "/bin/bash -c",
		"SH_MCP_AUTH_TOKEN": token,
	}
	c, err := parse(t, []string{"--listen", "127.0.0.1:2222", "--allow-dir", dir, "--mode", "full"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport != TransportHTTP || c.Listen != "127.0.0.1:2222" || c.Mode != ModeFull {
		t.Errorf("transport/listen/mode: %s %s %s", c.Transport, c.Listen, c.Mode)
	}
	if want := []string{dir}; !reflect.DeepEqual(c.AllowedDirs, want) {
		t.Errorf("allow-dir flag should replace env: %v", c.AllowedDirs)
	}
	if !reflect.DeepEqual(c.Shell, []string{"/bin/bash", "-c"}) {
		t.Errorf("shell: %v", c.Shell)
	}
}

func TestListsFromEnv(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	c, err := parse(t, []string{"--deny-glob", "**/secrets/**"}, map[string]string{
		"SH_MCP_MODE":                 "ask",
		"SH_MCP_ALLOW_DIR":            a + ";" + b,
		"SH_MCP_AUTO_APPROVE_COMMAND": "df; free ;uptime",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.AllowedDirs, []string{filepath.Clean(a), filepath.Clean(b)}) {
		t.Errorf("dirs: %v", c.AllowedDirs)
	}
	if !reflect.DeepEqual(c.AutoApproveCommands, []string{"df", "free", "uptime"}) {
		t.Errorf("commands: %v", c.AutoApproveCommands)
	}
	deny := strings.Join(c.DenyGlobs, " ")
	if !strings.Contains(deny, "**/.env") || !strings.Contains(deny, "**/secrets/**") {
		t.Errorf("deny-glob should extend the defaults: %v", c.DenyGlobs)
	}
}

func TestMCPServers(t *testing.T) {
	c, err := parse(t, []string{"--mcp-server", `files=npx -y "@scope/server files" C:\data`},
		map[string]string{"SH_MCP_MCP_SERVER": "a=one --x=1,2;b=two"})
	if err != nil {
		t.Fatal(err)
	}
	want := []MCPServer{{Name: "files", Argv: []string{"npx", "-y", "@scope/server files", `C:\data`}}}
	if !reflect.DeepEqual(c.MCPServers, want) {
		t.Errorf("flag should replace env and keep quotes/backslashes: %#v", c.MCPServers)
	}
	c, err = parse(t, nil, map[string]string{"SH_MCP_MCP_SERVER": "a=one --x=1,2; b=two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.MCPServers) != 2 || !reflect.DeepEqual(c.MCPServers[0].Argv, []string{"one", "--x=1,2"}) {
		t.Errorf("commas stay inside items: %#v", c.MCPServers)
	}
	for _, bad := range []string{"noequals", "bad name=x", "a=", `a="unterminated`} {
		if _, err := parse(t, []string{"--mcp-server", bad}, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := parse(t, []string{"--mcp-server", "a=x", "--mcp-server", "a=y"}, nil); err == nil {
		t.Error("duplicate names accepted")
	}
}

func TestTokenFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "token")
	os.WriteFile(f, []byte("  0123456789abcdef0123\n"), 0o600)
	c, err := parse(t, []string{"--listen", DefaultListen, "--allow-dir", ".", "--auth-token-file", f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthToken != "0123456789abcdef0123" {
		t.Errorf("token = %q", c.AuthToken)
	}
	_, err = parse(t, []string{"--auth-token-file", f}, map[string]string{"SH_MCP_AUTH_TOKEN": "x"})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("both token sources: %v", err)
	}
}

func TestGenTokenSkipsValidation(t *testing.T) {
	c, err := parse(t, []string{"--gen-token", "--mode", "nonsense"}, nil)
	if err != nil || !c.GenToken {
		t.Errorf("--gen-token: %v %+v", err, c)
	}
}

func TestInvalid(t *testing.T) {
	cases := []struct {
		args []string
		env  map[string]string
		want string
	}{
		{[]string{"--listen", DefaultListen}, nil, "--allow-dir"},
		{[]string{"--listen", DefaultListen, "--allow-dir", "."}, nil, "--gen-token"},
		{nil, map[string]string{"SH_MCP_INSECURE_NO_AUTH": "yes please"}, "SH_MCP_INSECURE_NO_AUTH"},
		{[]string{"--mode", "admin"}, nil, "--mode"},
		{[]string{"--mode", "ops"}, nil, "--mode must be"},
		{[]string{"--mode", "full", "--auto-approve-command", "git"}, nil, "only applies to --mode ask"},
		{[]string{"--public-url", "http://mcp.example.com"}, nil, "https"},
		{[]string{"--public-url", "https://mcp.example.com/sub"}, nil, "no path"},
		{[]string{"--transport", "http"}, nil, "not defined"},
		{[]string{"stray"}, nil, "unexpected arguments"},
	}
	for _, c := range cases {
		_, err := parse(t, c.args, c.env)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("args=%v env=%v: err=%v, want %q", c.args, c.env, err, c.want)
		}
	}
}
