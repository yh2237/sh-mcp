package output

import (
	"bytes"
	"testing"
)

// strip converts a complete piece of terminal output at once.
func strip(b []byte) string {
	var out bytes.Buffer
	term := NewTerminal(&out)
	term.Write(b)
	return out.String() + term.Pending()
}

func TestStripTerminal(t *testing.T) {
	cases := map[string]string{
		"plain\r\nline\r\n":                         "plain\nline\n",
		"\x1b[1;32mgreen\x1b[0m text":               "green text",
		"\x1b]0;window title\x07$ ls":               "$ ls",
		"\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\":  "link",
		"progress 10%\rprogress 50%\rprogress 100%": "progress 100%",
		"abc\b\bXY\n":                               "aXY\n",
		"typo\b \b\b \bo!":                          "tyo!",
		"\x1b(Bbell\x07 and \x1b=keypad":            "bell and keypad",
		"cut at the end \x1b[31":                    "cut at the end",
		"loading...\r\x1b[Kdone\n":                  "done\n",
		"abcdef\x1b[3D\x1b[KXY":                     "abcXY",
		"abc\x1b[1GZ":                               "Zbc",
		"abcd\x1b[3D\x1b[2P":                        "ad",
		// Python 3.13's REPL redraws the whole input line on every keystroke.
		"\x1b[?25l\x1b[4D\x1b[1;35m>>> \x1b[0m[\x1b[5D\x1b[?12l\x1b[?25h\x1b[5C" +
			"\x1b[?25l\x1b[5D\x1b[1;35m>>> \x1b[0m[1\x1b[6D\x1b[?12l\x1b[?25h\x1b[6C" +
			"\x1b[?25l\x1b[6D\x1b[1;35m>>> \x1b[0m[1]\x1b[7D\x1b[?12l\x1b[?25h\x1b[7C\x1b[7D\n\r[1]\r\n": ">>> [1]\n[1]\n",
		// Windows' pseudo-console skips lines by moving the cursor down.
		"\x1b[2J\x1b[m\x1b[HMicrosoft Windows\x1b]0;cmd.exe\a\r\n(c) Microsoft.\x1b[4;1HC:\\>": "Microsoft Windows\n(c) Microsoft.\n\nC:\\>",
		"top\x1b[30;1Hbottom":                     "top\n\n\nbottom",
		"abc\x1b[1;5HX":                           "abc X",
		"line one\r\nline two\x1b[1A\x1b[1Gagain": "line one\nline two\nagain",
	}
	for in, want := range cases {
		if got := strip([]byte(in)); got != want {
			t.Errorf("strip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTerminalAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	term := NewTerminal(&out)
	// Rows are tracked across writes, and sequences and UTF-8 characters can
	// be split between them.
	for _, chunk := range []string{
		"\x1b[HMicrosoft\r\n(c)\x1b[4;1HC:\\>",
		"echo hi\r\n\x1b[?2", "5lhi\x1b[7;1HC:\\>",
		"日本\xe8", "\xaa\x9e",
	} {
		term.Write([]byte(chunk))
	}
	if got, want := out.String(), "Microsoft\n(c)\n\nC:\\>echo hi\nhi\n\n"; got != want {
		t.Errorf("finished lines = %q, want %q", got, want)
	}
	if got, want := term.Pending(), "C:\\>日本語"; got != want {
		t.Errorf("pending line = %q, want %q", got, want)
	}
}
