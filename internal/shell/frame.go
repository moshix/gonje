package shell

import (
	"fmt"
	"strings"
	"time"
)

// Box holds the characters used to draw frames: Unicode box drawing, or
// plain ASCII when the terminal is not UTF-8. We draw these ourselves
// (no ncurses: cgo would break the static cross-builds).
type Box struct {
	H, V, TL, TR, BL, BR, LT, RT, Dot, Up string
}

var (
	unicodeBox = Box{"─", "│", "┌", "┐", "└", "┘", "├", "┤", "·", "↑"}
	asciiBox   = Box{"-", "|", "+", "+", "+", "+", "+", "+", "-", "^"}
)

// UseUnicode decides between Unicode and ASCII frames from the
// environment: GONJE_ASCII=1 forces ASCII; otherwise the first set of
// LC_ALL, LC_CTYPE, LANG must name UTF-8.
func UseUnicode(getenv func(string) string) bool {
	if getenv("GONJE_ASCII") == "1" {
		return false
	}
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return false
}

// Frame is a fully laid out screen: one Line per terminal row plus the
// cursor position (0-based row/column).
type Frame struct {
	Rows      []Line
	CursorRow int
	CursorCol int
}

// Minimum terminal size for frames; below it the layout drops them.
const (
	minFrameCols = 20
	minFrameRows = 8
	minPane      = 3 // lines per pane in split view
	noteFor      = 2 * time.Second
)

// usableWidth is the number of columns we draw on a terminal that is
// cols wide. The last column is left empty: writing into it puts most
// terminals into a pending-wrap state that breaks erase-to-end-of-line.
func usableWidth(cols int) int {
	if cols <= 0 {
		cols = DefaultWidth
	}
	return max(cols-1, 10)
}

// frameColor is used for borders and the divider.
const frameColor = Cyan

// border draws a horizontal frame line: left corner, a title embedded
// after one H, H fill, right corner — exactly w columns wide.
func border(b Box, left, right string, title []Seg, w int) Line {
	segs := []Seg{{left + b.H, frameColor}}
	used := 2 + 1 // left+H, right
	if len(title) > 0 {
		segs = append(segs, Seg{" ", frameColor})
		used++
		room := w - used - 2
		for _, t := range title {
			if room <= 0 {
				break
			}
			txt := Fit(t.Text, room) // Fit(_, 0) would mean "no limit"
			room -= Width(txt)
			used += Width(txt)
			segs = append(segs, Seg{txt, t.Color})
		}
		segs = append(segs, Seg{" ", frameColor})
		used++
	}
	segs = append(segs, Seg{strings.Repeat(b.H, max(w-used, 0)) + right, frameColor})
	return SegLine(segs...)
}

// boxed wraps one content row in the side borders with one blank of
// padding on each side: "│ text… │". The content is clipped to iw-2
// columns and padded so the right border always lines up.
func boxed(b Box, content Line, iw int) Line {
	segs := []Seg{{b.V + " ", frameColor}}
	iw -= 2
	if len(content.Segs) > 0 {
		left := iw
		for _, sg := range content.Segs {
			t := Fit(sg.Text, left)
			left -= Width(t)
			segs = append(segs, Seg{t, sg.Color})
		}
		segs = append(segs, Seg{strings.Repeat(" ", left), Default})
	} else {
		t := Fit(content.Text, iw)
		segs = append(segs, Seg{t, content.Color}, Seg{strings.Repeat(" ", iw-Width(t)), Default})
	}
	segs = append(segs, Seg{" " + b.V, frameColor})
	return SegLine(segs...)
}

// paneHeights splits the inner rows of the split view into the top pane
// (results + input line) and the events pane: from split_ratio, or the
// size set with Ctrl-G/Ctrl-V, with at least minPane rows each.
func (s *Shell) paneHeights(inner int) (top, bottom int) {
	bottom = inner * s.set.SplitRatio / 100
	if s.bottomLines > 0 {
		bottom = s.bottomLines
	}
	if inner >= 2*minPane {
		bottom = min(max(bottom, minPane), inner-minPane)
	} else {
		bottom = inner / 2
	}
	return inner - bottom, bottom
}

// innerRows is the number of pane rows for a terminal of rows lines in
// the current mode (frames take 3 rows in split view, 2 otherwise).
func (s *Shell) innerRows(rows int) int {
	if s.framed(rows, s.cols) {
		return rows - 3
	}
	return rows - 1
}

// framed reports whether the screen is big enough for frames.
func (s *Shell) framed(rows, cols int) bool { return cols >= minFrameCols && rows >= minFrameRows }

