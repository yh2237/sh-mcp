package security

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var ErrCommandNotAllowed = errors.New("program is not allowed (see --allow-command)")

type CommandPolicy struct {
	unrestricted bool
	allowed      map[string]string // normalized name -> absolute path
	env          []string
}

// NewCommandPolicy resolves allowed commands to absolute paths once, so later
// PATH changes or same-named files in a working directory cannot substitute a
// different binary. Commands that cannot be found are returned in missing.
func NewCommandPolicy(unrestricted bool, allowed, envPassthrough, secretEnv []string) (p *CommandPolicy, missing []string) {
	p = &CommandPolicy{unrestricted: unrestricted, allowed: map[string]string{}}
	for _, name := range allowed {
		path, err := exec.LookPath(name)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		p.allowed[normalizeCommand(name)] = abs
	}
	p.env = buildEnv(envPassthrough, secretEnv)
	return p, missing
}

func (p *CommandPolicy) Allowed() []string {
	out := make([]string, 0, len(p.allowed))
	for k := range p.allowed {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *CommandPolicy) Unrestricted() bool { return p.unrestricted }

// cwd is used for relative paths such as "./build.sh" in unrestricted mode.
func (p *CommandPolicy) Resolve(name, cwd string) (string, error) {
	if name == "" {
		return "", errors.New("command is empty")
	}
	if !p.unrestricted {
		if abs, ok := p.allowed[normalizeCommand(name)]; ok {
			return abs, nil
		}
		if filepath.IsAbs(name) {
			for _, abs := range p.allowed {
				if samePath(abs, name) {
					return abs, nil
				}
			}
		}
		return "", fmt.Errorf("%q: %w %v", name, ErrCommandNotAllowed, p.Allowed())
	}
	if strings.ContainsAny(name, `/\`) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		return exec.LookPath(name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		// exec.ErrDot (found only in the current directory) is rejected too.
		return "", err
	}
	return filepath.Abs(path)
}

// The shell is only used in --mode full, so the ops allowlist does not apply.
func (p *CommandPolicy) ResolveShell(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func (p *CommandPolicy) Env() []string { return append([]string(nil), p.env...) }

func normalizeCommand(name string) string {
	if runtime.GOOS == "windows" {
		name = strings.ToLower(name)
		for _, ext := range []string{".exe", ".cmd", ".bat", ".com"} {
			name = strings.TrimSuffix(name, ext)
		}
	}
	return name
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if caseInsensitiveFS {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Child processes get only these variables plus --env-passthrough. A
// trailing "*" matches by prefix.
var baseEnvUnix = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "TERM", "TZ", "TMPDIR",
	"LC_*", "XDG_*",
}

var baseEnvWindows = []string{
	"PATH", "PATHEXT", "SystemRoot", "SystemDrive", "windir", "COMSPEC",
	"TEMP", "TMP", "USERPROFILE", "USERNAME", "HOMEDRIVE", "HOMEPATH",
	"APPDATA", "LOCALAPPDATA", "ProgramData", "ProgramFiles", "ProgramFiles(x86)",
	"CommonProgramFiles", "PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS", "OS",
	"PSModulePath",
}

// Toolchain locations, proxies and CA bundles, so builds work as in the
// owner's shell. None of them is a secret by itself.
var baseEnvCommon = []string{
	"GOPATH", "GOROOT", "GOBIN", "GOCACHE", "GOMODCACHE", "GOENV", "GOFLAGS", "GOTOOLCHAIN",
	"GOPROXY", "GONOPROXY", "GOPRIVATE", "GONOSUMDB", "GOSUMDB",
	"CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "NVM_DIR", "NODE_PATH",
	"PYENV_ROOT", "VIRTUAL_ENV",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
}

func buildEnv(passthrough, secret []string) []string {
	norm := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	base := baseEnvUnix
	if runtime.GOOS == "windows" {
		base = baseEnvWindows
	}
	exact := map[string]bool{}
	var prefixes []string
	for _, list := range [][]string{base, baseEnvCommon, passthrough} {
		for _, k := range list {
			if p, ok := strings.CutSuffix(k, "*"); ok {
				prefixes = append(prefixes, norm(p))
			} else {
				exact[norm(k)] = true
			}
		}
	}
	secrets := map[string]bool{}
	for _, k := range secret {
		secrets[norm(k)] = true
	}
	allowed := func(k string) bool {
		k = norm(k)
		if secrets[k] {
			return false
		}
		if exact[k] {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}

	var env []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if ok && k != "" && allowed(k) {
			env = append(env, kv)
		}
	}
	// Never block on interactive prompts: stdin is empty anyway, but git
	// would otherwise try /dev/tty for credentials.
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "PAGER=cat")
	return env
}
