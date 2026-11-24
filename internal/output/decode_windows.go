//go:build windows

package output

import (
	"golang.org/x/sys/windows"
)

func decodeSystemCodePage(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", true
	}
	cp := windows.GetACP()
	n, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return "", false
	}
	buf := make([]uint16, n)
	n, err = windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), &buf[0], n)
	if err != nil {
		return "", false
	}
	return windows.UTF16ToString(buf[:n]), true
}
