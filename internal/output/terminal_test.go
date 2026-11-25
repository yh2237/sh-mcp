package output

import (
	"bytes"
	"strings"
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
		"top\x1b[30;1Hbottom": "top\n\n\nbottom",
		"top\x1b[2;1H\x1b[3;1H\x1b[4;1H\x1b[5;1H\x1b[6;1Hbottom": "top\n\n\nbottom",
		"a\r\n\r\n\r\n\r\n\r\nb":                                 "a\n\n\nb",
		"abc\x1b[1;5HX":                                          "abc X",
		"line one\r\nline two\x1b[1A\x1b[1Gagain":                "line one\nline two\nagain",
	}
	for in, want := range cases {
		if got := strip([]byte(in)); got != want {
			t.Errorf("strip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTerminalColumns(t *testing.T) {
	cases := map[string]string{
		// Wide characters take two columns; the cursor moves by columns.
		"日本\x1b[2Dx":  "日x",
		"日本語\b\b!!":   "日本!!",
		"日本語\b!":      "日本 !",
		"日本\rab":      "ab本",      // a blanks all of 日, b fills its second column
		"e\u0301t\bE": "e\u0301E", // a combining accent stays on its letter
		// Tabs move to the next stop.
		"a\tb":        "a       b",
		"abcdefgh\tc": "abcdefgh        c",
		// Cursor movement is bounded.
		"\x1b[999999Cx": strings.Repeat(" ", maxLineCells-1) + "x",
	}
	for in, want := range cases {
		if got := strip([]byte(in)); got != want {
			t.Errorf("strip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTerminalBounded(t *testing.T) {
	var out bytes.Buffer
	term := NewTerminal(&out)
	// Output without line breaks is written out in pieces.
	term.Write(bytes.Repeat([]byte("x"), 3*maxLineCells+5))
	if got := len(term.Pending()); got > maxLineCells {
		t.Errorf("pending line grew to %d cells", got)
	}
	if strings.Count(out.String(), "\n") != 3 {
		t.Errorf("expected 3 wrapped lines, got %q...", out.String()[:20])
	}
	// A runaway unfinished escape sequence is dropped.
	term = NewTerminal(&out)
	term.Write(append([]byte("\x1b]"), bytes.Repeat([]byte("a"), 2*maxEscape)...))
	term.Write([]byte("\x1b[2K\rok"))
	if got := term.Pending(); !strings.HasSuffix(got, "ok") {
		t.Errorf("after a runaway sequence: %q", got)
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
