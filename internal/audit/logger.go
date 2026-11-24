package audit

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A nil *Logger discards everything.
type Logger struct {
	mu   sync.Mutex
	path string
	file *os.File
	log  *slog.Logger
}

func Open(path string) (*Logger, error) {
	l := &Logger{path: path}
	if err := l.reopen(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) reopen() error {
	var w io.Writer = os.Stderr
	var f *os.File
	if l.path != "" {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o750); err != nil {
			return err
		}
		var err error
		f, err = os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		w = f
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
	}
	l.file = f
	l.log = slog.New(slog.NewJSONHandler(w, nil))
	return nil
}

func (l *Logger) Reopen() error {
	if l == nil || l.path == "" {
		return nil
	}
	return l.reopen()
}

func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

type Entry struct {
	Tool     string
	Args     map[string]any
	Duration time.Duration
	OK       bool
	Err      string
	Session  string
	Client   string
}

func (l *Logger) Record(e Entry) {
	if l == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("event", "tool_call"),
		slog.String("tool", e.Tool),
		slog.Any("args_summary", e.Args),
		slog.Int64("duration_ms", e.Duration.Milliseconds()),
		slog.Bool("ok", e.OK),
	}
	if e.Client != "" {
		attrs = append(attrs, slog.String("client", e.Client))
	}
	if e.Session != "" {
		attrs = append(attrs, slog.String("session", e.Session))
	}
	if e.Err != "" {
		attrs = append(attrs, slog.String("error", e.Err))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log.LogAttrs(context.Background(), slog.LevelInfo, "tool_call", attrs...)
}
