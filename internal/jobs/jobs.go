package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yh2237/sh-mcp/internal/output"
	"github.com/yh2237/sh-mcp/internal/proc"
)

const (
	StateRunning  = "running"
	StateExited   = "exited"
	StateKilled   = "killed"
	StateTimedOut = "timed_out"

	MaxRunning        = 16
	maxRetained       = 32
	OutputBufferBytes = 1 << 20
)

// Every command runs as a job, so a call can stop waiting and leave the
// process running in the background (the model used by Codex CLI's unified
// exec). Its lifetime is bounded by the caller's timeout, kill_job, or
// server shutdown.
type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*Job
	seq    int
	closed bool
}

func NewManager() *Manager {
	return &Manager{jobs: map[string]*Job{}}
}

type Job struct {
	ID      string
	Command string
	Dir     string
	Started time.Time
	Timeout time.Duration

	out   *output.Ring
	tree  *proc.Tree
	cmd   *exec.Cmd
	done  chan struct{}
	timer *time.Timer
	// ptmx is the controlling side of the job's pseudo-terminal (tty jobs
	// only); copied closes once its output has been read to the end.
	ptmx   *os.File
	copied chan struct{}

	mu       sync.Mutex
	state    string
	stopAs   string
	exitCode int
	ended    time.Time
	cursor   int64 // where write_stdin continues reading
}

type Info struct {
	ID, Command, Dir, State string
	ExitCode                int
	Started, Ended          time.Time
	Timeout                 time.Duration
	OutputBytes             int64
	TTY                     bool
}

type StartOptions struct {
	Argv      []string // Argv[0] must be an absolute path
	Dir       string
	Env       []string
	Stdin     string
	Display   string
	Timeout   time.Duration // 0 means no limit
	Configure func(*exec.Cmd)
	// TTY runs the command in a pseudo-terminal whose input stays open for
	// WriteInput (interactive programs).
	TTY bool
}

func (m *Manager) Start(o StartOptions) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("server is shutting down")
	}
	var running []string
	for _, j := range m.jobs {
		if j.State() == StateRunning {
			running = append(running, j.ID)
		}
	}
	if len(running) >= MaxRunning {
		sort.Strings(running)
		return nil, fmt.Errorf("%d jobs are already running (limit %d): %s; kill one with kill_job first",
			len(running), MaxRunning, strings.Join(running, ", "))
	}
	m.pruneLocked()

	m.seq++
	j := &Job{
		ID:      "job-" + strconv.Itoa(m.seq),
		Command: o.Display,
		Dir:     o.Dir,
		Timeout: o.Timeout,
		out:     output.NewRing(OutputBufferBytes),
		done:    make(chan struct{}),
		state:   StateRunning,
	}
	// proc.Start installs a tree-killing Cancel, which exec only permits on
	// commands created with a context. Jobs are stopped through stop instead,
	// so the context is never cancelled.
	cmd := exec.CommandContext(context.Background(), o.Argv[0], o.Argv[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	cmd.Stdout = j.out
	cmd.Stderr = j.out
	if o.Stdin != "" && !o.TTY {
		cmd.Stdin = strings.NewReader(o.Stdin)
	}
	if o.Configure != nil {
		o.Configure(cmd)
	}
	closeChild := func() {}
	if o.TTY {
		ptmx, closeTTY, err := attachTTY(cmd)
		if err != nil {
			return nil, err
		}
		j.ptmx, j.copied, closeChild = ptmx, make(chan struct{}), closeTTY
	}
	tree, err := proc.Start(cmd)
	closeChild() // the child has its own copy of the terminal side
	if err != nil {
		if j.ptmx != nil {
			j.ptmx.Close()
		}
		return nil, err
	}
	j.cmd, j.tree, j.Started = cmd, tree, time.Now()
	if j.ptmx != nil {
		go func() {
			_, _ = io.Copy(j.out, j.ptmx)
			close(j.copied)
		}()
		if o.Stdin != "" {
			_, _ = j.ptmx.Write([]byte(o.Stdin))
		}
	}
	if o.Timeout > 0 {
		j.timer = time.AfterFunc(o.Timeout, func() { j.stop(StateTimedOut) })
	}
	m.jobs[j.ID] = j
	go j.wait()
	return j, nil
}

func (j *Job) wait() {
	err := j.cmd.Wait()
	j.tree.Close()
	if j.ptmx != nil {
		// With every process gone the terminal reports end of output;
		// don't wait forever if a stray process still holds it.
		select {
		case <-j.copied:
		case <-time.After(2 * time.Second):
		}
		j.ptmx.Close()
	}
	if j.timer != nil {
		j.timer.Stop()
	}
	j.mu.Lock()
	j.ended = time.Now()
	switch {
	case j.cmd.ProcessState != nil:
		j.exitCode = j.cmd.ProcessState.ExitCode()
	case err != nil:
		j.exitCode = -1
	}
	j.state = StateExited
	if j.stopAs != "" {
		j.state = j.stopAs
	}
	j.mu.Unlock()
	j.out.Close()
	close(j.done)
}

func (j *Job) stop(as string) {
	j.mu.Lock()
	if j.state == StateRunning && j.stopAs == "" {
		j.stopAs = as
	}
	j.mu.Unlock()
	_ = j.tree.Kill()
}

func (m *Manager) pruneLocked() {
	var finished []*Job
	for _, j := range m.jobs {
		if j.State() != StateRunning {
			finished = append(finished, j)
		}
	}
	if len(finished) < maxRetained {
		return
	}
	sort.Slice(finished, func(a, b int) bool { return finished[a].Started.Before(finished[b].Started) })
	for _, j := range finished[:len(finished)-maxRetained+1] {
		delete(m.jobs, j.ID)
	}
}

func (m *Manager) Get(id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, fmt.Errorf("no job %q (see list_jobs)", id)
	}
	return j, nil
}

