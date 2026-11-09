// Copyright 2026 by moshix

// Package logx is gonje's file logger. Every protocol event, raw TTB/TTR
// block and decoded record ends up here (see PLAN: "gonje.log always gets
// full L3 detail plus raw TTB/TTR block dumps").
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"gonje/internal/ebcdic"
)

// Level is a log severity.
type Level int

// Log levels, lowest first.
const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO "
	case Warn:
		return "WARN "
	}
	return "ERROR"
}

// ParseLevel converts debug/info/warn/error to a Level (default Debug).
func ParseLevel(s string) Level {
	switch strings.ToLower(s) {
	case "info":
		return Info
	case "warn":
		return Warn
	case "error":
		return Error
	}
	return Debug
}

// Logger writes timestamped lines to an io.Writer. It is safe for
// concurrent use. A nil *Logger discards everything.
type Logger struct {
	mu     sync.Mutex
	w      io.Writer
	c      io.Closer
	min    Level
	prefix string
	now    func() time.Time
}

// New creates a logger writing to w at minimum level min.
func New(w io.Writer, min Level) *Logger {
	return &Logger{w: w, min: min, now: time.Now}
}

// Open opens (appending) a log file. The log can contain host names and
// traffic, so it is created, and kept, readable by the owner only (0600).
func Open(path string, min Level) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Chmod(0o600) // tighten a log file created by an older version
	l := New(f, min)
	l.c = f
	return l, nil
}

// Discard returns a logger that writes nothing.
func Discard() *Logger { return New(io.Discard, Error+1) }

// Close closes the underlying file, if any.
func (l *Logger) Close() error {
	if l == nil || l.c == nil {
		return nil
	}
	return l.c.Close()
}

// With returns a logger sharing the same output with an added prefix,
// e.g. "[NODEB]".
func (l *Logger) With(prefix string) *Logger {
	if l == nil {
		return nil
	}
	return &Logger{w: &sharedWriter{l}, min: l.min, prefix: l.prefix + prefix + " ", now: l.now}
}

// sharedWriter forwards writes to the parent logger under its mutex so
// derived loggers never interleave partial lines.
type sharedWriter struct{ p *Logger }

func (s *sharedWriter) Write(b []byte) (int, error) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	return s.p.w.Write(b)
}

// Enabled reports whether lv would be written.
func (l *Logger) Enabled(lv Level) bool { return l != nil && lv >= l.min }

// Escape makes text safe for a single log line: control characters
// (CR, LF, ESC, C1 controls, ...) become \xNN. Remote nodes control much
// of the text we log (messages, job names), and EBCDIC X'25' decodes to
// LF, so without this a peer could forge log lines or send escape
// sequences to whoever runs tail on the log.
func Escape(s string) string {
	bad := func(r rune) bool { return r < 0x20 || (r >= 0x7F && r < 0xA0) }
	if strings.IndexFunc(s, bad) < 0 {
		return s
	}
	var sb strings.Builder
	for _, r := range s {
		if bad(r) {
			fmt.Fprintf(&sb, "\\x%02x", r)
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// Log writes one message at level lv as exactly one log line.
func (l *Logger) Log(lv Level, msg string) { l.Lines(lv, []string{msg}) }

// Lines writes several lines (e.g. a title and a hex dump) at level lv,
// each with its own timestamp, in one write so they stay together.
func (l *Logger) Lines(lv Level, lines []string) {
	if !l.Enabled(lv) {
		return
	}
	ts := l.now().Format("2006-01-02 15:04:05.000")
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(ts)
		sb.WriteByte(' ')
		sb.WriteString(lv.String())
		sb.WriteByte(' ')
		sb.WriteString(l.prefix)
		sb.WriteString(Escape(line))
		sb.WriteByte('\n')
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, sb.String())
}

// Debug logs msg at Debug. Callers format with fmt.Sprintf so that no
// interface-typed varargs appear in our own APIs.
func (l *Logger) Debug(msg string) { l.Log(Debug, msg) }

// Info logs msg at Info.
func (l *Logger) Info(msg string) { l.Log(Info, msg) }

// Warn logs msg at Warn.
func (l *Logger) Warn(msg string) { l.Log(Warn, msg) }

// Error logs msg at Error.
func (l *Logger) Error(msg string) { l.Log(Error, msg) }

// Dump logs a title line followed by a hex dump of data.
func (l *Logger) Dump(lv Level, title string, data []byte) {
	if !l.Enabled(lv) {
		return
	}
	l.Lines(lv, append([]string{fmt.Sprintf("%s (%d bytes)", title, len(data))}, indent(ebcdic.HexDump(data))...))
}

func indent(lines []string) []string {
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return lines
}
