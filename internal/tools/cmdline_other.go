//go:build !windows

package tools

import "syscall"

func setCmdLine(*syscall.SysProcAttr, string) {}
