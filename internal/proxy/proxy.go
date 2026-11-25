// Package proxy lets sh-mcp act as an MCP client of other (stdio) MCP
// servers on the same machine, so remote clients that can only reach
// sh-mcp over HTTPS can still use local-only servers.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/config"
	"github.com/yh2237/sh-mcp/internal/output"
	"github.com/yh2237/sh-mcp/internal/proc"
)

const (
	MaxServers  = 16
	initTimeout = 30 * time.Second
	idleTimeout = 30 * time.Minute
	stderrBytes = 16 << 10
)

type Spec struct {
	Name    string
	Argv    []string // Argv[0] is looked up on PATH when the server starts
	Dir     string
	Dynamic bool // started by the agent rather than configured by the owner
}

func (s Spec) CommandLine() string { return strings.Join(s.Argv, " ") }

type Manager struct {
	version string
	env     []string
	resolve func(name, dir string) (string, error)

	mu      sync.Mutex
	servers map[string]*server
	closed  bool
	stop    chan struct{}
}

type server struct {
	spec Spec

	mu       sync.Mutex
	session  *mcp.ClientSession
	tree     *proc.Tree
	cmd      *exec.Cmd
	stderr   *output.Ring
	exited   chan struct{}
	lastUsed time.Time
	lastErr  string
	retired  bool // removed, replaced or shut down: never start again
}

// NewManager returns a manager whose servers run with env; resolve maps a
// program name to an absolute path.
func NewManager(version string, env []string, resolve func(name, dir string) (string, error)) *Manager {
	m := &Manager{version: version, env: env, resolve: resolve, servers: map[string]*server{}, stop: make(chan struct{})}
	go m.reapIdle()
	return m
}

func (m *Manager) Add(spec Spec) error {
	if !config.ValidMCPName(spec.Name) {
		return fmt.Errorf("invalid server name %q (use letters, digits, '-' and '_', up to 32)", spec.Name)
	}
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return fmt.Errorf("server %q: command is empty", spec.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("server is shutting down")
	}
	if old, ok := m.servers[spec.Name]; ok {
		if !old.spec.Dynamic || !spec.Dynamic {
			return fmt.Errorf("server %q already exists", spec.Name)
		}
		go old.retire()
	} else if len(m.servers) >= MaxServers {
		return fmt.Errorf("at most %d MCP servers can be registered", MaxServers)
	}
	m.servers[spec.Name] = &server{spec: spec}
	return nil
}

// Remove stops a server; agent-started servers are also forgotten.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	s, ok := m.servers[name]
	if ok && s.spec.Dynamic {
		delete(m.servers, name)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no MCP server %q (see list_mcp_servers)", name)
	}
	if s.spec.Dynamic {
		s.retire()
	} else {
		s.shutdown() // starts again on next use
	}
	return nil
}

type Info struct {
	Name, Command, State, ServerName, ServerVersion, LastError, Stderr string
	Dynamic                                                            bool
}

func (m *Manager) List() []Info {
	m.mu.Lock()
	servers := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.mu.Unlock()
	sort.Slice(servers, func(a, b int) bool { return servers[a].spec.Name < servers[b].spec.Name })
	out := make([]Info, len(servers))
	for i, s := range servers {
		out[i] = s.info()
	}
	return out
}

func (s *server) info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := Info{Name: s.spec.Name, Command: s.spec.CommandLine(), Dynamic: s.spec.Dynamic, State: "stopped (starts on first use)", LastError: s.lastErr}
	if s.session != nil {
		i.State = "running"
		if r := s.session.InitializeResult(); r != nil && r.ServerInfo != nil {
			i.ServerName, i.ServerVersion = r.ServerInfo.Name, r.ServerInfo.Version
		}
	}
	if s.stderr != nil {
		data, _, _ := s.stderr.ReadFrom(0, stderrBytes)
		i.Stderr = string(data)
	}
	return i
}

// Session returns a connected session, starting the server if needed.
func (m *Manager) Session(ctx context.Context, name string) (*mcp.ClientSession, error) {
	m.mu.Lock()
	s, ok := m.servers[name]
	closed := m.closed
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no MCP server %q (see list_mcp_servers)", name)
	}
	if closed {
		return nil, errors.New("server is shutting down")
	}
	return s.connect(ctx, m)
}

func (s *server) connect(ctx context.Context, m *Manager) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired {
		// Removed or replaced while this call was on its way: starting it
		// now would leave a process nobody manages.
		return nil, fmt.Errorf("MCP server %q was stopped", s.spec.Name)
	}
	s.lastUsed = time.Now()
	if s.session != nil {
		select {
		case <-s.exited:
			s.cleanupLocked()
		default:
			return s.session, nil
		}
	}

	exe, err := m.resolve(s.spec.Argv[0], s.spec.Dir)
	if err != nil {
		s.lastErr = err.Error()
		return nil, fmt.Errorf("start %s: %w", s.spec.Name, err)
	}
	cmd := exec.CommandContext(context.Background(), exe, s.spec.Argv[1:]...)
	cmd.Dir = s.spec.Dir
	cmd.Env = m.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s.stderr = output.NewRing(stderrBytes)
	cmd.Stderr = s.stderr
	tree, err := proc.Start(cmd)
	if err != nil {
		s.lastErr = err.Error()
		return nil, fmt.Errorf("start %s: %w", s.spec.Name, err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		tree.Close()
		close(exited)
	}()

	initCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "sh-mcp", Version: m.version}, nil)
	session, err := client.Connect(initCtx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
	if err != nil {
		_ = tree.Kill()
		<-exited
		data, _, _ := s.stderr.ReadFrom(0, stderrBytes)
		msg := fmt.Sprintf("connect to %s: %v", s.spec.Name, err)
		if tail := strings.TrimSpace(output.Text(data)); tail != "" {
			msg += "\nstderr:\n" + tail
		}
		s.lastErr = msg
		return nil, errors.New(msg)
	}
	s.session, s.tree, s.cmd, s.exited, s.lastErr = session, tree, cmd, exited, ""
	return session, nil
}

func (s *server) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
}

// retire stops the server for good; later connects fail.
func (s *server) retire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retired = true
	s.cleanupLocked()
}

// cleanupLocked closes the session (closing the server's stdin asks it to
// exit) and then kills whatever is left of its process tree.
func (s *server) cleanupLocked() {
	if s.session == nil {
		return
	}
	_ = s.session.Close()
	select {
	case <-s.exited:
	case <-time.After(2 * time.Second):
		_ = s.tree.Kill()
		<-s.exited
	}
	s.session, s.tree, s.cmd = nil, nil, nil
}

func (m *Manager) reapIdle() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		m.mu.Lock()
		servers := make([]*server, 0, len(m.servers))
		for _, s := range m.servers {
			servers = append(servers, s)
		}
		m.mu.Unlock()
		for _, s := range servers {
			s.mu.Lock()
			if s.session != nil && time.Since(s.lastUsed) > idleTimeout {
				s.cleanupLocked()
			}
			s.mu.Unlock()
		}
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stop)
	servers := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.retire()
		}()
	}
	wg.Wait()
}
