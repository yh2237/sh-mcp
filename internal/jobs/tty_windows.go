//go:build windows

package jobs

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/yh2237/sh-mcp/internal/proc"
)

// startInTerminal starts cmd in a new pseudo-console (ConPTY). os/exec cannot
// attach one, so the process is created by proc directly; only cmd's path,
// arguments, directory and environment are used.
func startInTerminal(cmd *exec.Cmd) (*proc.Tree, func() (*os.ProcessState, error), *terminal, error) {
	line := windows.ComposeCommandLine(cmd.Args)
	tree, pc, err := proc.StartPseudoConsole(cmd.Path, line, cmd.Dir, cmd.Env, ttyCols, ttyRows)
	if err != nil {
		return nil, nil, nil, err
	}
	p := tree.Process()
	return tree, p.Wait, &terminal{in: enterKeys{pc.In}, out: pc.Out, finish: func(copied <-chan struct{}) {
		// The output pipe only ends once the pseudo-console is closed; it
		// renders what is left first, so keep reading until then.
		pc.Close()
		select {
		case <-copied:
		case <-time.After(2 * time.Second):
		}
		pc.Out.Close()
	}}, nil
}

// enterKeys types line ends as the Enter key, which a console reads as "\r"
// (a Unix terminal accepts "\n"), so "\n" in write_stdin works on both.
type enterKeys struct{ w io.Writer }

func (e enterKeys) Write(p []byte) (int, error) {
	s := strings.ReplaceAll(strings.ReplaceAll(string(p), "\r\n", "\r"), "\n", "\r")
	if _, err := io.WriteString(e.w, s); err != nil {
		return 0, err
	}
	return len(p), nil
}
