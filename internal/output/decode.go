package output

import (
	"strings"
	"unicode/utf8"
)

// Text converts child-process output to valid UTF-8. On Windows, output that
// is not valid UTF-8 is assumed to be in the system ANSI code page (e.g.
// CP932 on Japanese systems, as printed by cmd.exe and many native tools)
// and converted; elsewhere invalid bytes are replaced.
func Text(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if s, ok := decodeSystemCodePage(b); ok {
		return s
	}
	return strings.ToValidUTF8(string(b), "�")
}
