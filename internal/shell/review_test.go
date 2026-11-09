// Copyright 2026 by moshix

package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
)

// TestLinkChangeAllOptions: turn=, max_streams= and buffersize= reach
// the backend and the saved file (second review M4).
func TestLinkChangeAllOptions(t *testing.T) {
	s, be := newTestShell(t)
	s.set.ConfigPath = copyConfig(t)
	s.Exec("link define nodec 10.0.0.3 save")
	res := s.Exec("link change nodec max_streams=4 turn=strict buffersize=1012 save")
	if !strings.Contains(joinText(res.Lines), "saved") {
		t.Fatalf("%+v", res.Lines)
	}
	if rc := be.remotes["NODEC"]; rc.Turn != "strict" || rc.MaxStreams != 4 || rc.BufSize != 1012 {
		t.Fatalf("runtime %+v", rc)
	}
	c, err := config.Load(s.set.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Remote("NODEC"); r == nil || r.Turn != "strict" || r.MaxStreams != 4 || r.BufSize != 1012 {
		t.Fatalf("saved %+v", r)
	}
}

// TestPasswordsMasked: a password typed on the command line never
// reaches the log, the history (memory and file) or the echoed command
// (second review M5).
func TestPasswordsMasked(t *testing.T) {
	s, _ := newTestShell(t)
	var buf strings.Builder
	s.set.Log = logx.New(&buf, logx.Debug)
	line := "link define nodeq 10.0.0.9 password=SEKRET"
	for _, r := range line {
		s.editor.Key(Key{Code: KeyRune, Rune: r})
	}
	a := s.editor.Key(Key{Code: KeyEnter})
	if a.Added {
		AppendHistory(s.set.HistoryPath, a.Line)
	}
	s.submit(a.Line)
	s.Exec("link change nodeq Remote_Password=OTHER")
	hist, _ := os.ReadFile(s.set.HistoryPath)
	for name, text := range map[string]string{"log": buf.String(), "history file": string(hist),
		"history": strings.Join(s.editor.History(), "\n"), "results": joinText(s.results.Visible(1000, 200))} {
		if strings.Contains(text, "SEKRET") || strings.Contains(text, "OTHER") {
			t.Fatalf("%s holds a password:\n%s", name, text)
		}
	}
	if !strings.Contains(buf.String(), "password=***") {
		t.Fatalf("log:\n%s", buf.String())
	}
	for in, want := range map[string]string{
		"link define x h password=abc save":    "link define x h password=*** save",
		"link change x PASSWORD=abc,port=1":    "link change x PASSWORD=***,port=1",
		"remote-password=abc":                  "remote-password=***",
		"msg nodeb bob my password=abc is bad": "msg nodeb bob my password=*** is bad",
		"mypassword=abc":                       "mypassword=abc",
	} {
		if got := maskSecrets(in); got != want {
			t.Errorf("maskSecrets(%q) = %q", in, got)
		}
	}
}

// TestPagerArrowKeys: an arrow key while paging is one key (Down = next
// page), never an Esc that quits plus "[B" typed into the command line
// (second review m6).
func TestPagerArrowKeys(t *testing.T) {
	s, _ := newTestShell(t)
	s.Render(24, 100)
	s.showResult(numbered(200), 1)
	var dec KeyDecoder
	feed := func(b ...byte) {
		for _, c := range b {
			for _, k := range dec.Feed(c) {
				if !s.PagerInput(k) {
					s.editor.Key(k)
				}
			}
		}
	}
	before := s.pg.next
	feed(0x1b, '[', 'B') // Down
	if !s.Paging() || s.pg.next == before || s.editor.Line() != "" {
		t.Fatalf("Down: paging %v next %d line %q", s.Paging(), s.pg.next, s.editor.Line())
	}
	feed(0x1b, '[', '6', '~') // PgDn
	feed(0x1b)                // a lone Esc: held until the read ends
	if !dec.LoneEscape() {
		t.Fatal("lone Esc not pending")
	}
	s.PagerInput(Key{Code: KeyEscape})
	if s.Paging() || s.editor.Line() != "" {
		t.Fatalf("Esc: paging %v line %q", s.Paging(), s.editor.Line())
	}
}

// TestMessagesNotHeldWhilePaging: in single-pane mode the pager holds
// events back, but not messages (they have their own pane, blink and
// ring), and the held list is capped (second review m7, n5).
func TestMessagesNotHeldWhilePaging(t *testing.T) {
	s, _ := newTestShell(t)
	s.split = false
	s.set.MsgBell = true
	s.Render(24, 100)
	s.showResult(numbered(200), 1)
	if !s.Paging() {
		t.Fatal("not paging")
	}
	s.HandleEvent(ev(link.EvMsgIn, 1, "urgent message"))
	if s.msgs.Len() != 1 || !s.bell {
		t.Fatal("message held by the pager")
	}
	for i := 0; i < maxHeld+50; i++ {
		s.HandleEvent(ev(link.EvLink, 1, "event"))
	}
	if len(s.held) != maxHeld {
		t.Fatalf("held %d", len(s.held))
	}
}

// TestBareLayoutShowsMessages: below 20x8 (no frames) messages still
// get rows (second review m7).
func TestBareLayoutShowsMessages(t *testing.T) {
	for _, split := range []bool{true, false} {
		s, _ := newTestShell(t)
		s.split = split
		s.HandleEvent(ev(link.EvMsgIn, 1, "hello from afar"))
		f := s.Render(7, 80)
		if len(f.Rows) != 7 || !strings.Contains(joinText(f.Rows), "hello from afar") {
			t.Fatalf("split %v:\n%s", split, joinText(f.Rows))
		}
		if !strings.Contains(f.Rows[f.CursorRow].Text, "NODEA") {
			t.Fatalf("cursor row %q", f.Rows[f.CursorRow].Text)
		}
	}
}

// TestRenderAllSizes renders every size from 1x20 to 49x139 with every
// focus, scrolled and resized: never taller or wider than the screen,
// cursor on screen (the second review's render sweep).
func TestRenderAllSizes(t *testing.T) {
	s, _ := newTestShell(t)
	s.set.Color = true
	for i := 0; i < 300; i++ {
		k := link.EvLink
		if i%3 == 0 {
			k = link.EvMsgIn
		}
		s.HandleEvent(link.Event{Time: time.Now(), Kind: k, Level: 1, Node: "NODEB",
			Text: fmt.Sprintf("event %d with some long text that wraps around the screen more than once when narrow enough ok", i)})
		s.addResult(Line{Text: fmt.Sprintf("result %d", i)})
	}
	for _, split := range []bool{true, false} {
		s.split = split
		for focus := 0; focus < 3; focus++ {
			s.focus = focus
			for rows := 1; rows < 50; rows++ {
				for cols := 20; cols < 140; cols += 3 {
					s.Scroll(-1)
					s.Resize(1)
					f := s.Render(rows, cols)
					if len(f.Rows) > max(rows, 2) {
						t.Fatalf("split=%v %dx%d: %d rows", split, rows, cols, len(f.Rows))
					}
					for _, r := range f.Rows {
						if Width(sanitize(r.Text)) > max(cols, 1) {
							t.Fatalf("split=%v %dx%d: row too wide %d: %q", split, rows, cols, Width(r.Text), r.Text)
						}
					}
					if f.CursorRow >= max(rows, 2) {
						t.Fatalf("split=%v %dx%d: cursor row %d", split, rows, cols, f.CursorRow)
					}
				}
			}
		}
	}
}

// TestSendMarks: msg, cmd and send add no result line when typed;
// the echoed line shows " …" until the backend reports, then " ✓", or
// " ✗" plus an error line; ASCII fallback without Unicode.
func TestSendMarks(t *testing.T) {
	s, be := newTestShell(t)
	s.box = unicodeBox
	echoRow := func() string {
		rows := s.results.Visible(200, 200)
		for i := len(rows) - 1; i >= 0; i-- {
			if strings.Contains(rows[i].Text, "> ") {
				return rows[i].Text
			}
		}
		return ""
	}
	for _, cmd := range []string{"msg nodeb bob hi there", "cmd nodeb q names", "send nodeb bob a.txt"} {
		before := s.results.Len()
		s.submit(cmd)
		if s.results.Len() != before+1 {
			t.Fatalf("%s: result lines added: %s", cmd, joinText(s.results.Visible(5, 200)))
		}
		if got := echoRow(); !strings.HasSuffix(got, cmd+" …") {
			t.Fatalf("%s: pending %q", cmd, got)
		}
		if changed, _ := s.ApplyMarks(); changed {
			t.Fatal("mark changed before the send finished")
		}
		done := be.dones[len(be.dones)-1]
		go done(nil) // reported from another goroutine, like the link
		<-s.Wake()
		if changed, _ := s.ApplyMarks(); !changed || !strings.HasSuffix(echoRow(), cmd+" ✓") {
			t.Fatalf("%s: ok %q", cmd, echoRow())
		}
		// The mark is in both copies (results and mixed pane).
		if m := s.mixed.Visible(200, 200); !strings.Contains(joinText(m), cmd+" ✓") {
			t.Fatalf("%s: mixed pane lacks the mark", cmd)
		}
	}
	// Failure later on the link: ✗ and an error line.
	s.submit("msg nodeb bob late")
	be.dones[len(be.dones)-1](errors.New("not delivered (link down)"))
	_, errs := s.ApplyMarks()
	if len(errs) != 1 || !strings.Contains(errs[0].Text, "message to NODEB/BOB: not delivered") {
		t.Fatalf("%+v", errs)
	}
	if !strings.Contains(joinText(s.results.Visible(200, 200)), "msg nodeb bob late ✗") {
		t.Fatal("no ✗ mark")
	}
	// Immediate failure: ✗ and the error line as before.
	be.fail = true
	res := s.submit("cmd nodeb q")
	if res.Lines[0].Color != Red || !strings.Contains(joinText(s.results.Visible(200, 200)), "cmd nodeb q ✗") {
		t.Fatalf("%+v", res.Lines)
	}
	be.fail = false
	// ASCII fallback.
	s.box = asciiBox
	s.submit("msg nodeb bob ascii")
	if !strings.HasSuffix(echoRow(), "ascii ...") {
		t.Fatalf("ascii pending %q", echoRow())
	}
	be.dones[len(be.dones)-1](nil)
	s.ApplyMarks()
	if !strings.HasSuffix(echoRow(), "ascii [ok]") {
		t.Fatalf("ascii ok %q", echoRow())
	}
	// Exec from code (no echoed line) keeps the confirmation line.
	if l := s.Exec("msg nodeb bob code").Lines; len(l) != 1 || !strings.Contains(l[0].Text, "message sent to NODEB/BOB") {
		t.Fatalf("%+v", l)
	}
}

// TestSaveWithoutConfigFile: started with -o only, an explicit save
// writes a new config file from the -o settings plus the change.
func TestSaveWithoutConfigFile(t *testing.T) {
	s, _ := newTestShell(t)
	s.set.ConfigPath = filepath.Join(t.TempDir(), "gonje.cnf")
	s.set.ConfigInit = "# gonje.cnf written by gonje from its -o options\nlocal_node=NODEA remote_host_name=NODEB remote_host_addr=b\n"
	res := s.Exec("link define nodec 10.0.0.3 save")
	if !strings.Contains(joinText(res.Lines), "saved to new config file "+s.set.ConfigPath) {
		t.Fatalf("%+v", res.Lines)
	}
	c, err := config.Load(s.set.ConfigPath)
	if err != nil || c.LocalNode != "NODEA" || c.Remote("NODEB") == nil || c.Remote("NODEC") == nil {
		t.Fatalf("%+v %v", c, err)
	}
	if res := s.Exec("link change nodec keepalive=20 save"); !strings.Contains(joinText(res.Lines), "saved to "+s.set.ConfigPath) ||
		strings.Contains(joinText(res.Lines), "new config file") {
		t.Fatalf("%+v", res.Lines)
	}
}
