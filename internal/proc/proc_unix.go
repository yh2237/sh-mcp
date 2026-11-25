//go:build unix

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type platformTree struct{}

func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// A new session (used for a controlling terminal) is also a new process
	// group, and setpgid would fail for a session leader.
	if !cmd.SysProcAttr.Setsid {
		cmd.SysProcAttr.Setpgid = true
	}
}

func attach(*os.Process) platformTree { return platformTree{} }

// The child is the group leader, so its pid is the pgid.
func signalGroup(p *os.Process, sig syscall.Signal) error {
	err := syscall.Kill(-p.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killTree(p *os.Process, _ *platformTree) error { return signalGroup(p, syscall.SIGKILL) }

func interruptTree(p *os.Process, _ *platformTree) error { return signalGroup(p, syscall.SIGINT) }

func release(*platformTree) {}
