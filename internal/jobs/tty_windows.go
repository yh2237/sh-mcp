//go:build windows

package jobs

import (
	"errors"
	"os"
	"os/exec"
)

func attachTTY(*exec.Cmd) (*os.File, func(), error) {
	return nil, nil, errors.New("tty is not supported on Windows yet")
}
