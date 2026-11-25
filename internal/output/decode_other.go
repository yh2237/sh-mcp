//go:build !windows

package output

func decodeSystemCodePage([]byte) (string, bool) { return "", false }
