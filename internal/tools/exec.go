package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
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
	TTY        bool     `json:"tty,omitempty" jsonschema:"run in a pseudo-terminal so the program can be driven with write_stdin: shells, REPLs, ssh, password prompts, interactive installers. Output has terminal control codes removed. Use timeout_sec 0 for a session that should stay open"`
}

type runShellInput struct {
	Script     string `json:"script" jsonschema:"shell script; pipes, redirects, && and multiple lines are allowed"`
	Cwd        string `json:"cwd,omitempty" jsonschema:"working directory, absolute or relative to default_dir (default: default_dir)"`
	Stdin      string `json:"stdin,omitempty" jsonschema:"text to pass on standard input"`
	WaitSec    *int   `json:"wait_sec,omitempty" jsonschema:"seconds to wait for the script to finish before returning a job_id and leaving it running in the background, 0-120 (default 30; 0 starts it in the background immediately)"`
	TimeoutSec *int   `json:"timeout_sec,omitempty" jsonschema:"kill the script after this many seconds (default 600). Use 0 for servers and watchers that should keep running until kill_job"`
	TTY        bool   `json:"tty,omitempty" jsonschema:"run in a pseudo-terminal so the program can be driven with write_stdin: shells, REPLs, ssh, password prompts, interactive installers. Output has terminal control codes removed. Use timeout_sec 0 for a session that should stay open"`
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

type writeStdinInput struct {
	JobID   string `json:"job_id"`
	Input   string `json:"input,omitempty" jsonschema:"keys to type into the job's terminal; end a line with \n. Control keys are JSON escapes: \u0003 Ctrl-C, \u0004 Ctrl-D (end of input), \u001b Escape, \u001b[A up arrow. Empty input just reads new output"`
	WaitSec *int   `json:"wait_sec,omitempty" jsonschema:"seconds to wait for output at most, 0-120 (default 5); returns as soon as the output pauses"`
}

const (
	defaultStdinWaitSec = 5
	// outputPause is how long output must stop before write_stdin returns:
	// a prompt has usually been printed by then.
	outputPause = 300 * time.Millisecond
)

const backgroundHelp = "If it is still running after wait_sec, the result has a job_id instead of an exit code and the process keeps running: " +
	"read more with get_job_output, stop it with kill_job. Output is stdout and stderr combined; long output keeps the beginning and the end."

func registerExec(s *mcp.Server, d *Deps) {
	desc := "Run a program directly, without a shell, and return its exit code and output. " + backgroundHelp
	if d.needsApproval() {
		desc += " Each command waits for the user's approval"
		if names := d.Cmd.PreApprovedNames(); len(names) > 0 {
			desc += ", except " + strings.Join(names, ", ")
		}
		desc += "."
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
			if !d.Cmd.PreApproved(exe) {
				msg := fmt.Sprintf("Run this command?\ncommand: %s\nprogram: %s\ncwd: %s", display, exe, dir)
				if in.TTY {
					msg += "\n(in a terminal; later input is approved separately)"
				}
				if in.Stdin != "" {
					msg += "\n--- stdin ---\n" + in.Stdin
				}
				if err := d.approve(req, msg); err != nil {
					return "", err
				}
			}
			return d.launch(ctx, req, jobs.StartOptions{Argv: append([]string{exe}, in.Args...), Dir: dir, Stdin: in.Stdin, Display: display, TTY: in.TTY}, in.WaitSec, in.TimeoutSec)
		})

	shellDesc := fmt.Sprintf("Run a script with %s and return its exit code and output. "+
		"Each call starts a fresh shell: cd and variables do not persist, so pass cwd instead. ", shellName) + backgroundHelp
	if d.needsApproval() {
		shellDesc += " Each script waits for the user's approval."
	}
	addTool(s, d, &mcp.Tool{
		Name:        "run_shell",
		Title:       "Run shell script",
		Description: shellDesc,
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
		argv, err := d.shellArgv(in.Script)
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Run this script with %s?\ncwd: %s\n---\n%s", shellName, dir, in.Script)
		if in.Stdin != "" {
			msg += "\n--- stdin ---\n" + in.Stdin
		}
		if err := d.approve(req, msg); err != nil {
			return "", err
		}
		return d.launch(ctx, req, jobs.StartOptions{Argv: argv, Dir: dir, Stdin: in.Stdin, Display: truncate(in.Script, 200), TTY: in.TTY}, in.WaitSec, in.TimeoutSec)
	})

	addTool(s, d, &mcp.Tool{
		Name:        "get_job_output",
		Title:       "Get job output",
		Description: "Wait for a background job to finish (up to wait_sec) and return its output from offset, its state and, once finished, its exit code.",
		Annotations: readOnlyAnn,
	}, func(in jobOutputInput) map[string]any {
		return map[string]any{"job_id": in.JobID, "offset": in.Offset}
	}, d.jobOutput)

	stdinDesc := "Type into a job started with tty=true (a shell, REPL, ssh session or password prompt) and return the output that follows; " +
		"empty input just reads new output. It returns as soon as the output pauses, which usually means the program waits for input again. " +
		`For a job without a terminal, only Ctrl-C ("\u0003") can be sent.`
	if d.needsApproval() {
		stdinDesc += " Each input waits for the user's approval."
	}
	addTool(s, d, &mcp.Tool{
		Name:        "write_stdin",
		Title:       "Write to job",
		Description: stdinDesc,
		Annotations: execAnn,
	}, func(in writeStdinInput) map[string]any {
		// The input itself is not logged: it may be a password.
		return map[string]any{"job_id": in.JobID, "input_bytes": len(in.Input)}
	}, d.writeStdin)

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
	if o.TTY {
		// Programs that draw on a terminal look at TERM; their control codes
		// are removed from the output anyway.
		o.Env = append(o.Env, "TERM=xterm-256color")
	}
	o.Timeout = secOrDefault(timeoutSec, defaultTimeoutSec, 0)
	j, err := d.Jobs.Start(o)
	if err != nil {
		return "", err
	}
	wait := secOrDefault(waitSec, defaultWaitSec, maxWaitSec)
	var exited bool
	if o.TTY {
		// An interactive program rarely exits: return once it waits for
		// input, which shows as a pause in its output.
		j.WaitOutput(ctx, 0, wait, outputPause)
		exited = j.Wait(ctx, 0, nil)
	} else {
		exited = j.Wait(ctx, wait, progressReporter(ctx, req, j))
	}
	if !exited && ctx.Err() != nil {
		// The caller gave up (cancelled request): don't leave work behind.
		j.Kill()
		d.Jobs.Forget(j.ID)
		return "", ctx.Err()
	}
	info := j.Info()
	// A finished command shows the beginning and end of all its output. A
	// running one returns a contiguous first page, so that continuing from
	// next with get_job_output does not skip anything.
	readMax := maxResultBytes
	if exited {
		readMax = jobs.OutputBufferBytes
	}
	data, next, dropped := j.Read(0, readMax)
	var b strings.Builder
	if exited {
		d.Jobs.Forget(j.ID)
		b.WriteString(exitLine(info))
	} else if info.TTY {
		j.SetCursor(next)
		fmt.Fprintf(&b, "%s is running in a terminal (%s). Type into it with write_stdin job_id=%q; stop it with kill_job.\n", info.ID, timeoutText(info.Timeout), info.ID)
	} else {
		fmt.Fprintf(&b, "%s is still running in the background after %s (%s).\n", info.ID, time.Since(info.Started).Round(time.Second), timeoutText(info.Timeout))
		fmt.Fprintf(&b, "Call get_job_output with job_id=%q and offset=%d to wait for more output, or kill_job to stop it.\n", info.ID, next)
	}
	fmt.Fprintf(&b, "duration: %s\n", time.Since(info.Started).Round(time.Millisecond))
	writeOutput(&b, data, dropped)
	return b.String(), nil
}

