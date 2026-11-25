//go:build windows

package proc

import (
	"errors"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type platformTree struct {
	job windows.Handle
}

func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

// attach puts the started process in a Job Object that kills all members
// when closed. Descendants inherit the job. A grandchild spawned in the few
// microseconds between Start and the assignment would escape; Go offers no
// way to start the child suspended, so that window is accepted.
func attach(cmd *exec.Cmd) platformTree {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return platformTree{}
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return platformTree{}
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return platformTree{}
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		windows.CloseHandle(job)
		return platformTree{}
	}
	return platformTree{job: job}
}

func killTree(cmd *exec.Cmd, t *platformTree) error {
	if t.job != 0 {
		return windows.TerminateJobObject(t.job, 1)
	}
	return cmd.Process.Kill()
}

func release(t *platformTree) {
	if t.job != 0 {
		windows.CloseHandle(t.job)
		t.job = 0
	}
}

// Ctrl-C cannot be delivered to a process without a console of its own.
func interruptTree(*exec.Cmd, *platformTree) error {
	return errors.New("interrupting a process is not supported on Windows; use kill_job")
}
