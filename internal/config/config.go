package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	TransportHTTP  = "http"
	TransportStdio = "stdio"

	ModeRead = "read"
	ModeAsk  = "ask"
	ModeFull = "full"

	// The token is only accepted from the environment or a file: command-line
	// arguments are visible to every user through ps.
	TokenEnv = "SH_MCP_AUTH_TOKEN"

	MCPPath       = "/mcp"
	DefaultListen = "127.0.0.1:8080"

	// ListSeparator separates items of list-valued environment variables. It
	// does not occur in paths, globs or command lines in practice, unlike ','.
	ListSeparator = ";"
)

type Config struct {
	Transport string
	Listen    string
	PublicURL *url.URL
	AuthToken string

	Mode                string
	AllowedDirs         []string
	AutoApproveCommands []string
	DenyGlobs           []string
	EnvPassthrough      []string
	MCPServers          []MCPServer

	AuditLog    string
	ShowVersion bool
	GenToken    bool
	Update      bool
}

// MCPServer is a stdio MCP server the owner lets the agent use through
// sh-mcp, in every mode.
type MCPServer struct {
	Name string
	Argv []string
}

// CanWrite and CanExec report whether the tools exist; in ask mode each call
// additionally needs the user's approval.
func (c *Config) CanWrite() bool { return c.Mode != ModeRead }
func (c *Config) CanExec() bool  { return c.Mode != ModeRead }

var defaultDenyGlobs = []string{
	"**/.env",
	"**/.env.*",
	"!**/.env.example",
	"!**/.env.sample",
	"!**/.env.template",
	"**/*.pem",
	"**/*.key",
	"**/*id_rsa*",
	"**/*id_ed25519*",
}

func Default() *Config {
	return &Config{
		Transport: TransportStdio,
		Mode:      ModeRead,
		DenyGlobs: append([]string(nil), defaultDenyGlobs...),
	}
}