// Resize grows (delta > 0) or shrinks the events pane by delta lines,
// keeping minPane lines in each pane, and shows the new size in the
// divider for a moment. Only meaningful in split view.
func (s *Shell) Resize(delta int) {
	if !s.split || s.rows == 0 {
		return
	}
	inner := s.innerRows(s.rows)
	_, cur := s.paneHeights(inner)
	s.bottomLines = cur + delta
	_, now := s.paneHeights(inner)
	s.bottomLines = now
	s.note(fmt.Sprintf("events %d lines", now))
}

// note shows a short message in the divider for noteFor.
func (s *Shell) note(text string) {
	s.noteText, s.noteUntil = text, s.now().Add(noteFor)
}

// Scroll focus in split view: the pane Ctrl-U/Ctrl-D act on. Tab
// toggles it; the focused pane's title is highlighted.
const (
	focusCommands = 0
	focusEvents   = 1
)

// ToggleFocus switches the scroll focus between the panes (split only).
func (s *Shell) ToggleFocus() {
	if s.split {
		s.focus = 1 - s.focus
	}
}

// scrollPane is the pane Ctrl-U/Ctrl-D act on, and its visible height.
func (s *Shell) scrollPane() (*Panel, int) {
	inner := s.innerRows(max(s.rows, minFrameRows))
	if !s.split {
		return s.mixed, inner - 1
	}
	top, bottom := s.paneHeights(inner)
	if s.focus == focusEvents {
		return s.events, bottom
	}
	return s.results, top - 1
}

// Scroll moves the focused pane by half its height: dir < 0 = back in
// time (Ctrl-U), dir > 0 = forward (Ctrl-D). The panel clamps at the
// oldest page and at live.
func (s *Shell) Scroll(dir int) {
	p, h := s.scrollPane()
	h = max(h, 2)
	if dir < 0 {
		p.ScrollUp(h/2, h)
	} else {
		p.ScrollDown(h / 2)
	}
}

// scrollHint describes a pane's scroll position: "↑ 40 lines" or "".
func (s *Shell) scrollHint(p *Panel) string {
	if p.Scroll() == 0 {
		return ""
	}
	return fmt.Sprintf("%s %d lines", s.box.Up, p.Scroll())
}

// focusMark is the marker of the focused pane.
func (s *Shell) focusMark() string {
	if s.box.H == unicodeBox.H {
		return "▲▼ "
	}
	return "^v "
}

// title is the text of the top border; in split view it also labels the
// command pane (highlighted when it has the scroll focus).
func (s *Shell) title() []Seg {
	li := s.be.Local()
	upColor := Green
	switch {
	case li.Links > 0 && li.Up == 0:
		upColor = Red
	case li.Up < li.Links:
		upColor = Yellow
	}
	d := " " + s.box.Dot + " "
	segs := []Seg{{"gonje " + s.set.Version, BoldCyan}, {d, frameColor}, {li.Name, BoldYellow},
		{d, frameColor}, {fmt.Sprintf("%d/%d links up", li.Up, li.Links), upColor}}
	if li.Pending > 0 {
		segs = append(segs, Seg{d, frameColor}, Seg{fmt.Sprintf("%d cmd pending", li.Pending), Magenta})
	}
	if li.Draining > 0 {
		segs = append(segs, Seg{d, frameColor}, Seg{fmt.Sprintf("%d draining", li.Draining), Yellow})
	}
	if s.split {
		label := Seg{"commands", Dim}
		if s.focus == focusCommands {
			label = Seg{s.focusMark() + "commands", BoldYellow}
		}
		segs = append(segs, Seg{d, frameColor}, label)
		if h := s.scrollHint(s.results); h != "" {
			segs = append(segs, Seg{d, frameColor}, Seg{h, BoldYellow})
		}
	}
	return segs
}

// hints is the status bar text in the bottom border.
func (s *Shell) hints() []Seg {
	h := "^U/^D scroll  help  quit"
	if s.split {
		h = "Tab focus  ^U/^D scroll  ^G/^V resize  help  quit"
	}
	return []Seg{{h, Dim}}
}

// dividerTitle is the text of the split-view divider: the events pane
// label (highlighted when focused), level, live/scroll position and the
// number of event lines held at the current level (or a resize note).
func (s *Shell) dividerTitle(bottom int) []Seg {
	d := " " + s.box.Dot + " "
	label := Seg{"incoming", BoldCyan}
	if s.focus == focusEvents {
		label = Seg{s.focusMark() + "incoming", BoldYellow}
	}
	segs := []Seg{label, {d, frameColor}, {fmt.Sprintf("L%d", s.level), Yellow}}
	if h := s.scrollHint(s.events); h != "" {
		segs = append(segs, Seg{d, frameColor}, Seg{h, BoldYellow})
	} else {
		segs = append(segs, Seg{d, frameColor}, Seg{"live", Green})
	}
	if s.noteText != "" && s.now().Before(s.noteUntil) {
		segs = append(segs, Seg{d, frameColor}, Seg{s.noteText, BoldYellow})
	} else {
		segs = append(segs, Seg{d, frameColor}, Seg{fmt.Sprintf("%d events", s.events.Len()), Dim})
	}
	return segs
}

