package server

import (
	"context"
	"encoding/json"
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
Read files before editing them; edit_file needs exact old_text. Use git_status, git_diff and git_log (read-only, no approval) rather than git through run_command. Commands that run longer than wait_sec keep running as background jobs: follow them with get_job_output.
Depending on the mode, changes and commands may wait for the user's approval; if the user declines, do not retry the same action without asking.`

type Server struct {
	cfg    *config.Config
	log    *slog.Logger
	deps   *tools.Deps
	server *mcp.Server
	oauth  *oauth.Server
}

func New(cfg *config.Config, version string, log *slog.Logger) (*Server, error) {
	cmd, missing := security.NewCommandPolicy(cfg.AutoApproveCommands, cfg.EnvPassthrough, []string{config.TokenEnv})
	if len(missing) > 0 {
		log.Warn("--auto-approve-command programs not found on PATH; they will need approval", "commands", missing)
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
	// git is resolved once, like the auto-approved commands, so a git placed
	// in a working directory or later on PATH is not picked up.
	gitPath, _ := resolveProgram("git", "")
	deps := &tools.Deps{Cfg: cfg, Guard: guard, Cmd: cmd, Jobs: jobs.NewManager(), Proxy: px, Audit: al, Git: gitPath}
	s := mcp.NewServer(&mcp.Implementation{Name: "sh-mcp", Version: version}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       log,
	})
	// Clients identify themselves with initialize, or with server/discover in
	// protocol versions from 2026-07-28 on.
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err == nil && (method == "initialize" || method == "server/discover") {
				if ss, ok := req.GetSession().(*mcp.ServerSession); ok {
					logClient(log, ss.InitializeParams())
				}
			}
			return res, err
		}
	})
	tools.Register(s, deps)

	log.Info("sh-mcp configured",
		"transport", cfg.Transport,
		"mode", cfg.Mode,
		"allowed_dirs", guard.Dirs(),
		"oauth", oa != nil,
		"tools", tools.ToolNames(cfg, gitPath != ""),
	)
	return &Server{cfg: cfg, log: log, deps: deps, server: s, oauth: oa}, nil
}

// logClient records who connected and what the client says it supports, such
// as elicitation, which decides how features that need the user can work.
func logClient(log *slog.Logger, p *mcp.InitializeParams) {
	if p == nil {
		return
	}
	attrs := []any{"protocol_version", p.ProtocolVersion}
	if p.ClientInfo != nil {
		attrs = append(attrs, "client", p.ClientInfo.Name, "client_version", p.ClientInfo.Version)
	}
	if caps, err := json.Marshal(p.Capabilities); err == nil {
		attrs = append(attrs, "capabilities", json.RawMessage(caps))
	}
	log.Info("client connected", attrs...)
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
	case config.ModeAsk:
		return "read files; change files and run commands with your approval"
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
