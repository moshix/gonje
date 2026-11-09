// Copyright 2026 by moshix

package shell

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gonje/internal/nje/link"
)

// fillShell gives a shell long results, events and messages to lay out.
func fillShell(t *testing.T, unicode bool) *Shell {
	t.Helper()
	s, _ := newTestShell(t)
	if unicode {
		s.box = unicodeBox
	} else {
		s.box = asciiBox
	}
	s.level = 3
	for i := 0; i < 60; i++ {
		s.HandleEvent(ev(link.EvLink, 2, strings.Repeat("event text ", 25)))
		s.HandleEvent(ev(link.EvMsgIn, 2, "from RELAY/ROOT to MOSHE: "+strings.Repeat("what is up? ", 12)))
	}
	s.submit("help")
	s.submit("nodes")
	for _, k := range "typing a long command " + strings.Repeat("x", 300) {
		s.editor.Key(Key{Code: KeyRune, Rune: k})
	}
	return s
}

// borderRows lists the row indexes that must hold frame borders for the
// shell's current layout, top to bottom: title line, events box (split),
// messages box, command box (whose bottom border is the last row).
func borderRows(s *Shell, rows int) map[int]bool {
	l := s.layoutFor(rows, s.cols)
	b := map[int]bool{0: true}
	r := 1
	box := func(n int) { // a box with n content rows starting at r
		b[r], b[r+n+1] = true, true
		r += n + 2
	}
	if s.split {
		box(l.bottom)
	}
	if l.msg > 0 {
		box(l.msg)
	}
	if s.split {
		box(l.top)
	} else {
		box(l.mixed + 1)
	}
	return b
}

// checkFrame verifies every row: exactly w wide; border rows only where
// the layout puts them; every other row is "│ content │" with the
// padding blanks, so no pane content ever lands on a border row or
// crosses the frame.
func checkFrame(t *testing.T, s *Shell, f Frame, rows, w int) {
	t.Helper()
	b := s.box
	if len(f.Rows) != rows {
		t.Fatalf("%d rows, want %d", len(f.Rows), rows)
	}
	borders := borderRows(s, rows)
	if !borders[rows-1] {
		t.Fatalf("layout does not end with a border: %v", borders)
	}
	for i, r := range f.Rows {
		if Width(r.Text) != w {
			t.Fatalf("row %d is %d wide, want %d: %q", i, Width(r.Text), w, r.Text)
		}
		rs := []rune(r.Text)
		first, last := string(rs[0]), string(rs[len(rs)-1])
		if borders[i] {
			okL := first == b.TL || first == b.BL || (i == 0 && first == b.H)
			okR := last == b.TR || last == b.BR || (i == 0 && last == b.H)
			if !okL || !okR || string(rs[1]) != b.H {
				t.Fatalf("row %d should be a border: %q", i, r.Text)
			}
			continue
		}
		if first != b.V || last != b.V || rs[1] != ' ' || rs[len(rs)-2] != ' ' {
			t.Fatalf("row %d is not a padded content row: %q", i, r.Text)
		}
	}
	if borders[f.CursorRow] || f.CursorCol < 2 || f.CursorCol > w-3 {
		t.Fatalf("cursor %d,%d on a border", f.CursorRow, f.CursorCol)
	}
}

func TestRenderFramedSizes(t *testing.T) {
	for _, unicode := range []bool{true, false} {
		for _, split := range []bool{true, false} {
			for _, size := range [][2]int{{24, 80}, {30, 80}, {50, 200}, {8, 20}, {12, 40}, {40, 132}, {16, 60}} {
				s := fillShell(t, unicode)
				s.split = split
				rows, cols := size[0], size[1]
				f := s.Render(rows, cols)
				checkFrame(t, s, f, rows, usableWidth(cols))
				if cols >= 40 && (!strings.Contains(f.Rows[0].Text, "gonje 9.9") || !strings.Contains(f.Rows[0].Text, "NODEA")) {
					t.Fatalf("title %q", f.Rows[0].Text)
				}
			}
		}
	}
}

