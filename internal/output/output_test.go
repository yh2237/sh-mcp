package output

import (
	"strings"
	"testing"
)

func TestTruncateMiddle(t *testing.T) {
	if got := string(TruncateMiddle([]byte("short"), 100)); got != "short" {
		t.Fatalf("short: %q", got)
	}
	in := "HEAD" + strings.Repeat("x", 1000) + "TAIL"
	got := string(TruncateMiddle([]byte(in), 100))
	if !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") || !strings.Contains(got, "bytes omitted") {
		t.Fatalf("got %q", got)
	}
	jp := []byte(strings.Repeat("あ", 200))
	if s := string(TruncateMiddle(jp, 100)); !strings.HasPrefix(s, "あ") || !strings.HasSuffix(s, "あ") || strings.ContainsRune(s, '�') {
		t.Fatalf("split a rune: %q", s)
	}
}

func TestRing(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("hello "))
	data, next, dropped := r.ReadFrom(0, 100)
	if string(data) != "hello " || next != 6 || dropped != 0 {
		t.Fatalf("%q %d %d", data, next, dropped)
	}
	r.Write([]byte("world!"))
	data, next, dropped = r.ReadFrom(6, 100)
	if string(data) != "world!" || next != 12 || dropped != 0 {
		t.Fatalf("%q %d %d", data, next, dropped)
	}
	data, _, dropped = r.ReadFrom(0, 100)
	if string(data) != "o world!" || dropped != 4 {
		t.Fatalf("%q %d", data, dropped)
	}
}