// writeStdin types into a terminal job (or interrupts a job without one) and
// returns the output that follows.
func (d *Deps) writeStdin(ctx context.Context, req *mcp.CallToolRequest, in writeStdinInput) (string, error) {
	j, err := d.Jobs.Get(in.JobID)
	if err != nil {
		return "", err
	}
	if in.Input != "" && j.TTY() {
		msg := fmt.Sprintf("Type this into %s (%s)?\n%s", j.ID, j.Command, strconv.Quote(in.Input))
		if err := d.approve(req, msg); err != nil {
			return "", err
		}
	}
	// Without a terminal only Ctrl-C can be sent, as a signal (like kill_job,
	// it needs no approval).
	data, dropped, more, err := j.Exchange(ctx, in.Input, secOrDefault(in.WaitSec, defaultStdinWaitSec, maxWaitSec), outputPause, maxResultBytes)
	if err != nil {
		return "", err
	}
	info := j.Info()
	var b strings.Builder
	b.WriteString(formatJob(info))
	b.WriteString("\n")
	if len(data) == 0 && dropped == 0 {
		b.WriteString("(no new output)\n")
		return b.String(), nil
	}
	writeOutput(&b, data, dropped)
	if more {
		b.WriteString("(more output follows: call write_stdin with empty input to read it)\n")
	}
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

// writeOutput appends a job's output (for terminal jobs already plain text).
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

// shellName is what run_shell runs scripts with: sh on Unix, PowerShell on
// Windows.
var shellName = map[bool]string{false: "/bin/sh -c", true: "PowerShell"}[runtime.GOOS == "windows"]

// shellArgv builds the command line for a script. PowerShell receives it
// base64-encoded (-EncodedCommand) because it does not parse arguments by the
// rules Go uses to quote them.
func (d *Deps) shellArgv(script string) ([]string, error) {
	if runtime.GOOS != "windows" {
		exe, err := d.Cmd.ResolveShell("/bin/sh")
		if err != nil {
			return nil, fmt.Errorf("shell /bin/sh: %w", err)
		}
		return []string{exe, "-c", script}, nil
	}
	exe, err := d.Cmd.ResolveShell("powershell.exe")
	if err != nil {
		return nil, fmt.Errorf("shell powershell.exe: %w", err)
	}
	// Make PowerShell write UTF-8 to the pipe instead of the OEM code page,
	// and not serialize progress bars into the output as CLIXML.
	script = "$ProgressPreference = 'SilentlyContinue'; [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); $OutputEncoding = [Console]::OutputEncoding\n" + script
	return []string{exe, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(script)}, nil
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