// TestRenderMessagesPane: messages get their own white-framed pane in
// the middle (below the events, above the command box), are not
// duplicated in the events pane, and the status hints sit in the
// bottom border of the command box.
func TestRenderMessagesPane(t *testing.T) {
	s := fillShell(t, true)
	f := s.Render(30, 80)
	l := s.layoutFor(30, 80)
	if l.msg != 6 {
		t.Fatalf("messages pane %d rows, want 6", l.msg)
	}
	top := f.Rows[l.bottom+3] // title line + events box
	if cmd := f.Rows[l.bottom+3+l.msg+2].Text; !strings.HasPrefix(cmd, "┌─ ▲▼ commands") {
		t.Fatalf("command box below the messages: %q", cmd)
	}
	if !strings.HasPrefix(top.Text, "┌─ messages · 60") || top.Segs[0].Color != BrightWhite {
		t.Fatalf("messages title %q %+v", top.Text, top.Segs[0])
	}
	if last := f.Rows[29].Text; !strings.Contains(last, "Tab focus") {
		t.Fatalf("status bar %q", last)
	}
	for _, e := range s.events.lines {
		if strings.Contains(e.Text, "what is up?") {
			t.Fatal("message duplicated in the events pane")
		}
	}
	if r := f.Rows[l.bottom+3+l.msg].Text; !strings.Contains(r, "what is up?") {
		t.Fatalf("last message row %q", r)
	}
	// No colors: no escape codes at all, white frame included.
	if strings.Contains(renderPad(top, 79, false), "\x1b") || !strings.Contains(renderPad(top, 79, true), "\x1b[1;97m") {
		t.Fatal("white frame color handling")
	}
	// split msgs <n>, and the minimum of 3.
	s.Exec("split msgs 10")
	if s.layoutFor(30, 80).msg != 10 {
		t.Fatal("split msgs")
	}
	if r := s.Exec("split msgs 2"); r.Lines[0].Color != Red {
		t.Fatal("split msgs 2 accepted")
	}
	// Single pane keeps the messages pane.
	s.split = false
	checkFrame(t, s, s.Render(30, 80), 30, 79)
	if s.layoutFor(30, 80).msg != 10 {
		t.Fatal("no messages pane when unsplit")
	}
	// A new message while scrolled back: "new ↓" in the title.
	s.focus = focusMessages
	s.Scroll(-1)
	s.HandleEvent(ev(link.EvRspIn, 2, "late response"))
	if f := s.Render(30, 80); !strings.Contains(joinText(f.Rows), "new ↓") {
		t.Fatal("no new-message marker")
	}
	for i := 0; i < 10; i++ {
		s.Scroll(1)
	}
	if f := s.Render(30, 80); strings.Contains(joinText(f.Rows), "new ↓") {
		t.Fatal("marker kept after scrolling down")
	}
}

func TestMessageBell(t *testing.T) {
	s, _ := newTestShell(t)
	s.HandleEvent(ev(link.EvMsgIn, 2, "x"))
	if s.bell {
		t.Fatal("bell without msg_bell")
	}
	s.set.MsgBell = true
	s.HandleEvent(ev(link.EvLink, 2, "x"))
	if s.bell {
		t.Fatal("bell for a non-message")
	}
	s.HandleEvent(ev(link.EvRspIn, 2, "x"))
	if !s.bell {
		t.Fatal("no bell for a response")
	}
}

