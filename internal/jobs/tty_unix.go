//go:build unix

package jobs

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/yh2237/sh-mcp/internal/proc"
)

// startInTerminal starts cmd with a new pseudo-terminal as its controlling
// terminal and standard input and output.
func startInTerminal(cmd *exec.Cmd) (*proc.Tree, func() (*os.ProcessState, error), *terminal, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: ttyRows, Cols: ttyCols}); err != nil {
		ptmx.Close()
		tty.Close()
		return nil, nil, nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0 // stdin in the child
	tree, err := proc.Start(cmd)
	tty.Close() // the child has its own copy
	if err != nil {
		ptmx.Close()
		return nil, nil, nil, err
	}
	wait := func() (*os.ProcessState, error) {
		err := cmd.Wait()
		return cmd.ProcessState, err
	}
	return tree, wait, &terminal{in: ptmx, out: ptmx, finish: func(copied <-chan struct{}) {
		// With every process gone the terminal reports end of output; don't
		// wait forever if a stray process still holds it.
		select {
		case <-copied:
		case <-time.After(2 * time.Second):
		}
		ptmx.Close()
	}}, nil
}
