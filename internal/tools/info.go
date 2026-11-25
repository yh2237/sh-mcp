package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/config"
)

type envInput struct{}

var probeCommands = []string{
	"git", "go", "node", "npm", "pnpm", "bun", "deno", "python3", "python", "uv",
	"cargo", "rustc", "java", "dotnet", "make", "docker", "rg", "jq", "curl",
}

func registerInfo(s *mcp.Server, d *Deps) {
	addTool(s, d, &mcp.Tool{
		Name:        "get_environment",
		Title:       "Get environment",
		Description: "Describe the machine sh-mcp runs on and what this server allows: OS, allowed directories, default working directory, mode, and which common developer tools are on PATH. Call this first to orient yourself.",
		Annotations: readOnlyAnn,
	}, func(envInput) map[string]any { return nil }, func(_ context.Context, req *mcp.CallToolRequest, _ envInput) (string, error) {
		var b strings.Builder
		host, _ := os.Hostname()
		fmt.Fprintf(&b, "os: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		fmt.Fprintf(&b, "hostname: %s\n", host)
		fmt.Fprintf(&b, "path_separator: %q\n", string(os.PathSeparator))
		fmt.Fprintf(&b, "allowed_dirs:\n")
		for _, dir := range d.Guard.Dirs() {
			fmt.Fprintf(&b, "  - %s\n", dir)
		}
		fmt.Fprintf(&b, "default_dir: %s (relative paths resolve here)\n", d.Guard.DefaultDir())
		fmt.Fprintf(&b, "mode: %s\n", d.Cfg.Mode)
		switch d.Cfg.Mode {
		case config.ModeRead:
			b.WriteString("  read files only; no file changes, no commands\n")
		case config.ModeAsk:
			b.WriteString("  change files and run commands, each after the user's approval\n")
			if names := d.Cmd.PreApprovedNames(); len(names) > 0 {
				fmt.Fprintf(&b, "  run without approval: %s\n", strings.Join(names, ", "))
			}
		case config.ModeFull:
			b.WriteString("  change files and run any command\n")
		}
		if note := d.approvalNote(req); note != "" {
			fmt.Fprintf(&b, "%s\n", note)
		}
		if d.Cfg.CanExec() {
			fmt.Fprintf(&b, "run_shell uses: %s\n", strings.Join(d.Cfg.Shell, " "))
		}
		if d.Cfg.CanExec() {
			fmt.Fprintf(&b, "commands: wait %ds by default, then continue as a background job; killed after %ds unless timeout_sec says otherwise\n",
				defaultWaitSec, defaultTimeoutSec)
		}
		var found []string
		for _, c := range probeCommands {
			if _, err := exec.LookPath(c); err == nil {
				found = append(found, c)
			}
		}
		fmt.Fprintf(&b, "tools_on_path: %s\n", strings.Join(found, ", "))
		return b.String(), nil
	})
}
