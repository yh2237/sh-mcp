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
	cmd.SysProcAttr.Setpgid = true
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
