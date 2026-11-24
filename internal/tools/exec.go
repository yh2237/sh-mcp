package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/jobs"
	"github.com/yh2237/sh-mcp/internal/output"
)

const (
	defaultWaitSec    = 30
	maxWaitSec        = 120
	defaultTimeoutSec = 600
	maxResultBytes    = 64 << 10
)

type runCommandInput struct {
	Command    string   `json:"command" jsonschema:"executable name (looked up on PATH) or path"`
	Args       []string `json:"args,omitempty" jsonschema:"arguments, passed as-is without shell interpretation"`
	Cwd        string   `json:"cwd,omitempty" jsonschema:"working directory, absolute or relative to default_dir (default: default_dir)"`
	Stdin      string   `json:"stdin,omitempty" jsonschema:"text to pass on standard input"`
	WaitSec    *int     `json:"wait_sec,omitempty" jsonschema:"seconds to wait for the command to finish before returning a job_id and leaving it running in the background, 0-120 (default 30; 0 starts it in the background immediately)"`
	TimeoutSec *int     `json:"timeout_sec,omitempty" jsonschema:"kill the command after this many seconds (default 600). Use 0 for servers and watchers that should keep running until kill_job"`
}

type runShellInput struct {
	Script     string `json:"script" jsonschema:"shell script; pipes, redirects, && and multiple lines are allowed"`
	Cwd        string `json:"cwd,omitempty" jsonschema:"working directory, absolute or relative to default_dir (default: default_dir)"`
	Stdin      string `json:"stdin,omitempty" jsonschema:"text to pass on standard input"`
	WaitSec    *int   `json:"wait_sec,omitempty" jsonschema:"seconds to wait for the script to finish before returning a job_id and leaving it running in the background, 0-120 (default 30; 0 starts it in the background immediately)"`
	TimeoutSec *int   `json:"timeout_sec,omitempty" jsonschema:"kill the script after this many seconds (default 600). Use 0 for servers and watchers that should keep running until kill_job"`
}

type jobOutputInput struct {
	JobID   string `json:"job_id"`
	Offset  int64  `json:"offset,omitempty" jsonschema:"byte offset to read from; pass next_offset from the previous result to get only new output"`
	WaitSec *int   `json:"wait_sec,omitempty" jsonschema:"seconds to wait for the job to finish before returning what is there, 0-120 (default 30)"`
}

type jobIDInput struct {
	JobID string `json:"job_id"`
}

type listJobsInput struct{}

const backgroundHelp = "If it is still running after wait_sec, the result has a job_id instead of an exit code and the process keeps running: " +
	"read more with get_job_output, stop it with kill_job. Output is stdout and stderr combined; long output keeps the beginning and the end."

func registerExec(s *mcp.Server, d *Deps) {
	desc := "Run a program directly, without a shell, and return its exit code and output. " + backgroundHelp
	if !d.Cmd.Unrestricted() {
		desc += " Only these programs are allowed: " + strings.Join(d.Cmd.Allowed(), ", ") + "."
	}
	addTool(s, d, &mcp.Tool{Name: "run_command", Title: "Run command", Description: desc, Annotations: execAnn},
		func(in runCommandInput) map[string]any {
			return map[string]any{"command": in.Command, "args": truncateArgs(in.Args), "cwd": in.Cwd}
		},
		func(ctx context.Context, req *mcp.CallToolRequest, in runCommandInput) (string, error) {
			dir, err := d.workDir(in.Cwd)
			if err != nil {
				return "", err
			}
			exe, err := d.Cmd.Resolve(in.Command, dir)
			if err != nil {
				return "", err
			}
			display := strings.Join(append([]string{in.Command}, in.Args...), " ")
			return d.launch(ctx, req, jobs.StartOptions{Argv: append([]string{exe}, in.Args...), Dir: dir, Stdin: in.Stdin, Display: display}, in.WaitSec, in.TimeoutSec)
		})

	if d.Cfg.CanWrite() {
		addTool(s, d, &mcp.Tool{
			Name:  "run_shell",
			Title: "Run shell script",
			Description: fmt.Sprintf("Run a script with %s and return its exit code and output. "+
				"Each call starts a fresh shell: cd and variables do not persist, so pass cwd instead. ", strings.Join(d.Cfg.Shell, " ")) + backgroundHelp,
			Annotations: execAnn,
		}, func(in runShellInput) map[string]any {
			return map[string]any{"script": truncate(in.Script, 200), "cwd": in.Cwd}
		}, func(ctx context.Context, req *mcp.CallToolRequest, in runShellInput) (string, error) {
			if strings.TrimSpace(in.Script) == "" {
				return "", errors.New("script is empty")
			}
			dir, err := d.workDir(in.Cwd)
			if err != nil {
				return "", err
			}
			argv, configure, err := d.shellArgv(in.Script)
			if err != nil {
				return "", err
			}
			return d.launch(ctx, req, jobs.StartOptions{Argv: argv, Dir: dir, Stdin: in.Stdin, Display: truncate(in.Script, 200), Configure: configure}, in.WaitSec, in.TimeoutSec)
		})
	}

	addTool(s, d, &mcp.Tool{
		Name:        "get_job_output",
		Title:       "Get job output",
		Description: "Wait for a background job to finish (up to wait_sec) and return its output from offset, its state and, once finished, its exit code.",
		Annotations: readOnlyAnn,
	}, func(in jobOutputInput) map[string]any {
		return map[string]any{"job_id": in.JobID, "offset": in.Offset}
	}, d.jobOutput)

	addTool(s, d, &mcp.Tool{
		Name:        "kill_job",
		Title:       "Kill job",
		Description: "Terminate a background job and every process it started.",
		Annotations: execAnn,
	}, func(in jobIDInput) map[string]any {
		return map[string]any{"job_id": in.JobID}
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in jobIDInput) (string, error) {
		j, err := d.Jobs.Kill(in.JobID)
		if err != nil {
			return "", err
		}
		return formatJob(j.Info()), nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "list_jobs",
		Title:       "List jobs",
		Description: "List background jobs (running and recently finished).",
		Annotations: readOnlyAnn,
	}, func(listJobsInput) map[string]any { return nil }, func(context.Context, *mcp.CallToolRequest, listJobsInput) (string, error) {
		infos := d.Jobs.List()
		if len(infos) == 0 {
			return "(no jobs)", nil
		}
		var b strings.Builder
		for _, info := range infos {
			b.WriteString(formatJob(info))
			b.WriteString("\n")
		}
		return b.String(), nil
	})
}

