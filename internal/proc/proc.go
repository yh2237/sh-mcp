// Package proc starts commands so that the whole process tree they spawn can
// be terminated: a process group on Unix, a Job Object on Windows.
//
// exec.CommandContext alone only kills the direct child. Grandchildren (the
// compiler under make, the server under a shell) would survive a timeout and
// keep the output pipes open, so Wait would never return.
package proc

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

// WaitDelay bounds how long Wait keeps reading output after the tree has
// been killed or the direct child exited while descendants hold the pipes.
const WaitDelay = 3 * time.Second

type Tree struct {
	mu     sync.Mutex
	p      *os.Process
	plat   platformTree
	closed bool
}

// Start starts cmd in a new process tree. cmd.Cancel is set to kill the
// whole tree, so a CommandContext timeout terminates descendants too.
func Start(cmd *exec.Cmd) (*Tree, error) {
	t := &Tree{}
	prepare(cmd)
	cmd.Cancel = t.Kill
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = WaitDelay
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t.p = cmd.Process
	t.plat = attach(cmd.Process)
	return t, nil
}

// Process is the direct child.
func (t *Tree) Process() *os.Process { return t.p }

// Kill is safe to call repeatedly and concurrently.
func (t *Tree) Kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	// After Close the process group or job is gone; its id may be reused.
	if t.p == nil || t.closed {
		return nil
	}
	return killTree(t.p, &t.plat)
}

// Interrupt sends Ctrl-C (SIGINT) to every process in the tree.
func (t *Tree) Interrupt() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.p == nil || t.closed {
		return nil
	}
	return interruptTree(t.p, &t.plat)
}

// Close kills any processes still left in the tree (for example background
// jobs started by a shell script) and releases OS resources. Call it after
// the direct child has been waited for.
func (t *Tree) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	_ = killTree(t.p, &t.plat)
	release(&t.plat)
}
