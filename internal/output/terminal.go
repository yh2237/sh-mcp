package output

import (
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	// maxBlankLines caps the empty lines produced by a jump down the screen
	// (a cleared screen would otherwise become dozens of them).
	maxBlankLines = 2
	// maxLineCells bounds the line being edited: output without line breaks
	// is written out in pieces of this width, and cursor movement stops here.
	maxLineCells = 4096
	// maxEscape bounds an unfinished escape sequence kept for the next write.
	maxEscape = 4096
	tabWidth  = 8
)

// Terminal turns the output of a pseudo-terminal into plain text, the way it
// would look on screen line by line, as it arrives. Finished lines are
// written to the underlying writer; the line the cursor is on (often a
// prompt) stays editable and is available from Pending.
//
// Within the current line it follows the cursor in screen columns (wide
// characters such as Japanese take two, tabs move to the next stop): text
// overwrites, and carriage return, backspace, cursor left/right/column, erase
// and insert/delete apply (so progress bars and REPLs that redraw their input
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
	if len(b)-n <= maxEscape {
		t.rest = append([]byte(nil), b[n:]...)
	} // else: a runaway sequence, dropped
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
	return t.t.text()
}

// lineTerm emulates a terminal one line at a time: finished lines go to out,
// the current one can still be edited.
type lineTerm struct {
	out []byte
	// cells are the screen columns of the current line: one character each,
	// "" for the second column of a wide character.
	cells  []string
	col    int
	row    int // 1-based screen row of the current line
	blanks int // consecutive blank lines already emitted
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
			t.col = min((t.col/tabWidth+1)*tabWidth, maxLineCells-1)
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
	w := runeWidth(r)
	if w == 0 {
		// A combining mark or joiner belongs to the character before it.
		for i := min(t.col, len(t.cells)) - 1; i >= 0; i-- {
			if t.cells[i] != "" {
				t.cells[i] += string(r)
				break
			}
		}
		return
	}
	if t.col+w > maxLineCells {
		t.newline() // wrap a runaway line
	}
	for len(t.cells) < t.col+w {
		t.cells = append(t.cells, " ")
	}
	t.clearWide(t.col)
	if w == 2 {
		t.clearWide(t.col + 1)
	}
	t.cells[t.col] = string(r)
	if w == 2 {
		t.cells[t.col+1] = ""
	}
	t.col += w
}

// clearWide blanks the other half of a wide character before column i is
// overwritten, as a terminal does.
func (t *lineTerm) clearWide(i int) {
	if i >= len(t.cells) {
		return
	}
	if t.cells[i] == "" && i > 0 {
		t.cells[i-1] = " "
	} else if i+1 < len(t.cells) && t.cells[i+1] == "" {
		t.cells[i+1] = " "
	}
}

func (t *lineTerm) text() string {
	return strings.TrimRight(strings.Join(t.cells, ""), " ")
}

func (t *lineTerm) emitLine() {
	s := t.text()
	t.cells, t.col = t.cells[:0], 0
	if s == "" {
		if t.blanks >= maxBlankLines {
			return
		}
		t.blanks++
	} else {
		t.blanks = 0
		t.out = append(t.out, s...)
	}
	t.out = append(t.out, '\n')
}

func (t *lineTerm) newline() {
	t.emitLine()
	t.row++
}

// moveTo puts the cursor at row (1-based) and column col (0-based). Going
// down ends the current line; going up (a redraw of earlier lines) starts a
// fresh one, as those lines were already written.
func (t *lineTerm) moveTo(row, col int) {
	switch {
	case row > t.row:
		for i := 0; i < min(row-t.row, maxBlankLines+1); i++ {
			t.emitLine()
		}
	case row < t.row && len(t.cells) > 0:
		t.emitLine()
	}
	t.row, t.col = row, clampCol(col)
}

func clampCol(col int) int { return min(max(col, 0), maxLineCells-1) }

// csi applies the cursor and erase sequences that affect the text.
func (t *lineTerm) csi(final byte, params string) {
	fields := strings.Split(params, ";")
	arg := func(i int) int {
		if i < len(fields) {
			if v, err := strconv.Atoi(fields[i]); err == nil && v > 0 {
				return min(v, maxLineCells)
			}
		}
		return 1
	}
	n := arg(0)
	switch final {
	case 'D': // cursor left
		t.col = max(t.col-n, 0)
	case 'C': // cursor right
		t.col = clampCol(t.col + n)
	case 'G': // cursor to column
		t.col = clampCol(n - 1)
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
			if t.col < len(t.cells) {
				t.clearWide(t.col)
				t.cells = t.cells[:t.col]
			}
		case "1":
			for i := 0; i <= t.col && i < len(t.cells); i++ {
				t.cells[i] = " "
			}
			t.clearWide(t.col + 1)
		case "2":
			t.cells = t.cells[:0]
		}
	case 'X': // erase characters
		for i := t.col; i < t.col+n && i < len(t.cells); i++ {
			t.clearWide(i)
			t.cells[i] = " "
		}
	case 'P': // delete characters
		if t.col < len(t.cells) {
			t.clearWide(t.col)
			t.cells = append(t.cells[:t.col], t.cells[min(t.col+n, len(t.cells)):]...)
		}
	case '@': // insert blanks
		if t.col < len(t.cells) {
			t.clearWide(t.col)
			blank := make([]string, n)
			for i := range blank {
				blank[i] = " "
			}
			t.cells = append(t.cells[:t.col], append(blank, t.cells[t.col:]...)...)
			if len(t.cells) > maxLineCells {
				t.cells = t.cells[:maxLineCells]
			}
		}
	}
}

// runeWidth is the number of screen columns r takes: 2 for East Asian wide
// characters and most emoji, 0 for combining marks and format characters.
func runeWidth(r rune) int {
	switch {
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case r >= 0x1100 && r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0x303e, // CJK radicals, punctuation
		r >= 0x3041 && r <= 0x33ff, // kana, CJK compatibility
		r >= 0x3400 && r <= 0x4dbf, // CJK extension A
		r >= 0x4e00 && r <= 0x9fff, // CJK ideographs
		r >= 0xa000 && r <= 0xa4cf, // Yi
		r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
		r >= 0xf900 && r <= 0xfaff, // CJK compatibility ideographs
		r >= 0xfe30 && r <= 0xfe4f, // CJK compatibility forms
		r >= 0xff00 && r <= 0xff60, // full-width forms
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f, // emoji
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd: // CJK extensions B and later
		return 2
	}
	return 1
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