func secOrDefault(p *int, def, maxSec int) time.Duration {
	v := def
	if p != nil {
		v = *p
	}
	v = max(v, 0)
	if maxSec > 0 {
		v = min(v, maxSec)
	}
	return time.Duration(v) * time.Second
}

// launch starts a command as a job and waits up to wait_sec for it. A
// finished command returns its full result and is forgotten; a running one
// stays a background job.
func (d *Deps) launch(ctx context.Context, req *mcp.CallToolRequest, o jobs.StartOptions, waitSec, timeoutSec *int) (string, error) {
	o.Env = d.Cmd.Env()
	o.Timeout = secOrDefault(timeoutSec, defaultTimeoutSec, 0)
	j, err := d.Jobs.Start(o)
	if err != nil {
		return "", err
	}
	exited := j.Wait(ctx, secOrDefault(waitSec, defaultWaitSec, maxWaitSec), progressReporter(ctx, req, j))
	if !exited && ctx.Err() != nil {
		// The caller gave up (cancelled request): don't leave work behind.
		j.Kill()
		d.Jobs.Forget(j.ID)
		return "", ctx.Err()
	}
	info := j.Info()
	data, next, dropped := j.Read(0, jobs.OutputBufferBytes)
	var b strings.Builder
	if exited {
		d.Jobs.Forget(j.ID)
		b.WriteString(exitLine(info))
	} else {
		fmt.Fprintf(&b, "%s is still running in the background after %s (%s).\n", info.ID, time.Since(info.Started).Round(time.Second), timeoutText(info.Timeout))
		fmt.Fprintf(&b, "Call get_job_output with job_id=%q and offset=%d to wait for more output, or kill_job to stop it.\n", info.ID, next)
	}
	fmt.Fprintf(&b, "duration: %s\n", time.Since(info.Started).Round(time.Millisecond))
	writeOutput(&b, data, dropped)
	return b.String(), nil
}

func (d *Deps) jobOutput(ctx context.Context, req *mcp.CallToolRequest, in jobOutputInput) (string, error) {
	j, err := d.Jobs.Get(in.JobID)
	if err != nil {
		return "", err
	}
	j.Wait(ctx, secOrDefault(in.WaitSec, defaultWaitSec, maxWaitSec), progressReporter(ctx, req, j))
	info := j.Info()
	data, next, dropped := j.Read(in.Offset, maxResultBytes)
	var b strings.Builder
	b.WriteString(formatJob(info))
	b.WriteString("\n")
	fmt.Fprintf(&b, "next_offset: %d", next)
	if next < info.OutputBytes {
		fmt.Fprintf(&b, " (more output available: call again with offset=%d)", next)
	}
	b.WriteString("\n")
	if len(data) == 0 && dropped == 0 {
		b.WriteString("(no new output)\n")
		return b.String(), nil
	}
	writeOutput(&b, data, dropped)
	return b.String(), nil
}

