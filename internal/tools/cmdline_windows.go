//go:build windows

package tools

import "syscall"

func setCmdLine(attr *syscall.SysProcAttr, cmdline string) { attr.CmdLine = cmdline }