func TestRenderTitlesAndHints(t *testing.T) {
	s := fillShell(t, true)
	f := s.Render(30, 100)
	if !strings.HasPrefix(f.Rows[0].Text, "── gonje 9.9 · NODEA · 1/2 links up ──") {
		t.Fatalf("title %q", f.Rows[0].Text)
	}
	if !strings.HasPrefix(f.Rows[1].Text, "┌─ incoming") {
		t.Fatalf("events box not at the top: %q", f.Rows[1].Text)
	}
	if c := cmdRow(f); !strings.HasPrefix(c, "┌─ ▲▼ commands ─") {
		t.Fatalf("command box title %q", c)
	}
	div := dividerRow(f, s.box)
	if !strings.Contains(div, "incoming · L3 · live · 60 events") || strings.Contains(div, "▲▼") {
		t.Fatalf("divider %q", div)
	}
	if !strings.Contains(f.Rows[29].Text, "Tab focus  ^U/^D scroll  ^G/^V resize") {
		t.Fatalf("status bar %q", f.Rows[29].Text)
	}
	s.split = false
	s.Scroll(-1)
	f = s.Render(30, 100)
	if last := f.Rows[29].Text; strings.Contains(last, "resize") {
		t.Fatalf("single-pane status bar %q", last)
	}
	if c := cmdRow(f); !strings.Contains(c, "commands · ↑ ") {
		t.Fatalf("single-pane scroll hint %q", c)
	}
}

// dividerRow returns the events box title row.
func dividerRow(f Frame, b Box) string {
	return rowWith(f, b.TL, "incoming")
}

// cmdRow returns the command box title row.
func cmdRow(f Frame) string { return rowWith(f, "", "commands") }

func rowWith(f Frame, prefix, text string) string {
	for _, r := range f.Rows[1 : len(f.Rows)-1] {
		if strings.HasPrefix(r.Text, prefix) && strings.Contains(r.Text, text) {
			return r.Text
		}
	}
	return ""
}

// TestFocusAndScroll: Tab cycles the scroll focus through the three
// panes; Ctrl-U/Ctrl-D (Scroll) act on the focused pane only, clamp at
// both ends, and submitting a command snaps back to live.
func TestFocusAndScroll(t *testing.T) {
	s := fillShell(t, true)
	for i := 0; i < 50; i++ {
		s.submit("version")
	}
	s.Render(40, 100)
	s.Scroll(-1)
	if s.results.Scroll() == 0 || s.events.Scroll() != 0 || s.msgs.Scroll() != 0 {
		t.Fatal("ctrl-u on commands")
	}
	if c := cmdRow(s.Render(40, 100)); !strings.Contains(c, "▲▼ commands · ↑ ") {
		t.Fatalf("title while scrolled %q", c)
	}
	s.Scroll(1)
	s.ToggleFocus() // events
	s.Scroll(-1)
	if s.events.Scroll() == 0 || s.results.Scroll() != 0 {
		t.Fatal("ctrl-u on events")
	}
	if div := dividerRow(s.Render(40, 100), s.box); !strings.Contains(div, "▲▼ incoming · L3 · ↑ ") {
		t.Fatalf("divider while scrolled %q", div)
	}
	s.ToggleFocus() // messages
	s.Scroll(-1)
	if s.msgs.Scroll() == 0 {
		t.Fatal("ctrl-u on messages")
	}
	if !strings.Contains(joinText(s.Render(40, 100).Rows), "▲▼ messages · 60 · ↑ ") {
		t.Fatal("messages title while scrolled")
	}
	s.ToggleFocus() // back to commands
	if s.focus != focusCommands {
		t.Fatal("focus did not cycle")
	}
	// Clamp at the oldest page and at live (events).
	s.focus = focusEvents
	for i := 0; i < 1000; i++ {
		s.Scroll(-1)
	}
	l := s.layoutFor(40, 100)
	if s.events.Scroll() != s.events.Len()-l.bottom {
		t.Fatalf("top clamp %d of %d", s.events.Scroll(), s.events.Len())
	}
	for i := 0; i < 1000; i++ {
		s.Scroll(1)
	}
	if s.events.Scroll() != 0 {
		t.Fatal("bottom clamp")
	}
	s.Scroll(-1)
	s.submit("level 3")
	if s.events.Scroll() != 0 || s.results.Scroll() != 0 {
		t.Fatal("not live after submit")
	}
	// Single pane: Tab toggles between the pane and the messages.
	s.split, s.focus = false, focusCommands
	s.ToggleFocus()
	if s.focus != focusMessages {
		t.Fatal("unsplit focus to messages")
	}
	s.ToggleFocus()
	if s.focus != focusCommands {
		t.Fatal("unsplit focus back")
	}
}

