package output

import "testing"

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
	}
	for in, want := range cases {
		if got := string(StripTerminal([]byte(in))); got != want {
			t.Errorf("StripTerminal(%q) = %q, want %q", in, got, want)
		}
	}
}
