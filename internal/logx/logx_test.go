// Copyright 2026 by moshix

package logx

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixed() time.Time { return time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC) }

func TestLevelsAndFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Info)
	l.now = fixed
	l.Debug("hidden")
	l.Info("hello")
	l.With("[NODEB]").Warn("two\nlines\x1b[2J")
	l.Lines(Info, []string{"a", "b"})
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Fatal("debug not filtered")
	}
	want := "2026-11-09 08:00:00.000 INFO  hello\n" +
		"2026-11-09 08:00:00.000 WARN  [NODEB] two\\x0alines\\x1b[2J\n" +
		"2026-11-09 08:00:00.000 INFO  a\n" +
		"2026-11-09 08:00:00.000 INFO  b\n"
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

func TestDump(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Debug)
	l.now = fixed
	l.Dump(Debug, "block", []byte{0xE3, 0xC5, 0xE2, 0xE3})
	if !strings.Contains(buf.String(), "block (4 bytes)") ||
		!strings.Contains(buf.String(), "0000  E3 C5 E2 E3") ||
		!strings.Contains(buf.String(), "TEST") {
		t.Fatalf("dump: %s", buf.String())
	}
}

func TestNilAndParse(t *testing.T) {
	var l *Logger
	l.Info("x") // must not panic
	if l.With("x") != nil || l.Close() != nil {
		t.Fatal("nil logger")
	}
	if ParseLevel("warn") != Warn || ParseLevel("zzz") != Debug || ParseLevel("ERROR") != Error {
		t.Fatal("ParseLevel")
	}
	Discard().Error("nothing")
}

func TestOpenMode0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gonje.log")
	os.WriteFile(p, nil, 0o644) // pre-existing, too open
	l, err := Open(p, Debug)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("x")
	l.Close()
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
}

func TestEscape(t *testing.T) {
	if Escape("plain text") != "plain text" || Escape("a\rb\u0085") != "a\\x0db\\x85" {
		t.Fatalf("%q", Escape("a\rb\u0085"))
	}
}
