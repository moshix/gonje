package shell

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gonje/internal/nje/link"
)

// fillShell gives a shell long results and events to lay out.
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
		s.HandleEvent(ev(link.EvMsgIn, 2, strings.Repeat("event text ", 25)))
	}
	s.submit("help")
	s.submit("nodes")
	for _, k := range "typing a long command " + strings.Repeat("x", 300) {
		s.editor.Key(Key{Code: KeyRune, Rune: k})
	}
	return s
}

// checkFrame verifies the outer frame: every row exactly w wide, corner
// and side characters in place, so no text ever crosses the border.
func checkFrame(t *testing.T, f Frame, rows, w int, b Box, split bool) {
	t.Helper()
	if len(f.Rows) != rows {
		t.Fatalf("%d rows, want %d", len(f.Rows), rows)
	}
	for i, r := range f.Rows {
		if Width(r.Text) != w {
			t.Fatalf("row %d is %d wide, want %d: %q", i, Width(r.Text), w, r.Text)
		}
		rs := []rune(r.Text)
		first, last := string(rs[0]), string(rs[len(rs)-1])
		switch {
		case i == 0:
			if first != b.TL || last != b.TR {
				t.Fatalf("top border %q", r.Text)
			}
		case i == rows-1:
			if first != b.BL || last != b.BR {
				t.Fatalf("bottom border %q", r.Text)
			}
		case first == b.LT:
			if !split || last != b.RT {
				t.Fatalf("divider %q", r.Text)
			}
		default:
			if first != b.V || last != b.V {
				t.Fatalf("row %d crosses the frame: %q", i, r.Text)
			}
		}
	}
	if f.CursorCol < 1 || f.CursorCol >= w-1 || f.CursorRow <= 0 || f.CursorRow >= rows-1 {
		t.Fatalf("cursor %d,%d outside the frame", f.CursorRow, f.CursorCol)
	}
}

func TestRenderFramedSizes(t *testing.T) {
	for _, unicode := range []bool{true, false} {
		for _, split := range []bool{true, false} {
			for _, size := range [][2]int{{24, 80}, {50, 200}, {8, 20}, {12, 40}, {40, 132}} {
				s := fillShell(t, unicode)
				s.split = split
				rows, cols := size[0], size[1]
				f := s.Render(rows, cols)
				checkFrame(t, f, rows, usableWidth(cols), s.box, split)
				dividers := 0
				for _, r := range f.Rows[1 : rows-1] {
					if strings.HasPrefix(r.Text, s.box.LT) {
						dividers++
					}
				}
				if (split && dividers != 1) || (!split && dividers != 0) {
					t.Fatalf("split=%v: %d dividers", split, dividers)
				}
				if cols >= 40 && (!strings.Contains(f.Rows[0].Text, "gonje 9.9") || !strings.Contains(f.Rows[0].Text, "NODEA")) {
					t.Fatalf("title %q", f.Rows[0].Text)
				}
			}
		}
	}
}

func TestRenderTitlesAndHints(t *testing.T) {
	s := fillShell(t, true)
	f := s.Render(24, 100)
	if !strings.HasPrefix(f.Rows[0].Text, "┌─ gonje 9.9 · NODEA · 1/2 links up · ▲▼ commands ─") {
		t.Fatalf("title %q", f.Rows[0].Text)
	}
	div := dividerRow(f, s.box)
	if !strings.Contains(div, "incoming · L3 · live · 60 events") || strings.Contains(div, "▲▼") {
		t.Fatalf("divider %q", div)
	}
	if !strings.Contains(f.Rows[23].Text, "Tab focus  ^U/^D scroll  ^G/^V resize") {
		t.Fatalf("status bar %q", f.Rows[23].Text)
	}
	// Single pane: no focus/resize hints, scroll indicator in the status bar.
	s.split = false
	s.Scroll(-1)
	f = s.Render(24, 100)
	last := f.Rows[23].Text
	if strings.Contains(last, "resize") || strings.Contains(last, "Tab") || !strings.Contains(last, "↑ ") {
		t.Fatalf("single-pane status bar %q", last)
	}
	s.ToggleFocus() // no-op when unsplit
	if s.focus != focusCommands {
		t.Fatal("focus toggled in single-pane mode")
	}
}

func dividerRow(f Frame, b Box) string {
	for _, r := range f.Rows[1 : len(f.Rows)-1] {
		if strings.HasPrefix(r.Text, b.LT) {
			return r.Text
		}
	}
	return ""
}