// inputLine renders the editor with the colored prompt, w columns wide,
// or the pager's "-- more --" prompt while long output is paged.
func (s *Shell) inputLine(w int) (Line, int) {
	if s.pg != nil {
		l := fitLine(s.pagerLine(), w)
		return l, min(Width(l.Text), w-1)
	}
	ptext, psegs := s.prompt()
	text, col := s.editor.Render(ptext, w)
	if strings.HasPrefix(text, ptext) {
		return SegLine(append(psegs, Seg{text[len(ptext):], Default})...), col
	}
	return Line{Text: text}, col
}

// Render lays out the whole screen for a terminal of rows x cols:
//
//	split on                         split off
//	┌─ gonje 0.2 · NODEA · 1/1 ─┐    ┌─ gonje 0.2 · NODEA · 1/1 ─┐
//	│ command results           │    │ results and events,       │
//	│ NODEA [1/1]> input_       │    │ interleaved               │
//	├─ incoming · L2 · live ────┤    │ NODEA [1/1]> input_       │
//	│ events                    │    └─ ^U/^D scroll  help ──────┘
//	└─ ^G/^V resize  ^U/^D ... ─┘
//
// Every row is exactly the usable width; text is clipped or wrapped to
// the inner width so it can never overwrite the frame. Below 20x8 the
// frames are dropped and the panes use the whole screen.
func (s *Shell) Render(rows, cols int) Frame {
	s.rows, s.cols = rows, cols
	w := usableWidth(cols)
	if !s.framed(rows, cols) {
		return s.renderBare(max(rows, 2), w)
	}
	b := s.box
	iw := w - 2  // between the side borders
	cw := iw - 2 // text width: one blank of padding on each side
	s.width = cw
	f := Frame{Rows: []Line{border(b, b.TL, b.TR, s.title(), w)}}
	add := func(ls []Line) {
		for _, l := range ls {
			f.Rows = append(f.Rows, boxed(b, l, iw))
		}
	}
	if s.split {
		top, bottom := s.paneHeights(rows - 3)
		add(s.results.Visible(top-1, cw))
		in, col := s.inputLine(cw)
		f.CursorRow, f.CursorCol = len(f.Rows), 2+col
		add([]Line{in})
		f.Rows = append(f.Rows, border(b, b.LT, b.RT, s.dividerTitle(bottom), w))
		add(s.events.Visible(bottom, cw))
	} else {
		inner := rows - 2
		add(s.mixed.Visible(inner-1, cw))
		in, col := s.inputLine(cw)
		f.CursorRow, f.CursorCol = len(f.Rows), 2+col
		add([]Line{in})
	}
	hint := s.hints()
	if !s.split {
		if h := s.scrollHint(s.mixed); h != "" {
			hint = append([]Seg{{h, BoldYellow}, {"  ", Default}}, hint...)
		}
	}
	f.Rows = append(f.Rows, border(b, b.BL, b.BR, hint, w))
	return f
}

// renderBare is the frameless layout for tiny terminals.
func (s *Shell) renderBare(rows, w int) Frame {
	s.width = w
	var f Frame
	if s.split && rows >= 5 {
		inner := rows - 1
		top, bottom := s.paneHeights(inner)
		f.Rows = append(f.Rows, s.results.Visible(top-1, w)...)
		in, col := s.inputLine(w)
		f.CursorRow, f.CursorCol = len(f.Rows), col
		f.Rows = append(f.Rows, in)
		f.Rows = append(f.Rows, Line{Text: strings.Repeat(s.box.H, w), Color: frameColor})
		f.Rows = append(f.Rows, s.events.Visible(bottom, w)...)
	} else {
		f.Rows = append(f.Rows, s.mixed.Visible(rows-1, w)...)
		in, col := s.inputLine(w)
		f.CursorRow, f.CursorCol = len(f.Rows), col
		f.Rows = append(f.Rows, in)
	}
	for i := range f.Rows {
		f.Rows[i] = fitLine(f.Rows[i], w)
	}
	return f
}

// fitLine cuts a line (and its segments) to w columns.
func fitLine(l Line, w int) Line {
	if Width(l.Text) <= w {
		return l
	}
	if len(l.Segs) == 0 {
		return Line{Text: Fit(l.Text, w), Color: l.Color}
	}
	var segs []Seg
	left := w
	for _, sg := range l.Segs {
		if left <= 0 {
			break
		}
		t := Fit(sg.Text, left)
		left -= Width(t)
		segs = append(segs, Seg{t, sg.Color})
	}
	return SegLine(segs...)
}