func (m *Manager) Forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, id)
}

func (m *Manager) List() []Info {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].Started.Before(jobs[b].Started) })
	out := make([]Info, len(jobs))
	for i, j := range jobs {
		out[i] = j.Info()
	}
	return out
}

func (m *Manager) Kill(id string) (*Job, error) {
	j, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	j.Kill()
	return j, nil
}

// Kill waits briefly for the process tree to exit.
func (j *Job) Kill() {
	j.stop(StateKilled)
	select {
	case <-j.done:
	case <-time.After(5 * time.Second):
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	for _, j := range jobs {
		j.stop(StateKilled)
	}
	for _, j := range jobs {
		select {
		case <-j.done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (j *Job) State() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

func (j *Job) Info() Info {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Info{
		ID: j.ID, Command: j.Command, Dir: j.Dir, State: j.state, ExitCode: j.exitCode,
		Started: j.Started, Ended: j.ended, Timeout: j.Timeout, OutputBytes: j.out.Total(), TTY: j.ptmx != nil,
	}
}

// Wait blocks until the job exits, d elapses or ctx is done, calling tick
// periodically while it waits. It reports whether the job has exited.
func (j *Job) Wait(ctx context.Context, d time.Duration, tick func()) bool {
	if d <= 0 {
		select {
		case <-j.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-j.done:
			return true
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if tick != nil {
				tick()
			}
		}
	}
}

func (j *Job) Read(offset int64, maxBytes int) (data []byte, next, dropped int64) {
	return j.out.ReadFrom(offset, maxBytes)
}

// ErrNoTTY is returned when input is sent to a job without a terminal.
var ErrNoTTY = errors.New("this job was not started with tty=true, so its input is closed; start it again with tty=true to interact with it")

func (j *Job) TTY() bool { return j.ptmx != nil }

// WriteInput types s into the job's terminal.
func (j *Job) WriteInput(s string) error {
	if j.ptmx == nil {
		return ErrNoTTY
	}
	select {
	case <-j.done:
		return fmt.Errorf("%s has already ended", j.ID)
	default:
	}
	_, err := j.ptmx.Write([]byte(s))
	return err
}

// Interrupt sends Ctrl-C to the job's processes (for jobs without a terminal,
// where Ctrl-C cannot be typed).
func (j *Job) Interrupt() error { return j.tree.Interrupt() }

// Cursor is the offset up to which write_stdin has returned output.
func (j *Job) Cursor() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cursor
}

func (j *Job) SetCursor(offset int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cursor = max(j.cursor, offset)
}

// WaitOutput waits for output beyond from and returns once it has paused for
// idle, so interactive programs answer quickly; it also returns when the job
// ends, d elapses or ctx is done.
func (j *Job) WaitOutput(ctx context.Context, from int64, d, idle time.Duration) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	quiet := time.NewTimer(idle)
	quiet.Stop() // armed once output arrives
	defer quiet.Stop()
	for {
		changed, total, closed := j.out.Changed()
		if closed {
			return
		}
		if total > from {
			quiet.Reset(idle)
		}
		select {
		case <-changed:
		case <-quiet.C:
			return
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		case <-j.done:
			return
		}
	}
}
