//go:build windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
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

// newKillOnCloseJob creates a Job Object that kills all its processes when
// its last handle is closed. Descendants of a member join it too.
func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// attach puts a started process in a new Job Object. A grandchild spawned in
// the few microseconds between Start and the assignment would escape; os/exec
// cannot start a child suspended, so that window is accepted here (the
// pseudo-console path below starts suspended and has no such window).
func attach(p *os.Process) platformTree {
	job, err := newKillOnCloseJob()
	if err != nil {
		return platformTree{}
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
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

func killTree(p *os.Process, t *platformTree) error {
	if t.job != 0 {
		return windows.TerminateJobObject(t.job, 1)
	}
	return p.Kill()
}

func release(t *platformTree) {
	if t.job != 0 {
		windows.CloseHandle(t.job)
		t.job = 0
	}
}

// Ctrl-C cannot be delivered to a process without a console of its own.
// (In a pseudo-console it is typed as "\x03" instead.)
func interruptTree(*os.Process, *platformTree) error {
	return errors.New("interrupting a process is not supported on Windows; use kill_job")
}

// PseudoConsole is the controlling side of a Windows pseudo-console (ConPTY):
// keystrokes go to In, the rendered output comes from Out.
type PseudoConsole struct {
	In  *os.File
	Out *os.File
	hpc windows.Handle
}

// Close ends the pseudo-console. Out reports end of output afterwards, so
// keep reading it until then: on older Windows versions Close waits for the
// remaining output to be read.
func (c *PseudoConsole) Close() {
	if c.hpc != 0 {
		windows.ClosePseudoConsole(c.hpc)
		c.hpc = 0
	}
	c.In.Close()
}

// StartPseudoConsole starts path with the command line cmdLine in a new
// pseudo-console of cols x rows. The process starts suspended and joins a
// Job Object before it runs, so its whole tree can be killed.
func StartPseudoConsole(path, cmdLine, dir string, env []string, cols, rows int16) (*Tree, *PseudoConsole, error) {
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return nil, nil, err
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, nil, err
	}
	var hpc windows.Handle
	err := windows.CreatePseudoConsole(windows.Coord{X: cols, Y: rows}, inRead, outWrite, 0, &hpc)
	// The pseudo-console keeps its own copies of its ends of the pipes.
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)
	if err != nil {
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return nil, nil, err
	}
	pc := &PseudoConsole{In: os.NewFile(uintptr(inWrite), "conpty-in"), Out: os.NewFile(uintptr(outRead), "conpty-out"), hpc: hpc}
	t, err := startInConsole(path, cmdLine, dir, env, hpc)
	if err != nil {
		// Close the output first: on older Windows, closing the console
		// waits for its output to be read.
		pc.Out.Close()
		pc.Close()
		return nil, nil, err
	}
	return t, pc, nil
}

func startInConsole(path, cmdLine, dir string, env []string, hpc windows.Handle) (*Tree, error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	// The attribute value is the console handle itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		return nil, err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// Without this the child would inherit sh-mcp's own (redirected)
	// standard handles instead of using the pseudo-console.
	si.Flags = windows.STARTF_USESTDHANDLES

	app, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return nil, err
	}
	var wdir *uint16
	if dir != "" {
		if wdir, err = windows.UTF16PtrFromString(dir); err != nil {
			return nil, err
		}
	}
	block, err := envBlock(env)
	if err != nil {
		return nil, err
	}
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcess(app, line, nil, nil, false, flags, block, wdir, &si.StartupInfo, &pi); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(job)
		return nil, err
	}
	// While pi.Process is open the pid cannot be reused, so FindProcess gets
	// this very process.
	p, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		windows.CloseHandle(job) // kills the suspended process
		return nil, err
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return &Tree{p: p, plat: platformTree{job: job}}, nil
}

// envBlock builds a CreateProcess environment block: "k=v" strings, each
// NUL-terminated, ending with an extra NUL. Later duplicates win.
func envBlock(env []string) (*uint16, error) {
	if env == nil {
		return nil, nil // inherit sh-mcp's environment, like os/exec
	}
	seen := map[string]int{}
	var kept []string
	for _, kv := range env {
		k := kv
		if i := indexEnvSep(kv); i >= 0 {
			k = kv[:i]
		}
		key := windowsEnvKey(k)
		if i, ok := seen[key]; ok {
			kept[i] = kv
			continue
		}
		seen[key] = len(kept)
		kept = append(kept, kv)
	}
	// Windows expects the block sorted by name, case-insensitively.
	sort.SliceStable(kept, func(a, b int) bool { return envSortKey(kept[a]) < envSortKey(kept[b]) })
	var b []uint16
	for _, kv := range kept {
		s, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, err
		}
		b = append(b, s...) // includes the terminating NUL
	}
	b = append(b, 0)
	if len(b) == 1 {
		b = append(b, 0)
	}
	return &b[0], nil
}

// indexEnvSep finds the '=' ending a variable name; names such as "=C:" (a
// drive's current directory) start with '='.
func indexEnvSep(kv string) int {
	for i := 1; i < len(kv); i++ {
		if kv[i] == '=' {
			return i
		}
	}
	return -1
}

// windowsEnvKey folds a variable name the way Windows compares them.
func windowsEnvKey(k string) string { return strings.ToUpper(k) }

func envSortKey(kv string) string {
	if i := indexEnvSep(kv); i >= 0 {
		kv = kv[:i]
	}
	return windowsEnvKey(kv)
}
