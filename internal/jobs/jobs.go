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

	out      *output.Ring
	tree     *proc.Tree
	waitProc func() (*os.ProcessState, error)
	done     chan struct{}
	timer    *time.Timer
	// term is the job's pseudo-terminal (tty jobs only); copied closes once
	// its output has been read to the end. screen turns that output into the
	// plain text kept in out, except for the line still being edited.
	term   *terminal
	copied chan struct{}
	screen *output.Terminal
	// inputSem serializes writes to the terminal; exchangeMu serializes
	// write_stdin calls so each gets its own part of the output.
	inputSem   chan struct{}
	exchangeMu sync.Mutex

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

const (
	ttyRows = 30
	ttyCols = 120
)

// terminal is the controlling side of a job's pseudo-terminal.
type terminal struct {
	in  io.Writer // keystrokes
	out io.Reader // what the program prints
	// finish runs once the process tree is gone. It returns after the output
	// has been read to the end (or given up on) and releases the terminal.
	finish func(copied <-chan struct{})
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
	if o.TTY {
		tree, wait, term, err := startInTerminal(cmd)
		if err != nil {
			return nil, err
		}
		j.tree, j.waitProc, j.term, j.copied = tree, wait, term, make(chan struct{})
		j.screen = output.NewTerminal(j.out)
		j.inputSem = make(chan struct{}, 1)
		go func() {
			_, _ = io.Copy(io.MultiWriter(j.screen, poker{j.out}), term.out)
			close(j.copied)
		}()
	} else {
		tree, err := proc.Start(cmd)
		if err != nil {
			return nil, err
		}
		j.tree = tree
		j.waitProc = func() (*os.ProcessState, error) {
			err := cmd.Wait()
			return cmd.ProcessState, err
		}
	}
	j.Started = time.Now()
	if o.Timeout > 0 {
		j.timer = time.AfterFunc(o.Timeout, func() { j.stop(StateTimedOut) })
	}
	m.jobs[j.ID] = j
	go j.wait()
	if o.TTY && o.Stdin != "" {
		// Typed in the background: a program that does not read its input
		// must not hold up Start, which runs under the manager's lock.
		go func() { _ = j.WriteInput(o.Stdin, o.Timeout) }()
	}
	return j, nil
}

func (j *Job) wait() {
	state, err := j.waitProc()
	j.tree.Close()
	if j.term != nil {
		j.term.finish(j.copied)
	}
	if j.timer != nil {
		j.timer.Stop()
	}
	j.mu.Lock()
	j.ended = time.Now()
	switch {
	case state != nil:
		j.exitCode = state.ExitCode()
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
		Started: j.Started, Ended: j.ended, Timeout: j.Timeout, OutputBytes: j.out.Total(), TTY: j.term != nil,
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

// Read returns output from offset. For a terminal job that reaches the end,
// the line still being edited (typically a prompt) is appended; next does not
// include it, so it is returned again, finished, by a later read.
func (j *Job) Read(offset int64, maxBytes int) (data []byte, next, dropped int64) {
	data, next, dropped = j.out.ReadFrom(offset, maxBytes)
	if j.screen != nil && next == j.out.Total() {
		data = append(data, j.screen.Pending()...)
	}
	return data, next, dropped
}

// poker counts any terminal output as activity, even before it becomes text.
type poker struct{ r *output.Ring }

func (p poker) Write(b []byte) (int, error) {
	p.r.Poke()
	return len(b), nil
}

// ErrNoTTY is returned when input is sent to a job without a terminal.
var ErrNoTTY = errors.New("this job was not started with tty=true, so its input is closed; start it again with tty=true to interact with it")

func (j *Job) TTY() bool { return j.term != nil }

// ErrInputBlocked is returned when the program does not read what is typed.
var ErrInputBlocked = errors.New("the program is not reading its input; the rest will be typed when it does")

// WriteInput types s into the job's terminal, giving up after timeout (0
// means no limit) if the program does not read it. Writes never interleave.
func (j *Job) WriteInput(s string, timeout time.Duration) error {
	if j.term == nil {
		return ErrNoTTY
	}
	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	select {
	case j.inputSem <- struct{}{}:
	case <-j.done:
		return fmt.Errorf("%s has already ended", j.ID)
	case <-expired:
		return ErrInputBlocked
	}
	written := make(chan error, 1)
	go func() {
		_, err := j.term.in.Write([]byte(s))
		<-j.inputSem
		written <- err
	}()
	select {
	case err := <-written:
		return err
	case <-j.done:
		return fmt.Errorf("%s has already ended", j.ID)
	case <-expired:
		return ErrInputBlocked
	}
}

// Exchange types input into a terminal job (or, for a job without one,
// delivers a lone Ctrl-C as a signal) and returns the output that follows,
// once it pauses for idle or wait elapses. Calls are serialized, so each
// returns the output after the previous one.
func (j *Job) Exchange(ctx context.Context, input string, wait, idle time.Duration, maxBytes int) (data []byte, dropped int64, more bool, err error) {
	j.exchangeMu.Lock()
	defer j.exchangeMu.Unlock()
	_, since, _ := j.out.Watch()
	from := j.Cursor()
	if j.term == nil {
		from = j.out.Total() // before the signal, so its handler's output is kept
		if input != "" {
			if input != "\x03" {
				return nil, 0, false, ErrNoTTY
			}
			if err := j.Interrupt(); err != nil {
				return nil, 0, false, err
			}
		}
	} else if input != "" {
		if err := j.WriteInput(input, wait); err != nil {
			return nil, 0, false, err
		}
	}
	j.WaitOutput(ctx, since, wait, idle)
	data, next, dropped := j.Read(from, maxBytes)
	if j.term != nil {
		j.SetCursor(next)
	}
	return data, dropped, next < j.out.Total(), nil
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

// WaitOutput waits for output activity after the activity count since and
// returns once it has paused for idle, so interactive programs answer
// quickly; it also returns when the job ends, d elapses or ctx is done.
// Counting activity (not bytes) also notices a prompt being redrawn, which
// adds no finished line.
func (j *Job) WaitOutput(ctx context.Context, since uint64, d, idle time.Duration) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	quiet := time.NewTimer(idle)
	quiet.Stop() // armed once something happens
	defer quiet.Stop()
	for {
		changed, activity, closed := j.out.Watch()
		if closed {
			return
		}
		if activity != since {
			since = activity
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
