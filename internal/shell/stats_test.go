package shell

import (
	"strings"
	"testing"
	"time"

	"gonje/internal/node"
)

func TestFormatHelpers(t *testing.T) {
	bytesCases := map[int64]string{0: "0 B", 512: "512 B", 1229: "1.2 KB", 3 << 20: "3.0 MB", 5 << 30: "5.0 GB"}
	for n, want := range bytesCases {
		if got := fmtBytes(n); got != want {
			t.Errorf("fmtBytes(%d) = %q want %q", n, got, want)
		}
	}
	durCases := map[time.Duration]string{42 * time.Second: "42s", 192 * time.Second: "3m12s",
		2*time.Hour + 5*time.Minute: "2h05m", 76 * time.Hour: "3d04h"}
	for d, want := range durCases {
		if got := fmtDur(d); got != want {
			t.Errorf("fmtDur(%v) = %q want %q", d, got, want)
		}
	}
}

func TestStatsCommand(t *testing.T) {
	s, be := newTestShell(t)
	text := joinText(s.Exec("stats").Lines)
	for _, want := range []string{"gonje 9.9", "up 3m12s", "messages in/out", "4/5", "commands in/out", "1/0",
		"responses sent", "files in/out", "1/0 (2.0 KB / 0 B)", "bytes in/out", "1.3 KB / 900 B",
		"errors", "connects", "3 (1 failed)", "2 accepted, 1 rejected", "NODE", "STATE", "NODEB", "NODEC"} {
		if !strings.Contains(text, want) {
			t.Errorf("stats lacks %q:\n%s", want, text)
		}
	}
	one := joinText(s.Exec("stats nodeb").Lines)
	for _, want := range []string{"link NODEB", "session uptime", "1m00s", "connects/failures", "3/1",
		"null buffers in/out", "77/0", "BCB seq errors", "NAKs received", "idle reply delay", "responses in/out", "0/2"} {
		if !strings.Contains(one, want) {
			t.Errorf("stats NODEB lacks %q:\n%s", want, one)
		}
	}
	if !strings.Contains(joinText(s.Exec("stats nodec").Lines), "DOWN (held)") {
		t.Fatal("held state")
	}
	if l := s.Exec("stats nosuch").Lines; l[0].Color != Red {
		t.Fatal("unknown node")
	}
	s.Exec("stats reset")
	s.Exec("stats reset nodeb")
	if be.calls[len(be.calls)-2] != "reset " || be.calls[len(be.calls)-1] != "reset nodeb" {
		t.Fatalf("reset calls %q", be.calls)
	}
}

// TestStatsTableWidths: the table drops low-priority columns to fit and
// keeps NODE and STATE; with no limit every column is there.
func TestStatsTableWidths(t *testing.T) {
	s, be := newTestShell(t)
	c := be.Counters()
	full := s.statsTable(c, 0)
	for _, h := range []string{"SESSION", "MSG i/o", "CMD i/o", "FILES i/o", "BYTES in/out", "NULL i/o", "CONN", "ERR", "LAST"} {
		if !strings.Contains(full[0].Text, h) {
			t.Errorf("full table lacks %s", h)
		}
	}
	prev := 1000
	for _, w := range []int{120, 90, 70, 50, 30, 20} {
		tab := s.statsTable(c, w)
		cols := len(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(tab[0].Text, " i/o", "_io"), " in/out", "_io")))
		if cols > prev {
			t.Fatalf("width %d: more columns than a wider table", w)
		}
		prev = cols
		for _, l := range tab {
			if Width(l.Text) > w && cols > 2 {
				t.Fatalf("width %d: %q is %d wide", w, l.Text, Width(l.Text))
			}
		}
		if !strings.Contains(tab[0].Text, "NODE") || !strings.Contains(tab[0].Text, "STATE") {
			t.Fatalf("width %d dropped NODE/STATE", w)
		}
		if w == 50 && strings.Contains(tab[0].Text, "LAST") {
			t.Fatal("LAST should go first")
		}
	}
	// State colors as in nodes.
	if full[1].Segs[2].Color != BoldGreen || full[2].Segs[2].Color != Red {
		t.Fatalf("state colors %+v", full[1].Segs)
	}
}

func TestDrainCommand(t *testing.T) {
	s, be := newTestShell(t)
	res := s.Exec("drain nodeb")
	if be.calls[len(be.calls)-1] != "drain nodeb" || !strings.Contains(res.Lines[0].Text, "draining: waiting for 1 inbound") ||
		res.Lines[0].Color != Yellow {
		t.Fatalf("%+v", res.Lines)
	}
	if res := s.Exec("drain"); res.Lines[0].Color != Red {
		t.Fatal("usage")
	}
	be.local.Draining = 1
	if f := s.Render(24, 100); !strings.Contains(f.Rows[0].Text, "1 draining") {
		t.Fatalf("title %q", f.Rows[0].Text)
	}
	if stateColor(node.Draining) != Yellow {
		t.Fatal("DRAINING not yellow")
	}
}

func TestCmdHintAndPendingTitle(t *testing.T) {
	s, be := newTestShell(t)
	if l := s.Exec("cmd nodeb d t").Lines[0].Text; l != "command sent to NODEB; responses will appear in the events pane" {
		t.Fatalf("%q", l)
	}
	be.local.Pending = 2
	if f := s.Render(24, 120); !strings.Contains(f.Rows[0].Text, "2 cmd pending") {
		t.Fatalf("title %q", f.Rows[0].Text)
	}
}
