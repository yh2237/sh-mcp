package output

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// StripTerminal turns pseudo-terminal output into plain text, the way it would
// look on screen line by line. Within the current line it follows the cursor:
// text overwrites, and carriage return, backspace, cursor left/right/column,
// erase and insert/delete apply (so progress bars and REPLs that redraw their
// input line come out once). Other escape sequences (colors, titles, modes)
// are dropped. Programs that draw on the whole screen (vim, top) still do not
// come out readable; that would need a full screen emulator.
func StripTerminal(b []byte) []byte {
	var out []byte
	var line []rune
	col := 0
	put := func(r rune) {
		for len(line) < col {
			line = append(line, ' ')
		}
		if col < len(line) {
			line[col] = r
		} else {
			line = append(line, r)
		}
		col++
	}
	flush := func() {
		out = append(out, strings.TrimRight(string(line), " ")...)
		line, col = line[:0], 0
	}
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			next, final, params := parseEscape(b, i)
			line, col = applyCSI(line, col, final, params)
			i = next
			continue
		case c == '\r':
			col = 0
		case c == '\n':
			flush()
			out = append(out, '\n')
		case c == '\b':
			col = max(col-1, 0)
		case c == '\t':
			put('\t')
		case c < 0x20 || c == 0x7f:
			// other control characters (bell, ...) have no text
		default:
			r, size := utf8.DecodeRune(b[i:])
			put(r)
			i += size
			continue
		}
		i++
	}
	flush()
	return out
}

// applyCSI applies the cursor and erase sequences that affect a single line.
func applyCSI(line []rune, col int, final byte, params string) ([]rune, int) {
	n := 1
	if v, err := strconv.Atoi(strings.SplitN(params, ";", 2)[0]); err == nil && v > 0 {
		n = v
	}
	switch final {
	case 'D': // cursor left
		col = max(col-n, 0)
	case 'C': // cursor right
		col += n
	case 'G': // cursor to column
		col = n - 1
	case 'K': // erase in line
		switch params {
		case "", "0":
			if col < len(line) {
				line = line[:col]
			}
		case "1":
			for i := 0; i <= col && i < len(line); i++ {
				line[i] = ' '
			}
		case "2":
			line = line[:0]
		}
	case 'P': // delete characters
		if col < len(line) {
			line = append(line[:col], line[min(col+n, len(line)):]...)
		}
	case '@': // insert blanks
		if col < len(line) {
			line = append(line[:col], append([]rune(strings.Repeat(" ", n)), line[col:]...)...)
		}
	}
	return line, col
}

// parseEscape returns the index after the escape sequence starting at b[i]
// (ESC) and, for CSI sequences, the final byte and the parameters. An
// unfinished sequence at the end of b is dropped.
func parseEscape(b []byte, i int) (next int, final byte, params string) {
	if i+1 >= len(b) {
		return len(b), 0, ""
	}
	switch b[i+1] {
	case '[': // CSI: parameters, then a final byte 0x40-0x7e
		for j := i + 2; j < len(b); j++ {
			if b[j] >= 0x40 && b[j] <= 0x7e {
				p := string(b[i+2 : j])
				if p != "" && (p[0] < '0' || p[0] > ';') {
					return j + 1, 0, "" // private sequences (?25l, >c) do not move the cursor
				}
				return j + 1, b[j], p
			}
		}
		return len(b), 0, ""
	case ']', 'P', '^', '_': // OSC, DCS, PM, APC: until BEL or ESC \
		for j := i + 2; j < len(b); j++ {
			if b[j] == 0x07 {
				return j + 1, 0, ""
			}
			if b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\' {
				return j + 2, 0, ""
			}
		}
		return len(b), 0, ""
	case '(', ')', '*', '+', '#', '%': // character set and similar: one more byte
		return min(i+3, len(b)), 0, ""
	}
	return i + 2, 0, ""
}
