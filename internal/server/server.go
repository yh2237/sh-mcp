package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/audit"
	"github.com/yh2237/sh-mcp/internal/config"
	"github.com/yh2237/sh-mcp/internal/jobs"
	"github.com/yh2237/sh-mcp/internal/oauth"
	"github.com/yh2237/sh-mcp/internal/proxy"
	"github.com/yh2237/sh-mcp/internal/security"
	"github.com/yh2237/sh-mcp/internal/tools"
)

const instructions = `sh-mcp gives you access to files (and, depending on its mode, commands) on the machine it runs on, like working in a terminal there.
Start with get_environment to learn the OS, allowed directories, default directory, mode and installed tools.
Paths may be absolute or relative to the default directory; only paths inside the allowed directories are accessible.
Read files before editing them; edit_file needs exact old_text. Commands that run longer than wait_sec keep running as background jobs: follow them with get_job_output.`

type Server struct {
	cfg    *config.Config
	log    *slog.Logger
	deps   *tools.Deps
	server *mcp.Server
	oauth  *oauth.Server
}

func New(cfg *config.Config, version string, log *slog.Logger) (*Server, error) {
	cmd, missing := security.NewCommandPolicy(cfg.Mode == config.ModeFull, cfg.AllowedCommands, cfg.EnvPassthrough, []string{config.TokenEnv})
	if cfg.Mode == config.ModeOps {
		if len(cmd.Allowed()) == 0 {
			return nil, fmt.Errorf("none of the --allow-command programs were found on PATH: %v", missing)
		}
		if len(missing) > 0 {
			log.Warn("allowed commands not found on PATH; they will be unavailable", "commands", missing)
		}
	}
	var oa *oauth.Server
	if cfg.Transport == config.TransportHTTP && cfg.PublicURL != nil && cfg.AuthToken != "" {
		var err error
		if oa, err = oauth.New(cfg.PublicURL, config.MCPPath, cfg.AuthToken, accessDescription(cfg)); err != nil {
			return nil, err
		}
	}
	guard, err := security.NewGuard(cfg.AllowedDirs, cfg.DenyGlobs)
	if err != nil {
		return nil, err
	}
	al, err := audit.Open(cfg.AuditLog)
	if err != nil {
		guard.Close()
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	px := proxy.NewManager(version, cmd.Env(), resolveProgram)
	for _, srv := range cfg.MCPServers {
		if err := px.Add(proxy.Spec{Name: srv.Name, Argv: srv.Argv, Dir: guard.DefaultDir()}); err != nil {
			px.Shutdown()
			guard.Close()
			al.Close()
			return nil, err
		}
	}
	deps := &tools.Deps{Cfg: cfg, Guard: guard, Cmd: cmd, Jobs: jobs.NewManager(), Proxy: px, Audit: al}
	s := mcp.NewServer(&mcp.Implementation{Name: "sh-mcp", Version: version}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       log,
	})
	tools.Register(s, deps)

	log.Info("sh-mcp configured",
		"transport", cfg.Transport,
		"mode", cfg.Mode,
		"allowed_dirs", guard.Dirs(),
		"oauth", oa != nil,
		"tools", tools.ToolNames(cfg),
	)
	return &Server{cfg: cfg, log: log, deps: deps, server: s, oauth: oa}, nil
}

// resolveProgram finds an MCP server program. Configured servers were chosen
// by the owner and agent-started ones only exist in full mode, so neither is
// subject to the ops allowlist.
func resolveProgram(name, dir string) (string, error) {
	if strings.ContainsAny(name, `/\`) && !filepath.IsAbs(name) {
		name = filepath.Join(dir, name)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}

func accessDescription(cfg *config.Config) string {
	switch cfg.Mode {
	case config.ModeFull:
		return "read and change files, run any command"
	case config.ModeOps:
		return "read files, run " + strings.Join(cfg.AllowedCommands, ", ")
	}
	return "read files"
}

func (s *Server) MCP() *mcp.Server { return s.server }

func (s *Server) Audit() *audit.Logger { return s.deps.Audit }

func (s *Server) Close() {
	s.deps.Jobs.Shutdown()
	s.deps.Proxy.Shutdown()
	s.deps.Guard.Close()
	s.deps.Audit.Close()
}

func (s *Server) Run(ctx context.Context) error {
	if s.cfg.Transport == config.TransportStdio {
		return s.server.Run(ctx, &mcp.StdioTransport{})
	}
	return s.runHTTP(ctx)
}

func (s *Server) Handler() http.Handler {
	public := s.cfg.PublicURL
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.server }, &mcp.StreamableHTTPOptions{
		Logger:         s.log,
		SessionTimeout: time.Hour,
		// security.CheckHost replaces this check when --public-url is set.
		DisableLocalhostProtection: public != nil,
	})
	if s.cfg.AuthToken != "" {
		var verifyOAuth func(string) (*auth.TokenInfo, error)
		metadataURL := ""
		if s.oauth != nil {
			verifyOAuth = s.oauth.VerifyAccessToken
			metadataURL = s.oauth.ResourceMetadataURL()
		}
		mcpHandler = security.RequireBearer(security.BearerVerifier(s.cfg.AuthToken, verifyOAuth), metadataURL, mcpHandler)
	}
	csrf := http.NewCrossOriginProtection()
	mcpHandler = csrf.Handler(mcpHandler)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle(config.MCPPath, mcpHandler)
	mux.Handle(config.MCPPath+"/", mcpHandler)
	if s.oauth != nil {
		s.oauth.Register(mux, csrf)
	}
	if public != nil {
		return security.CheckHost([]string{public.Host}, mux)
	}
	return mux
}

func (s *Server) runHTTP(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if s.cfg.AuthToken == "" {
		s.log.Warn("HTTP transport is running WITHOUT authentication (--insecure-no-auth)")
	}
	if s.cfg.PublicURL != nil && s.cfg.AuthToken != "" {
		s.log.Info("OAuth enabled", "mcp_url", s.oauth.Resource())
	}
	s.log.Info("listening", "addr", ln.Addr().String(), "path", config.MCPPath)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
