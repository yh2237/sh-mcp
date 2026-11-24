// Package proc starts commands so that the whole process tree they spawn can
// be terminated: a process group on Unix, a Job Object on Windows.
//
// exec.CommandContext alone only kills the direct child. Grandchildren (the
// compiler under make, the server under a shell) would survive a timeout and
// keep the output pipes open, so Wait would never return.
package proc

import (
	"os/exec"
	"sync"
	"time"
)

// WaitDelay bounds how long Wait keeps reading output after the tree has
// been killed or the direct child exited while descendants hold the pipes.
const WaitDelay = 3 * time.Second

type Tree struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	plat   platformTree
	closed bool
}

// Start starts cmd in a new process tree. cmd.Cancel is set to kill the
// whole tree, so a CommandContext timeout terminates descendants too.
func Start(cmd *exec.Cmd) (*Tree, error) {
	t := &Tree{cmd: cmd}
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
	t.plat = attach(cmd)
	return t, nil
}

// Kill is safe to call repeatedly and concurrently.
func (t *Tree) Kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd.Process == nil {
		return nil
	}
	return killTree(t.cmd, &t.plat)
}

// Close kills any processes still left in the tree (for example background
// jobs started by a shell script) and releases OS resources. Call it after
// Wait returns.
func (t *Tree) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	_ = killTree(t.cmd, &t.plat)
	release(&t.plat)
}