// progressReporter sends MCP progress notifications while a call waits, so
// clients that reset their request timeout on progress keep waiting.
func progressReporter(ctx context.Context, req *mcp.CallToolRequest, j *jobs.Job) func() {
	if req == nil || req.Session == nil || req.Params == nil {
		return nil
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return nil
	}
	return func() {
		elapsed := time.Since(j.Started)
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token,
			Progress:      elapsed.Seconds(),
			Message:       fmt.Sprintf("%s running for %s", j.ID, elapsed.Round(time.Second)),
		})
	}
}

func writeOutput(b *strings.Builder, data []byte, dropped int64) {
	if dropped > 0 {
		fmt.Fprintf(b, "(%d earlier bytes of output were discarded)\n", dropped)
	}
	if len(data) == 0 {
		b.WriteString("output: (empty)\n")
		return
	}
	text := output.Text(output.TruncateMiddle(data, maxResultBytes))
	b.WriteString("--- output ---\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
}

func exitLine(i jobs.Info) string {
	switch i.State {
	case jobs.StateTimedOut:
		return fmt.Sprintf("exit_code: killed (timeout_sec %d reached; process tree terminated)\n", int(i.Timeout.Seconds()))
	case jobs.StateKilled:
		return "exit_code: killed\n"
	}
	return fmt.Sprintf("exit_code: %d\n", i.ExitCode)
}

func timeoutText(t time.Duration) string {
	if t == 0 {
		return "no timeout"
	}
	return fmt.Sprintf("killed after timeout_sec %d", int(t.Seconds()))
}

func formatJob(i jobs.Info) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s state: %s", i.ID, i.State)
	if i.State == jobs.StateRunning {
		fmt.Fprintf(&b, " (for %s, %s)", time.Since(i.Started).Round(time.Second), timeoutText(i.Timeout))
	} else {
		fmt.Fprintf(&b, ", %s", strings.TrimSpace(exitLine(i)))
	}
	fmt.Fprintf(&b, "\n  command: %s\n  cwd: %s", i.Command, i.Dir)
	return b.String()
}

func (d *Deps) workDir(cwd string) (string, error) {
	if cwd == "" {
		return d.Guard.DefaultDir(), nil
	}
	r, err := d.Guard.Resolve(cwd)
	if err != nil {
		return "", err
	}
	info, err := r.Root.Stat(r.Rel)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd %s is not a directory", r.Abs)
	}
	return r.Abs, nil
}

// shellArgv builds the command line for a shell script. PowerShell receives
// the script base64-encoded (-EncodedCommand) and cmd.exe a raw command line,
// because neither parses arguments with the rules Go uses to quote them.
func (d *Deps) shellArgv(script string) ([]string, func(*exec.Cmd), error) {
	sh := d.Cfg.Shell
	exe, err := d.Cmd.ResolveShell(sh[0])
	if err != nil {
		return nil, nil, fmt.Errorf("shell %q: %w", sh[0], err)
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe)))
	args := append([]string(nil), sh[1:]...)
	switch {
	case (base == "powershell" || base == "pwsh") && len(args) > 0 && strings.EqualFold(args[len(args)-1], "-Command"):
		// Make PowerShell write UTF-8 to the pipe instead of the OEM code page,
		// and not serialize progress bars into the output as CLIXML.
		script = "$ProgressPreference = 'SilentlyContinue'; [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); $OutputEncoding = [Console]::OutputEncoding\n" + script
		args[len(args)-1] = "-EncodedCommand"
		return append([]string{exe}, append(args, encodePowerShell(script))...), nil, nil
	case base == "cmd":
		cmdline := fmt.Sprintf(`"%s" %s /S /C "%s"`, exe, strings.Join(withoutSlashC(args), " "), script)
		return []string{exe}, func(c *exec.Cmd) {
			if c.SysProcAttr == nil {
				c.SysProcAttr = &syscall.SysProcAttr{}
			}
			setCmdLine(c.SysProcAttr, cmdline)
		}, nil
	}
	return append([]string{exe}, append(args, script)...), nil, nil
}

func withoutSlashC(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.EqualFold(a, "/C") && !strings.EqualFold(a, "/S") {
			out = append(out, a)
		}
	}
	return out
}

func encodePowerShell(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func truncateArgs(args []string) []string {
	out := make([]string, 0, min(len(args), 20))
	for i, a := range args {
		if i == 20 {
			out = append(out, fmt.Sprintf("…(%d more)", len(args)-20))
			break
		}
		out = append(out, truncate(a, 100))
	}
	return out
}