// TestEventCountMatchesRows: the divider counts the event lines held at
// the current level, and that many non-empty rows are drawn.
func TestEventCountMatchesRows(t *testing.T) {
	for level := 1; level <= 3; level++ {
		s, _ := newTestShell(t)
		s.box = unicodeBox
		s.level = level
		s.HandleEvent(ev(link.EvCmdIn, 1, "cmd"))
		s.HandleEvent(ev(link.EvLink, 2, "link"))
		s.HandleEvent(ev(link.EvDump, 3, "dump", "0000  C1"))
		want := []int{1, 2, 4}[level-1]
		f := s.Render(30, 100)
		if div := dividerRow(f, s.box); !strings.Contains(div, fmt.Sprintf("%d events", want)) {
			t.Fatalf("L%d divider %q", level, div)
		}
		l := s.layoutFor(30, 100)
		start := 2 // title line, events box top border
		drawn := 0
		for _, r := range f.Rows[start : start+l.bottom] {
			if strings.TrimSpace(strings.Trim(r.Text, "│")) != "" {
				drawn++
			}
		}
		if drawn != want {
			t.Fatalf("L%d: %d rows drawn, divider says %d", level, drawn, want)
		}
	}
}

// TestFramePadding: the input line is inside the frame with both borders.
func TestFramePadding(t *testing.T) {
	for _, split := range []bool{true, false} {
		s := fillShell(t, true)
		s.split = split
		f := s.Render(24, 60)
		checkFrame(t, s, f, 24, 59)
		in := f.Rows[f.CursorRow].Text
		if !strings.Contains(in, "NODEA [1/2]> ") {
			t.Fatalf("input row %q", in)
		}
	}
}

func TestRenderTiny(t *testing.T) {
	for _, split := range []bool{true, false} {
		for _, size := range [][2]int{{7, 80}, {24, 19}, {3, 15}, {5, 30}} {
			s := fillShell(t, true)
			s.split = split
			rows, cols := size[0], size[1]
			f := s.Render(rows, cols)
			if len(f.Rows) != max(rows, 2) {
				t.Fatalf("%v: %d rows", size, len(f.Rows))
			}
			for i, r := range f.Rows {
				if Width(r.Text) > usableWidth(cols) || strings.ContainsAny(r.Text, "┌│└") {
					t.Fatalf("%v row %d: %q", size, i, r.Text)
				}
			}
		}
	}
}

func TestResizePanes(t *testing.T) {
	s := fillShell(t, true)
	s.Render(30, 100) // 30 - 8 (messages box) - 5 = 17 inner rows, 40% -> 6
	inner := 17
	_, b := s.paneHeights(inner)
	if b != 6 {
		t.Fatalf("initial events pane %d", b)
	}
	s.Resize(1)
	if _, b = s.paneHeights(inner); b != 7 {
		t.Fatalf("grow -> %d", b)
	}
	f := s.Render(30, 100)
	if !strings.Contains(joinText(f.Rows), "events 7 lines") {
		t.Fatal("size note not shown")
	}
	for i := 0; i < 50; i++ {
		s.Resize(1)
	}
	if top, b := s.paneHeights(inner); b != inner-minPane || top != minPane {
		t.Fatalf("grow clamp: top %d bottom %d", top, b)
	}
	for i := 0; i < 50; i++ {
		s.Resize(-1)
	}
	if _, b = s.paneHeights(inner); b != minPane {
		t.Fatalf("shrink clamp %d", b)
	}
	checkFrame(t, s, s.Render(30, 100), 30, 99)
	// The note disappears after a while.
	s.now = func() time.Time { return time.Now().Add(time.Minute) }
	if strings.Contains(joinText(s.Render(30, 100).Rows), "events 3 lines") {
		t.Fatal("note did not expire")
	}
	// split <percent> resets the manual size.
	s.Exec("split 25")
	if _, b = s.paneHeights(inner); b != 4 || s.bottomLines != 0 {
		t.Fatalf("split 25 -> %d", b)
	}
	// Resize keys do nothing in single-pane mode.
	s.split = false
	s.Resize(5)
	if s.bottomLines != 0 {
		t.Fatal("resized while unsplit")
	}
}