func Parse(args []string, getenv func(string) string, usageOut io.Writer) (*Config, error) {
	c := Default()
	var publicURL string
	var mcpServers []string
	fs := flag.NewFlagSet("sh-mcp", flag.ContinueOnError)
	fs.SetOutput(usageOut)
	fs.Usage = func() {
		fmt.Fprintf(usageOut, "Usage: sh-mcp [flags]\n\n"+
			"Runs over stdio unless --listen or --public-url is given, then over HTTP.\n"+
			"Every flag --x-y can also be set as SH_MCP_X_Y (flags win). Repeatable flags take\n"+
			"several values in one variable separated by %q.\n"+
			"The HTTP auth token is read from $%s.\n\n", ListSeparator, TokenEnv)
		fs.PrintDefaults()
	}
	b := &binder{fs: fs, getenv: getenv}

	b.str(&c.Mode, "mode", `what the agent may do: "read" (read files), "ask" (also change files and run commands, each approved by the user) or "full" (anything the user running sh-mcp can do)`)
	b.list(&c.AllowedDirs, "allow-dir", false, "directory the tools may access (repeatable; required for http, default for stdio: working directory)")
	b.list(&c.AutoApproveCommands, "auto-approve-command", false, "program run_command may start without approval in --mode ask (repeatable)")
	b.list(&c.DenyGlobs, "deny-glob", true, "additional glob of files the file tools must not touch, or with a leading ! files to exempt (repeatable; added to the defaults)")
	b.list(&c.EnvPassthrough, "env-passthrough", false, "extra environment variable passed to commands and MCP servers; NAME* passes every variable starting with NAME (repeatable)")
	b.list(&mcpServers, "mcp-server", false, `stdio MCP server the agent may use through sh-mcp, as name=command line, e.g. "github=npx -y @modelcontextprotocol/server-github" (repeatable)`)

	b.str(&c.Listen, "listen", "serve HTTP on this address, e.g. "+DefaultListen)
	b.str(&publicURL, "public-url", "public https URL of this server, e.g. https://mcp.example.com; serves HTTP (on "+DefaultListen+" unless --listen is given), enables OAuth and accepts that host behind a tunnel or reverse proxy")

	b.str(&c.AuditLog, "audit-log", "JSONL audit log file (default: stderr)")
	fs.BoolVar(&c.ShowVersion, "version", false, "print version and exit")
	fs.BoolVar(&c.GenToken, "gen-token", false, "print a new random auth token and exit")
	fs.BoolVar(&c.Update, "update", false, "replace this binary with the latest release (checked against its SHA256SUMS) and exit")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if c.ShowVersion || c.GenToken || c.Update {
		return c, nil
	}

	var errs []error
	c.AuthToken = getenv(TokenEnv)
	seen := map[string]bool{}
	for _, raw := range mcpServers {
		srv, err := parseMCPServer(raw)
		if err == nil && seen[srv.Name] {
			err = fmt.Errorf("--mcp-server %q is given twice", srv.Name)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		seen[srv.Name] = true
		c.MCPServers = append(c.MCPServers, srv)
	}
	if publicURL != "" {
		u, err := parsePublicURL(publicURL)
		if err != nil {
			errs = append(errs, err)
		}
		c.PublicURL = u
	}
	if c.Listen != "" || publicURL != "" {
		c.Transport = TransportHTTP
		if c.Listen == "" {
			c.Listen = DefaultListen
		}
	}
	errs = append(errs, c.Finalize())
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

var mcpNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func ValidMCPName(name string) bool { return mcpNameRE.MatchString(name) }

func parseMCPServer(raw string) (MCPServer, error) {
	name, cmdline, ok := strings.Cut(raw, "=")
	name = strings.TrimSpace(name)
	if !ok || !ValidMCPName(name) {
		return MCPServer{}, fmt.Errorf("--mcp-server must look like name=command (name: letters, digits, '-', '_'), got %q", raw)
	}
	argv, err := SplitCommandLine(cmdline)
	if err != nil || len(argv) == 0 {
		return MCPServer{}, fmt.Errorf("--mcp-server %s: invalid command line %q", name, cmdline)
	}
	return MCPServer{Name: name, Argv: argv}, nil
}

// SplitCommandLine splits on whitespace, honoring single and double quotes.
// Backslashes are literal so Windows paths need no escaping.
func SplitCommandLine(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

func parsePublicURL(s string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(s, "/"))
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, fmt.Errorf("--public-url must look like https://mcp.example.com (no path), got %q", s)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("--public-url must use https, got %q", s)
	}
	return u, nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (c *Config) Finalize() error {
	var errs []error
	switch c.Mode {
	case ModeRead, ModeAsk, ModeFull:
	default:
		errs = append(errs, fmt.Errorf("--mode must be %q, %q or %q, got %q", ModeRead, ModeAsk, ModeFull, c.Mode))
	}
	if len(c.AutoApproveCommands) > 0 && c.Mode != ModeAsk {
		errs = append(errs, errors.New("--auto-approve-command only applies to --mode ask (it lists programs that run without approval)"))
	}

	if len(c.AllowedDirs) == 0 {
		if c.Transport == TransportHTTP {
			// systemd starts services in "/", so "default to the working
			// directory" would expose the whole filesystem.
			errs = append(errs, errors.New("--allow-dir is required when serving HTTP"))
		} else {
			wd, err := os.Getwd()
			if err != nil {
				errs = append(errs, fmt.Errorf("resolve working directory: %w", err))
			} else {
				c.AllowedDirs = []string{wd}
			}
		}
	}
	for i, d := range c.AllowedDirs {
		abs, err := filepath.Abs(expandHome(d))
		if err != nil {
			errs = append(errs, fmt.Errorf("--allow-dir %s: %w", d, err))
			continue
		}
		c.AllowedDirs[i] = abs
	}

	if c.Transport == TransportHTTP {
		if c.AuthToken == "" {
			errs = append(errs, fmt.Errorf("serving HTTP requires an auth token in $%s (create one with --gen-token)", TokenEnv))
		}
		if c.AuthToken != "" && len(c.AuthToken) < 16 {
			errs = append(errs, errors.New("auth token must be at least 16 characters"))
		}
	}

	return errors.Join(errs...)
}

func envName(flagName string) string {
	return "SH_MCP_" + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// binder registers flags whose defaults come from the matching environment
// variable, so flags override the environment, which overrides built-ins.
type binder struct {
	fs     *flag.FlagSet
	getenv func(string) string
}

func (b *binder) env(name string) (string, string, bool) {
	key := envName(name)
	v := b.getenv(key)
	return key, v, v != ""
}

func (b *binder) str(p *string, name, usage string) {
	key, v, ok := b.env(name)
	if ok {
		*p = v
	}
	b.fs.StringVar(p, name, *p, fmt.Sprintf("%s [%s]", usage, key))
}

// Unless additive, the first flag replaces the environment/default value
// instead of extending it.
func (b *binder) list(p *[]string, name string, additive bool, usage string) {
	key, v, ok := b.env(name)
	if ok {
		items := splitList(v)
		if additive {
			*p = append(*p, items...)
		} else {
			*p = items
		}
	}
	b.fs.Var(&listValue{p: p, additive: additive}, name, fmt.Sprintf("%s [%s]", usage, key))
}

type listValue struct {
	p        *[]string
	additive bool
	set      bool
}

func (l *listValue) String() string {
	if l.p == nil {
		return ""
	}
	return strings.Join(*l.p, ListSeparator)
}

func (l *listValue) Set(v string) error {
	if !l.set && !l.additive {
		*l.p = nil
	}
	l.set = true
	*l.p = append(*l.p, v)
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ListSeparator) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
