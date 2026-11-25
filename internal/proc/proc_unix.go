//go:build unix

package proc

import (
	"errors"
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

func interruptTree(cmd *exec.Cmd, _ *platformTree) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func attach(*exec.Cmd) platformTree { return platformTree{} }

func killTree(cmd *exec.Cmd, _ *platformTree) error {
	// The child is the group leader, so its pid is the pgid.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func release(*platformTree) {}
