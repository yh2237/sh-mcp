//go:build unix

package jobs

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

const (
	ttyRows = 30
	ttyCols = 120
)

// attachTTY gives cmd a new pseudo-terminal as its controlling terminal and
// stdio. It returns the controlling side; the caller closes the terminal side
// (returned by closeChild) once the process has started.
func attachTTY(cmd *exec.Cmd) (ptmx *os.File, closeChild func(), err error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, nil, err
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: ttyRows, Cols: ttyCols}); err != nil {
		ptmx.Close()
		tty.Close()
		return nil, nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0 // stdin in the child
	return ptmx, func() { tty.Close() }, nil
}