// TestFocusAndScroll: Tab moves the scroll focus; Ctrl-U/Ctrl-D (Scroll)
// act on the focused pane only, clamp at both ends, and submitting a
// command snaps back to live.
func TestFocusAndScroll(t *testing.T) {
	s := fillShell(t, true)
	for i := 0; i < 50; i++ {
		s.submit("version")
	}
	s.Render(30, 100)
	// Commands pane has the focus by default.
	s.Scroll(-1)
	if s.results.Scroll() == 0 || s.events.Scroll() != 0 {
		t.Fatalf("ctrl-u on commands: results %d events %d", s.results.Scroll(), s.events.Scroll())
	}
	f := s.Render(30, 100)
	if !strings.Contains(f.Rows[0].Text, "▲▼ commands · ↑ ") {
		t.Fatalf("title while scrolled %q", f.Rows[0].Text)
	}
	s.Scroll(1)
	if s.results.Scroll() != 0 {
		t.Fatal("ctrl-d back to live")
	}
	// Tab: events pane.
	s.ToggleFocus()
	s.Scroll(-1)
	if s.events.Scroll() == 0 || s.results.Scroll() != 0 {
		t.Fatal("ctrl-u on events")
	}
	f = s.Render(30, 100)
	if div := dividerRow(f, s.box); !strings.Contains(div, "▲▼ incoming · L3 · ↑ ") {
		t.Fatalf("divider while scrolled %q", div)
	}
	if strings.Contains(f.Rows[0].Text, "▲▼") {
		t.Fatal("both panes marked as focused")
	}
	// Clamp at the oldest page and at live.
	for i := 0; i < 1000; i++ {
		s.Scroll(-1)
	}
	_, bottom := s.paneHeights(27)
	if s.events.Scroll() != s.events.Len()-bottom {
		t.Fatalf("top clamp %d of %d", s.events.Scroll(), s.events.Len())
	}
	for i := 0; i < 1000; i++ {
		s.Scroll(1)
	}
	if s.events.Scroll() != 0 {
		t.Fatal("bottom clamp")
	}
	// Submitting snaps everything back to live.
	s.Scroll(-1)
	s.ToggleFocus()
	s.Scroll(-1)
	s.submit("level 3")
	if s.events.Scroll() != 0 || s.results.Scroll() != 0 {
		t.Fatal("not live after submit")
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
		s.HandleEvent(ev(link.EvMsgIn, 2, "msg"))
		s.HandleEvent(ev(link.EvDump, 3, "dump", "0000  C1"))
		want := []int{1, 2, 4}[level-1]
		f := s.Render(30, 100)
		if div := dividerRow(f, s.box); !strings.Contains(div, fmt.Sprintf("%d events", want)) {
			t.Fatalf("L%d divider %q", level, div)
		}
		drawn := 0
		after := false
		for _, r := range f.Rows[1 : len(f.Rows)-1] {
			if strings.HasPrefix(r.Text, s.box.LT) {
				after = true
				continue
			}
			if after && strings.TrimSpace(strings.Trim(r.Text, "│")) != "" {
				drawn++
			}
		}
		if drawn != want {
			t.Fatalf("L%d: %d rows drawn, divider says %d", level, drawn, want)
		}
	}
}

// TestFramePadding: one blank between the border and the text on both
// sides, and the input line is inside the frame with both borders.
func TestFramePadding(t *testing.T) {
	for _, split := range []bool{true, false} {
		s := fillShell(t, true)
		s.split = split
		f := s.Render(20, 60)
		for i, r := range f.Rows[1 : len(f.Rows)-1] {
			rs := []rune(r.Text)
			if string(rs[0]) == s.box.LT {
				continue
			}
			if rs[1] != ' ' || rs[len(rs)-2] != ' ' {
				t.Fatalf("split=%v row %d not padded: %q", split, i+1, r.Text)
			}
		}
		in := []rune(f.Rows[f.CursorRow].Text)
		if string(in[0]) != s.box.V || string(in[len(in)-1]) != s.box.V || !strings.Contains(string(in), "NODEA [1/2]> ") {
			t.Fatalf("input row %q", string(in))
		}
		if f.CursorCol < 2 || f.CursorCol > len(in)-3 {
			t.Fatalf("cursor col %d", f.CursorCol)
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
	s.Render(30, 100) // inner = 27 rows, 40% -> 10 event lines
	_, b := s.paneHeights(27)
	if b != 10 {
		t.Fatalf("initial events pane %d", b)
	}
	s.Resize(1)
	if _, b = s.paneHeights(27); b != 11 {
		t.Fatalf("grow -> %d", b)
	}
	f := s.Render(30, 100)
	if !strings.Contains(joinText(f.Rows), "events 11 lines") {
		t.Fatal("size note not shown")
	}
	for i := 0; i < 50; i++ {
		s.Resize(1)
	}
	if top, b := s.paneHeights(27); b != 27-minPane || top != minPane {
		t.Fatalf("grow clamp: top %d bottom %d", top, b)
	}
	for i := 0; i < 50; i++ {
		s.Resize(-1)
	}
	if _, b = s.paneHeights(27); b != minPane {
		t.Fatalf("shrink clamp %d", b)
	}
	checkFrame(t, s.Render(30, 100), 30, 99, s.box, true)
	// The note disappears after a while.
	s.now = func() time.Time { return time.Now().Add(time.Minute) }
	if strings.Contains(joinText(s.Render(30, 100).Rows), "events 3 lines") {
		t.Fatal("note did not expire")
	}
	// split <percent> resets the manual size.
	s.Exec("split 25")
	if _, b = s.paneHeights(27); b != 6 || s.bottomLines != 0 {
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
