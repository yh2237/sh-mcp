package tools

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/audit"
	"github.com/yh2237/sh-mcp/internal/config"
	"github.com/yh2237/sh-mcp/internal/jobs"
	"github.com/yh2237/sh-mcp/internal/proxy"
	"github.com/yh2237/sh-mcp/internal/security"
)

type Deps struct {
	Cfg   *config.Config
	Guard *security.Guard
	Cmd   *security.CommandPolicy
	Jobs  *jobs.Manager
	Proxy *proxy.Manager
	Audit *audit.Logger
}

func Register(s *mcp.Server, d *Deps) {
	registerInfo(s, d)
	registerRead(s, d)
	registerSearch(s, d)
	if d.Cfg.CanWrite() {
		registerWrite(s, d)
	}
	if d.Cfg.CanExec() {
		registerExec(s, d)
	}
	if ProxyEnabled(d.Cfg) {
		registerMCP(s, d)
	}
}

// ProxyEnabled reports whether the MCP relay tools are offered: when the
// owner configured servers, or when the agent may start servers itself.
func ProxyEnabled(cfg *config.Config) bool {
	return len(cfg.MCPServers) > 0 || cfg.CanExec()
}

func ToolNames(cfg *config.Config) []string {
	names := []string{"get_environment", "read_file", "list_dir", "search_files"}
	if cfg.CanWrite() {
		names = append(names, "write_file", "edit_file", "delete_file", "move_file")
	}
	if cfg.CanExec() {
		names = append(names, "run_command", "run_shell", "get_job_output", "kill_job", "list_jobs")
	}
	if ProxyEnabled(cfg) {
		names = append(names, "list_mcp_servers", "list_mcp_tools", "call_mcp_tool")
		if cfg.CanExec() {
			names = append(names, "start_mcp_server", "stop_mcp_server")
		}
	}
	return names
}

func boolPtr(b bool) *bool { return &b }

// Every hint is set explicitly: clients treat a missing readOnlyHint as
// "may destroy data".
var (
	readOnlyAnn = &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
	writeAnn    = &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(false)}
	execAnn     = &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(true)}
)

type handler[In any] func(ctx context.Context, req *mcp.CallToolRequest, in In) ([]mcp.Content, error)

type textHandler[In any] func(ctx context.Context, req *mcp.CallToolRequest, in In) (string, error)

func addTool[In any](s *mcp.Server, d *Deps, t *mcp.Tool, summarize func(In) map[string]any, h textHandler[In]) {
	addContentTool(s, d, t, summarize, func(ctx context.Context, req *mcp.CallToolRequest, in In) ([]mcp.Content, error) {
		text, err := h(ctx, req, in)
		if err != nil {
			return nil, err
		}
		return []mcp.Content{&mcp.TextContent{Text: text}}, nil
	})
}

// Errors are returned to the model as tool errors (IsError) so it can correct
// itself. summarize must not include file contents or secrets.
func addContentTool[In any](s *mcp.Server, d *Deps, t *mcp.Tool, summarize func(In) map[string]any, h handler[In]) {
	mcp.AddTool(s, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		content, err := h(ctx, req, in)
		if ask, ok := errors.AsType[*errNeedsApproval](err); ok {
			// Not audited: the call is audited once the user has answered.
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{approvalInputID: ask.params},
				RequestState:  ask.state,
			}, nil, nil
		}
		e := audit.Entry{Tool: t.Name, Args: summarize(in), Duration: time.Since(start), OK: err == nil}
		if req != nil {
			if req.Session != nil {
				e.Session = req.Session.ID()
			}
			if req.Extra != nil && req.Extra.TokenInfo != nil {
				e.Client = req.Extra.TokenInfo.UserID
			}
		}
		if err != nil {
			e.Err = err.Error()
		}
		d.Audit.Record(e)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}},
			}, nil, nil
		}
		return &mcp.CallToolResult{Content: content}, nil, nil
	})
}