func TestHelpLayout(t *testing.T) {
	for _, width := range []int{0, 120, 78, 50, 30} {
		lines := helpLines(width)
		col := helpCol()
		for _, l := range lines {
			if width > 0 && Width(l.Text) > width && len(l.Segs) > 0 {
				t.Fatalf("width %d: %q is %d wide", width, l.Text, Width(l.Text))
			}
		}
		// Every command row starts its description at the same column.
		if width == 0 || width >= 78 {
			for _, l := range lines {
				if len(l.Segs) == 5 || (len(l.Segs) == 6 && l.Segs[1].Color == BoldCyan) {
					if len(l.Segs) == 6 && Width(l.Segs[0].Text+l.Segs[1].Text+l.Segs[2].Text+l.Segs[3].Text+l.Segs[4].Text) != col+4 {
						t.Fatalf("width %d: misaligned %q", width, l.Text)
					}
				}
			}
		}
	}
	text := joinText(helpLines(0))
	for _, want := range []string{"Links", "Messages & commands", "Files", "Local node", "Display", "Misc",
		"link define", "local name", "split", "version", "Ctrl-U/Ctrl-D"} {
		if !strings.Contains(text, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	// Colored command names, dimmed args.
	for _, l := range helpLines(0) {
		if len(l.Segs) == 6 && (l.Segs[1].Color != BoldCyan || l.Segs[3].Color != Dim) {
			t.Fatalf("colors %+v", l.Segs)
		}
	}
	d := joinText(helpFor([]string{"link", "define"}))
	if !strings.Contains(d, "usage: link define <node> <addr> [opts] [save]") || !strings.Contains(d, "example: link define NODEC") {
		t.Fatalf("%s", d)
	}
	if d := joinText(helpFor([]string{"link"})); !strings.Contains(d, "usage: link") {
		t.Fatal(d)
	}
	if d := helpFor([]string{"nosuch"}); d[0].Color != Red {
		t.Fatal("unknown help topic")
	}
	// The table is wrapped to the shell's current width.
	s, _ := newTestShell(t)
	s.Render(24, 60)
	for _, l := range s.Exec("help").Lines {
		if len(l.Segs) > 0 && Width(l.Text) > s.width {
			t.Fatalf("help row %q wider than %d", l.Text, s.width)
		}
	}
}

// TestMessageBlink: a new message's tag blinks (SGR 5) and toggles
// reverse video every 500 ms for 10 s, then renders normally; several
// messages blink on their own clocks; no blink without colors.
func TestMessageBlink(t *testing.T) {
	s, _ := newTestShell(t)
	s.box = unicodeBox
	s.set.Color = true
	t0 := time.Date(2026, 11, 9, 11, 29, 33, 0, time.Local)
	clock := t0
	s.now = func() time.Time { return clock }
	tagAt := func(text string) Color {
		t.Helper()
		f := s.Render(30, 100)
		for _, r := range f.Rows {
			if strings.Contains(r.Text, text) {
				for _, sg := range r.Segs {
					if strings.HasPrefix(sg.Text, "MSG") || strings.HasPrefix(sg.Text, "RSP") {
						return sg.Color
					}
				}
			}
		}
		t.Fatalf("no tag found for %q", text)
		return Default
	}
	s.HandleEvent(ev(link.EvMsgIn, 2, "first message"))
	steps := []struct {
		at   time.Duration
		want Color
	}{
		{0, BlinkRev}, {499 * time.Millisecond, BlinkRev}, {500 * time.Millisecond, Blink},
		{1000 * time.Millisecond, BlinkRev}, {9 * time.Second, BlinkRev}, {9600 * time.Millisecond, Blink}, {10 * time.Second, Cyan},
	}
	for _, st := range steps {
		clock = t0.Add(st.at)
		if got := tagAt("first message"); got != st.want {
			t.Fatalf("at %v: tag color %d, want %d", st.at, got, st.want)
		}
	}
	// A second message 3 s in blinks on its own clock.
	clock = t0.Add(3 * time.Second)
	s.HandleEvent(ev(link.EvRspIn, 2, "second message"))
	clock = t0.Add(12 * time.Second) // first stopped, second still blinking
	if tagAt("first message") != Cyan || tagAt("second message") == Magenta || !s.Blinking() {
		t.Fatal("independent blink clocks")
	}
	clock = t0.Add(13 * time.Second)
	if tagAt("second message") != Magenta || s.Blinking() {
		t.Fatal("second blink did not stop after 10 s")
	}
	// The rendered escape codes, and none without colors.
	clock = t0.Add(20 * time.Second)
	s.HandleEvent(ev(link.EvMsgIn, 2, "third"))
	f := s.Render(30, 100)
	var row Line
	for _, r := range f.Rows {
		if strings.Contains(r.Text, "third") {
			row = r
		}
	}
	if !strings.Contains(renderPad(row, 99, true), "\x1b[5;7mMSG") {
		t.Fatal("no blink codes on the tag")
	}
	s.set.Color = false
	if strings.Contains(renderPad(s.Render(30, 100).Rows[0], 99, false), "\x1b") || s.Blinking() {
		t.Fatal("blink with NO_COLOR")
	}
	// The blinking pane still keeps its frame (border-row check with messages).
	checkFrame(t, s, s.Render(30, 100), 30, 99)
}

// TestTablesNeverWrap: table rows (nodes / link list, routes, stats) are
// clipped with "…" instead of wrapping, at any pane width, and the
// rendered pane rows never cross the frame.
func TestTablesNeverWrap(t *testing.T) {
	clipped := 0
	for _, cols := range []int{30, 45, 60, 80, 100} {
		s := fillShell(t, true)
		s.Render(40, cols)
		for _, cmd := range []string{"nodes", "link list", "routes", "stats"} {
			for _, l := range s.Exec(cmd).Lines {
				if !l.Clip {
					continue
				}
				rows := wrapLine(l, s.width)
				if len(rows) != 1 || Width(rows[0].Text) > s.width {
					t.Fatalf("%s at %d cols wrapped: %q", cmd, cols, l.Text)
				}
				if Width(l.Text) > s.width {
					clipped++
				}
				if Width(l.Text) > s.width && !strings.HasSuffix(rows[0].Text, "…") {
					t.Fatalf("%s at %d cols: clipped row without …: %q", cmd, cols, rows[0].Text)
				}
			}
		}
		// The status table of fillShell's backend: every row clipped.
		for _, l := range s.statusLines() {
			if strings.HasPrefix(l.Text, "  last error") {
				continue
			}
			if !l.Clip {
				t.Fatalf("status row not clipped: %q", l.Text)
			}
		}
		checkFrame(t, s, s.Render(40, cols), 40, usableWidth(cols))
	}
	if clipped == 0 {
		t.Fatal("no table row was wide enough to be clipped")
	}
	// clipLine keeps the colors of what fits.
	l := clipLine(SegLine(Seg{"abcdef", Red}, Seg{"ghij", Green}), 8)
	if l.Text != "abcdefg…" || l.Segs[0].Color != Red || l.Segs[len(l.Segs)-1].Color != Green {
		t.Fatalf("clipLine %q %+v", l.Text, l.Segs)
	}
}
