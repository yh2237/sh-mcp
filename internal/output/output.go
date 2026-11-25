package output

import (
	"fmt"
	"sync"
	"unicode/utf8"
)

// Ring keeps the most recent max bytes of a stream, addressed by absolute
// offsets so readers can poll for new output incrementally.
type Ring struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	total   int64
	changed chan struct{}
	closed  bool
}

func NewRing(max int) *Ring {
	return &Ring{max: max, changed: make(chan struct{})}
}

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total += int64(len(p))
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - r.max; over > 0 {
		r.buf = append(r.buf[:0], r.buf[over:]...)
	}
	r.notifyLocked()
	return len(p), nil
}

// Poke wakes waiters without adding output, for activity that has not become
// text yet (a terminal redrawing its current line).
func (r *Ring) Poke() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifyLocked()
}

func (r *Ring) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.notifyLocked()
}

func (r *Ring) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// If offset points before the retained window, the gap is reported as dropped.
func (r *Ring) ReadFrom(offset int64, maxBytes int) (data []byte, next, dropped int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := r.total - int64(len(r.buf))
	if offset < start {
		dropped = start - offset
		offset = start
	}
	if offset > r.total {
		offset = r.total
	}
	i := int(offset - start)
	j := min(len(r.buf), i+maxBytes)
	return append([]byte(nil), r.buf[i:j]...), offset + int64(j-i), dropped
}

func (r *Ring) Total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// Changed returns a channel closed on the next write or Close, and the
// current total, so callers can wait for output beyond an offset.
func (r *Ring) Changed() (<-chan struct{}, int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changed, r.total, r.closed
}

// TruncateMiddle keeps the beginning and the end of b within max bytes,
// because compilers and test runners print the actual error last.
func TruncateMiddle(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	head := runeBoundary(b, max/4)
	tail := len(b) - (max - head)
	for tail < len(b) && !utf8.RuneStart(b[tail]) {
		tail++
	}
	out := append([]byte(nil), b[:head]...)
	out = append(out, fmt.Sprintf("\n…[%d bytes omitted]…\n", tail-head)...)
	return append(out, b[tail:]...)
}

func runeBoundary(b []byte, n int) int {
	for n > 0 && n < len(b) && !utf8.RuneStart(b[n]) {
		n--
	}
	return n
}
