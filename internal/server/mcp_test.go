package server

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/config"
)

const serveTestMCP = "serve-test-mcp"

// TestMain lets the test binary double as a tiny downstream MCP server, so
// the relay can be tested without external programs.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == serveTestMCP {
		runTestMCPServer()
		return
	}
	os.Exit(m.Run())
}

type echoInput struct {
	Text string `json:"text"`
}

func runTestMCPServer() {
	s := mcp.NewServer(&mcp.Implementation{Name: "test-downstream", Version: "1.0"}, nil)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo text back.", Annotations: readOnly},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Always fails.", Annotations: readOnly},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New("downstream broke")
		})
	mcp.AddTool(s, &mcp.Tool{Name: "save", Description: "Pretends to change something."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "saved"}}}, nil, nil
		})
	_ = s.Run(context.Background(), &mcp.StdioTransport{})
}

func testServerArgv(t *testing.T) []string {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, serveTestMCP}
}

func TestMCPRelayConfigured(t *testing.T) {
	argv := testServerArgv(t)
	h := newHarness(t, config.ModeRead, func(c *config.Config) {
		c.MCPServers = []config.MCPServer{{Name: "down", Argv: argv}}
	})
	res, err := h.sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	got := strings.Join(names, ",")
	if !strings.Contains(got, "call_mcp_tool") || strings.Contains(got, "start_mcp_server") {
		t.Errorf("read mode with configured servers: %s", got)
	}

	if out := h.ok("list_mcp_servers", nil); !strings.Contains(out, "down: stopped") || !strings.Contains(out, "configured by the owner") {
		t.Errorf("list servers: %s", out)
	}
	out := h.ok("list_mcp_tools", map[string]any{"server": "down"})
	if !strings.Contains(out, "## echo") || !strings.Contains(out, `"text"`) || !strings.Contains(out, "3 tools") {
		t.Errorf("list tools: %s", out)
	}
	out = h.ok("call_mcp_tool", map[string]any{"server": "down", "tool": "echo", "arguments": map[string]any{"text": "こんにちは"}})
	if out != "echo: こんにちは" {
		t.Errorf("call: %q", out)
	}
	h.fail("call_mcp_tool", map[string]any{"server": "down", "tool": "fail"}, "downstream broke")
	h.fail("call_mcp_tool", map[string]any{"server": "down", "tool": "save"}, "only relays read-only tools")
	h.fail("call_mcp_tool", map[string]any{"server": "nope", "tool": "echo"}, "no MCP server")
	if out := h.ok("list_mcp_servers", nil); !strings.Contains(out, "down: running") || !strings.Contains(out, "test-downstream") {
		t.Errorf("after use: %s", out)
	}
}

func TestMCPRelayDynamic(t *testing.T) {
	argv := testServerArgv(t)
	h := newHarness(t, config.ModeFull, nil)
	out := h.ok("start_mcp_server", map[string]any{"server": "dyn", "command": argv[0], "args": argv[1:]})
	if !strings.Contains(out, "started dyn") || !strings.Contains(out, "3 tools") {
		t.Fatalf("connect: %s", out)
	}
	if out := h.ok("call_mcp_tool", map[string]any{"server": "dyn", "tool": "echo", "arguments": map[string]any{"text": "x"}}); out != "echo: x" {
		t.Errorf("call: %q", out)
	}
	if out := h.ok("call_mcp_tool", map[string]any{"server": "dyn", "tool": "save"}); out != "saved" {
		t.Errorf("full mode relays any tool: %q", out)
	}
	h.ok("stop_mcp_server", map[string]any{"server": "dyn"})
	if out := h.ok("list_mcp_servers", nil); strings.Contains(out, "dyn") {
		t.Errorf("disconnected server still listed: %s", out)
	}
	h.fail("start_mcp_server", map[string]any{"server": "bad name!", "command": argv[0]}, "invalid server name")
	// The test binary with no matching tests exits at once: not an MCP server.
	h.fail("start_mcp_server", map[string]any{"server": "notmcp", "command": argv[0], "args": []string{"-test.run=^$"}}, "connect to notmcp")
	if out := h.ok("list_mcp_servers", nil); strings.Contains(out, "notmcp") {
		t.Errorf("failed server should be forgotten: %s", out)
	}
}
