package output

import (
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// maxBlankLines caps the empty lines produced by a jump down the screen (a
// cleared screen would otherwise become dozens of them).
const maxBlankLines = 2

// Terminal turns the output of a pseudo-terminal into plain text, the way it
// would look on screen line by line, as it arrives. Finished lines are
// written to the underlying writer; the line the cursor is on (often a
// prompt) stays editable and is available from Pending.
//
// Within the current line it follows the cursor: text overwrites, and
// carriage return, backspace, cursor left/right/column, erase and
// insert/delete apply (so progress bars and REPLs that redraw their input
// line come out once). Moving the cursor down to another row (how Windows'
// pseudo-console skips lines) becomes line breaks. Other escape sequences
// (colors, titles, modes) are dropped. Programs that draw on the whole screen
// (vim, top) still do not come out readable; that would need a full screen
// emulator.
type Terminal struct {
	mu   sync.Mutex
	w    io.Writer
	t    lineTerm
	rest []byte // an escape sequence or character cut off at the end of a write
}

func NewTerminal(w io.Writer) *Terminal {
	return &Terminal{w: w, t: lineTerm{row: 1}}
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := append(t.rest, p...)
	t.rest = nil
	n := t.t.feed(b)
	t.rest = append([]byte(nil), b[n:]...)
	if len(t.t.out) > 0 {
		_, err := t.w.Write(t.t.out)
		t.t.out = t.t.out[:0]
		if err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Pending is the current, unfinished line.
func (t *Terminal) Pending() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimRight(string(t.t.line), " ")
}

// lineTerm emulates a terminal one line at a time: finished lines go to out,
// the current one can still be edited.
type lineTerm struct {
	out  []byte
	line []rune
	col  int
	row  int // 1-based screen row of the current line
}

// feed processes b and returns how many bytes were used; the rest is an
// incomplete escape sequence or UTF-8 character to be completed by the next
// write.
func (t *lineTerm) feed(b []byte) int {
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			next, final, params, ok := parseEscape(b, i)
			if !ok {
				return i
			}
			t.csi(final, params)
			i = next
			continue
		case c == '\r':
			t.col = 0
		case c == '\n':
			t.newline()
		case c == '\b':
			t.col = max(t.col-1, 0)
		case c == '\t':
			t.put('\t')
		case c < 0x20 || c == 0x7f:
			// other control characters (bell, ...) have no text
		default:
			if !utf8.FullRune(b[i:]) {
				return i
			}
			r, size := utf8.DecodeRune(b[i:])
			t.put(r)
			i += size
			continue
		}
		i++
	}
	return len(b)
}

func (t *lineTerm) put(r rune) {
	for len(t.line) < t.col {
		t.line = append(t.line, ' ')
	}
	if t.col < len(t.line) {
		t.line[t.col] = r
	} else {
		t.line = append(t.line, r)
	}
	t.col++
}

func (t *lineTerm) flush() {
	t.out = append(t.out, strings.TrimRight(string(t.line), " ")...)
	t.line, t.col = t.line[:0], 0
}

func (t *lineTerm) newline() {
	t.flush()
	t.out = append(t.out, '\n')
	t.row++
}

// moveTo puts the cursor at row (1-based) and column col (0-based). Going
// down ends the current line; going up (a redraw of earlier lines) starts a
// fresh one, as those lines were already written.
func (t *lineTerm) moveTo(row, col int) {
	switch {
	case row > t.row:
		for i := 0; i < min(row-t.row, maxBlankLines+1); i++ {
			t.flush()
			t.out = append(t.out, '\n')
		}
	case row < t.row && len(t.line) > 0:
		t.flush()
		t.out = append(t.out, '\n')
	}
	t.row, t.col = row, max(col, 0)
}

// csi applies the cursor and erase sequences that affect the text.
func (t *lineTerm) csi(final byte, params string) {
	fields := strings.Split(params, ";")
	arg := func(i int) int {
		if i < len(fields) {
			if v, err := strconv.Atoi(fields[i]); err == nil && v > 0 {
				return v
			}
		}
		return 1
	}
	n := arg(0)
	switch final {
	case 'D': // cursor left
		t.col = max(t.col-n, 0)
	case 'C': // cursor right
		t.col += n
	case 'G': // cursor to column
		t.col = n - 1
	case 'H', 'f': // cursor to row;column
		t.moveTo(arg(0), arg(1)-1)
	case 'B', 'E': // cursor down (E also to column 1)
		col := t.col
		if final == 'E' {
			col = 0
		}
		t.moveTo(t.row+n, col)
	case 'A', 'F': // cursor up (F also to column 1)
		col := t.col
		if final == 'F' {
			col = 0
		}
		t.moveTo(max(t.row-n, 1), col)
	case 'K': // erase in line
		switch params {
		case "", "0":
			if t.col < len(t.line) {
				t.line = t.line[:t.col]
			}
		case "1":
			for i := 0; i <= t.col && i < len(t.line); i++ {
				t.line[i] = ' '
			}
		case "2":
			t.line = t.line[:0]
		}
	case 'X': // erase characters
		for i := t.col; i < t.col+n && i < len(t.line); i++ {
			t.line[i] = ' '
		}
	case 'P': // delete characters
		if t.col < len(t.line) {
			t.line = append(t.line[:t.col], t.line[min(t.col+n, len(t.line)):]...)
		}
	case '@': // insert blanks
		if t.col < len(t.line) {
			t.line = append(t.line[:t.col], append([]rune(strings.Repeat(" ", n)), t.line[t.col:]...)...)
		}
	}
}

// parseEscape parses the escape sequence starting at b[i] (ESC). It returns
// the index after it and, for CSI sequences, the final byte and the
// parameters; ok is false if b ends before the sequence does.
func parseEscape(b []byte, i int) (next int, final byte, params string, ok bool) {
	if i+1 >= len(b) {
		return 0, 0, "", false
	}
	switch b[i+1] {
	case '[': // CSI: parameters, then a final byte 0x40-0x7e
		for j := i + 2; j < len(b); j++ {
			if b[j] >= 0x40 && b[j] <= 0x7e {
				p := string(b[i+2 : j])
				if p != "" && (p[0] < '0' || p[0] > ';') {
					return j + 1, 0, "", true // private sequences (?25l, >c) do not move the cursor
				}
				return j + 1, b[j], p, true
			}
		}
		return 0, 0, "", false
	case ']', 'P', '^', '_': // OSC, DCS, PM, APC: until BEL or ESC \
		for j := i + 2; j < len(b); j++ {
			if b[j] == 0x07 {
				return j + 1, 0, "", true
			}
			if b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\' {
				return j + 2, 0, "", true
			}
		}
		return 0, 0, "", false
	case '(', ')', '*', '+', '#', '%': // character set and similar: one more byte
		if i+2 >= len(b) {
			return 0, 0, "", false
		}
		return i + 3, 0, "", true
	}
	return i + 2, 0, "", true
}
