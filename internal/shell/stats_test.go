// Copyright 2026 by moshix

package shell

import (
	"strings"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/nje/link"
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
	for _, want := range []string{"LINE 0 NODEB", "session uptime", "1m00s", "connects/failures", "3/1",
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
	if full[1].Segs[3].Color != BoldGreen || full[2].Segs[3].Color != Red {
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
	if l := s.Exec("cmd nodeb d t").Lines[0].Text; l != "command sent to NODEB; responses will appear in the messages pane" {
		t.Fatalf("%q", l)
	}
	be.local.Pending = 2
	if f := s.Render(24, 120); !strings.Contains(f.Rows[0].Text, "2 cmd pending") {
		t.Fatalf("title %q", f.Rows[0].Text)
	}
}

func TestLocalTimeoutAndStreamsOptions(t *testing.T) {
	s, be := newTestShell(t)
	s.set.ConfigPath = copyConfig(t)
	if r := s.Exec("local timeout 300 save"); be.local.Timeout != 300 || !strings.Contains(joinText(r.Lines), "saved") {
		t.Fatalf("%+v", r.Lines)
	}
	if c, _ := config.Load(s.set.ConfigPath); c.Timeout != 300 {
		t.Fatal("timeout not saved")
	}
	if r := s.Exec("local timeout -5"); r.Lines[0].Color != Red {
		t.Fatal("bad timeout accepted")
	}
	s.Exec("link define nodec 10.0.0.3 max-streams=3")
	if be.remotes["NODEC"].MaxStreams != 3 {
		t.Fatalf("%+v", be.remotes["NODEC"])
	}
	s.Exec("link change nodec max_streams=5")
	if be.remotes["NODEC"].MaxStreams != 5 {
		t.Fatal("max_streams change")
	}
	for _, bad := range []string{"link change nodec max_streams=9", "link change nodec eof_wait=5"} {
		if r := s.Exec(bad); r.Lines[0].Color != Red {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRouteCommands(t *testing.T) {
	s, be := newTestShell(t)
	s.set.ConfigPath = copyConfig(t)
	r := s.Exec("ROUTE MVSF TO MVSB ALT MVSC")
	if be.lastRoute != (config.Route{Dest: "MVSF", Via: "MVSB", Alt: "MVSC"}) || r.Lines[0].Text != "route to MVSF via MVSB (alt MVSC)" {
		t.Fatalf("%+v %+v", be.lastRoute, r.Lines)
	}
	s.Exec("route far to nodeb save")
	if c, err := config.Load(s.set.ConfigPath); err != nil || len(c.Routes) != 1 || c.Routes[0] != (config.Route{Dest: "FAR", Via: "NODEB"}) {
		t.Fatalf("saved routes %v", err)
	}
	s.Exec("route default nodeb save")
	if c, _ := config.Load(s.set.ConfigPath); c.DefaultRoute != "NODEB" {
		t.Fatal("default route not saved")
	}
	s.Exec("route default delete save")
	s.Exec("route delete far save")
	if c, _ := config.Load(s.set.ConfigPath); c.DefaultRoute != "" || len(c.Routes) != 0 {
		t.Fatal("route deletes not saved")
	}
	if !strings.Contains(be.calls[len(be.calls)-1], "route delete FAR") {
		t.Fatalf("%q", be.calls)
	}
	for _, bad := range []string{"route", "route x to", "route x via y", "route x to y alt", "route x to y or z", "route TOOLONGNAME to b"} {
		if r := s.Exec(bad); r.Lines[0].Color != Red {
			t.Errorf("%q accepted", bad)
		}
	}
	lines := s.Exec("route list").Lines
	list := joinText(lines)
	for _, want := range []string{"NODE", "PATH", "ALT", "STATE", "NODEB", "direct", "NODEC", "MVSF", "via MVSB", "MVSC",
		"default via MVSB"} {
		if !strings.Contains(list, want) {
			t.Fatalf("routes table lacks %q:\n%s", want, list)
		}
	}
	// One row per remote (2) and route (1), plus the default row.
	if len(lines) != 1+2+1+1 || !strings.HasPrefix(strings.TrimSpace(lines[4].Text), "*") {
		t.Fatalf("rows:\n%s", list)
	}
	if lines[1].Segs[5].Color != BoldGreen || lines[2].Segs[5].Color != Red {
		t.Fatalf("state colors %+v", lines[1].Segs)
	}
	if joinText(s.Exec("routes").Lines) != list {
		t.Fatal("routes alias")
	}
}

// TestLineReferences: wherever a node is accepted, "line <n>", "l<n>"
// or a bare number select the node with that LINE number.
func TestLineReferences(t *testing.T) {
	s, be := newTestShell(t)
	cases := []struct{ in, call string }{
		{"connect line 1", "connect NODEC"},
		{"drop l1", "drop NODEC"},
		{"drain 0", "drain NODEB"},
		{"connect nodec", "connect nodec"}, // names still work
		{"msg 1 bob  two   blanks", "msg NODEC|bob|two   blanks"},
		{"msg line 0 bob 7 dwarfs", "msg NODEB|bob|7 dwarfs"},
		{"cmd l0 q sys", "cmd NODEB|q sys"},
		{"send 1 bob f.txt", "send NODEC|bob|f.txt"},
		{"route far to line 0 alt l1", "route FAR,NODEB,NODEC"},
		{"stats reset 1", "reset NODEC"},
	}
	for _, c := range cases {
		be.calls = nil
		s.Exec(c.in)
		if len(be.calls) == 0 || be.calls[len(be.calls)-1] != c.call {
			t.Errorf("%q -> %q, want %q", c.in, be.calls, c.call)
		}
	}
	// A number that is no line stays as typed (and fails as a node name).
	be.calls = nil
	s.Exec("connect 7")
	if be.calls[0] != "connect 7" {
		t.Fatalf("%q", be.calls)
	}
	// Tables show the LINE column first; events name the line.
	if !strings.HasPrefix(s.Exec("nodes").Lines[0].Text, "LINE NODE") {
		t.Fatal("nodes header")
	}
	if !strings.HasPrefix(strings.TrimSpace(s.Exec("routes").Lines[0].Text), "LINE") {
		t.Fatal("routes header")
	}
	ev := link.Event{Kind: link.EvLink, Level: 2, Node: "HOUCICS", Line: 0, HasLine: true, Text: "link up"}
	if l := FormatEvent(ev)[0].Text; !strings.Contains(l, "LINE 0 HOUCICS link up") {
		t.Fatalf("%q", l)
	}
	// link define: line= and NAT options.
	s.Exec("link define nodex 1.2.3.4 line=7 natin=10.1.1.2 natout=35.192.26.118")
	if rc := be.remotes["NODEX"]; rc.Line != 7 || !rc.LineSet || rc.NatIn != "10.1.1.2" || rc.NatOut != "35.192.26.118" {
		t.Fatalf("%+v", rc)
	}
	if r := s.Exec("link change nodex natout=999.1.1.1"); r.Lines[0].Color != Red {
		t.Fatal("bad NAT address accepted")
	}
}
