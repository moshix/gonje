// Copyright 2026 by moshix

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
	return borderC(b, left, right, title, w, frameColor)
}

// borderC is border in a given frame color.
func borderC(b Box, left, right string, title []Seg, w int, fc Color) Line {
	segs := []Seg{{left + b.H, fc}}
	used := 2 + 1 // left+H, right
	if len(title) > 0 {
		segs = append(segs, Seg{" ", fc})
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
		segs = append(segs, Seg{" ", fc})
		used++
	}
	segs = append(segs, Seg{strings.Repeat(b.H, max(w-used, 0)) + right, fc})
	return SegLine(segs...)
}

// boxed wraps one content row in the side borders with one blank of
// padding on each side: "│ text… │". The content is clipped to iw-2
// columns and padded so the right border always lines up.
func boxed(b Box, content Line, iw int) Line { return boxedC(b, content, iw, frameColor) }

// boxedC is boxed in a given frame color.
func boxedC(b Box, content Line, iw int, fc Color) Line {
	segs := []Seg{{b.V + " ", fc}}
	iw -= 2
	if len(content.Segs) > 0 {
		left := iw
		for _, sg := range content.Segs {
			// Stop when the row is full: Fit(_, 0) would mean "no
			// limit" and let the rest spill past the right border (the
			// bug that pushed an event onto the bottom border row).
			if left <= 0 {
				break
			}
			t := Fit(sg.Text, left)
			left -= Width(t)
			segs = append(segs, Seg{t, sg.Color})
		}
		segs = append(segs, Seg{strings.Repeat(" ", left), Default})
	} else {
		t := Fit(content.Text, iw)
		segs = append(segs, Seg{t, content.Color}, Seg{strings.Repeat(" ", iw-Width(t)), Default})
	}
	segs = append(segs, Seg{" " + b.V, fc})
	return SegLine(segs...)
}

// paneHeights splits the rows left for the events pane and the command
// pane (results + input line): the events pane gets split_ratio percent,
// or the size set with Ctrl-G/Ctrl-V, with at least minPane rows each.
// top is the command pane (now at the bottom of the screen), bottom the
// events pane (now at the top); the names are kept from the old layout.
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

// msgFrameColor: the messages pane has its own bright white frame so
// incoming messages stand out.
const msgFrameColor = BrightWhite

// layout holds the pane sizes for one screen size.
type layout struct {
	framed bool
	msg    int // rows inside the messages box (0 = no box)
	top    int // split: command pane rows incl. the input line (bottom of screen)
	bottom int // split: events pane rows (top of screen)
	mixed  int // single pane: rows above the input line
}

// layoutFor computes the panes for rows x cols. Framed, top to bottom:
// a title line, the events box (split only), the white messages box and
// the command box whose last content row is the input line and whose
// bottom border holds the key hints. Every box costs two border rows.
// The messages box keeps s.msgLines rows (min 3) unless the screen is
// too small for it; then it is shrunk or left out.
func (s *Shell) layoutFor(rows, cols int) layout {
	l := layout{framed: s.framed(rows, cols)}
	if !l.framed {
		if s.split {
			l.top, l.bottom = s.paneHeights(max(rows-1, 2))
		} else {
			l.mixed = max(rows-1, 1)
		}
		return l
	}
	// base: title line + command box borders (+ events box borders);
	// panes: the fewest content rows the other boxes need.
	base, panes := 3, 2
	if s.split {
		base, panes = 5, 2*minPane
	}
	m := max(s.msgLines, minPane)
	if rows-base-(m+2) < panes {
		m = rows - base - 2 - panes // shrink to fit
	}
	if m < minPane {
		m = 0 // no room: no messages box
	}
	l.msg = m
	avail := rows - base
	if m > 0 {
		avail -= m + 2
	}
	if s.split {
		l.top, l.bottom = s.paneHeights(avail)
	} else {
		l.mixed = avail - 1
	}
	return l
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
	l := s.layoutFor(s.rows, s.cols)
	inner := l.top + l.bottom
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
	focusMessages = 2
)

// ToggleFocus moves the scroll focus to the next pane, top to bottom:
// events, messages, commands in split view; messages and the single
// pane otherwise. The default focus is the command pane.
func (s *Shell) ToggleFocus() {
	if s.split {
		next := map[int]int{focusEvents: focusMessages, focusMessages: focusCommands, focusCommands: focusEvents}
		s.focus = next[s.focus]
		return
	}
	if s.focus == focusMessages {
		s.focus = focusCommands
	} else {
		s.focus = focusMessages
	}
}

// scrollPane is the pane Ctrl-U/Ctrl-D act on, and its visible height.
func (s *Shell) scrollPane() (*Panel, int) {
	l := s.layoutFor(max(s.rows, minFrameRows), max(s.cols, minFrameCols))
	switch {
	case s.focus == focusMessages:
		return s.msgs, max(l.msg, 1)
	case !s.split:
		return s.mixed, l.mixed
	case s.focus == focusEvents:
		return s.events, l.bottom
	}
	return s.results, l.top - 1
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

// title is the text of the title line at the top of the screen.
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
	return segs
}

// cmdTitle labels the command box (highlighted when it has the scroll
// focus) with its scroll position.
func (s *Shell) cmdTitle() []Seg {
	d := " " + s.box.Dot + " "
	label := Seg{"commands", Dim}
	if s.focus == focusCommands {
		label = Seg{s.focusMark() + "commands", BoldYellow}
	}
	segs := []Seg{label}
	p := s.results
	if !s.split {
		p = s.mixed
	}
	if h := s.scrollHint(p); h != "" {
		segs = append(segs, Seg{d, frameColor}, Seg{h, BoldYellow})
	}
	return segs
}

// hints is the status bar text in the bottom border.
func (s *Shell) hints() []Seg {
	h := "Tab focus  ^U/^D scroll  help  quit"
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
//	── gonje 0.6 · NODEA · 1/1 ───    ── gonje 0.6 · NODEA · 1/1 ───
//	┌─ incoming · L2 · live ────┐    ┌─ messages · 3 ────────────┐
//	│ events                    │    │ messages                  │
//	└───────────────────────────┘    └───────────────────────────┘
//	┌─ messages · 3 ────────────┐    ┌─ commands ────────────────┐
//	│ messages (white frame)    │    │ results and events,       │
//	└───────────────────────────┘    │ interleaved               │
//	┌─ commands ────────────────┐    │ NODEA [1/1]> input_       │
//	│ command results           │    └─ Tab focus  ^U/^D ... ────┘
//	│ NODEA [1/1]> input_       │
//	└─ Tab focus  ^G/^V ... ────┘
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
	f := Frame{Rows: []Line{border(b, b.H, b.H, s.title(), w)}}
	add := func(ls []Line) {
		for _, l := range ls {
			f.Rows = append(f.Rows, boxed(b, l, iw))
		}
	}
	l := s.layoutFor(rows, cols)
	if s.split {
		f.Rows = append(f.Rows, border(b, b.TL, b.TR, s.dividerTitle(l.bottom), w))
		add(s.events.Visible(l.bottom, cw))
		f.Rows = append(f.Rows, border(b, b.BL, b.BR, nil, w))
	}
	if l.msg > 0 {
		f.Rows = append(f.Rows, borderC(b, b.TL, b.TR, s.msgTitle(), w, msgFrameColor))
		now := s.now()
		for _, ml := range s.msgs.Visible(l.msg, cw) {
			f.Rows = append(f.Rows, boxedC(b, s.blinkTag(ml, now), iw, msgFrameColor))
		}
		f.Rows = append(f.Rows, borderC(b, b.BL, b.BR, nil, w, msgFrameColor))
	}
	// The command box at the bottom: results (or the mixed pane), the
	// input line, and the key hints in its bottom border.
	f.Rows = append(f.Rows, border(b, b.TL, b.TR, s.cmdTitle(), w))
	if s.split {
		add(s.results.Visible(l.top-1, cw))
	} else {
		add(s.mixed.Visible(l.mixed, cw))
	}
	in, col := s.inputLine(cw)
	f.CursorRow, f.CursorCol = len(f.Rows), 2+col
	add([]Line{in})
	f.Rows = append(f.Rows, border(b, b.BL, b.BR, s.hints(), w))
	return f
}

// Blinking of new messages: the "MSG"/"RSP" tag blinks for blinkFor.
// SGR 5 is ignored by many terminals, so reverse video is also toggled
// every blinkPhase (the driver redraws while anything blinks).
const (
	blinkFor   = 10 * time.Second
	blinkPhase = 500 * time.Millisecond
)

// blinkTag returns l with its tag segment set to blink (reverse video
// in the "on" half of each phase) while l.BlinkUntil is in the future.
func (s *Shell) blinkTag(l Line, now time.Time) Line {
	if !s.set.Color || len(l.Segs) < 2 || !now.Before(l.BlinkUntil) {
		return l
	}
	age := now.Sub(l.BlinkUntil.Add(-blinkFor))
	c := Blink
	if (age/blinkPhase)%2 == 0 {
		c = BlinkRev
	}
	segs := append([]Seg(nil), l.Segs...)
	segs[1].Color = c
	l.Segs = segs
	return l
}

// Blinking reports whether a message tag is still blinking (the driver
// keeps redrawing every blinkPhase until it stops).
func (s *Shell) Blinking() bool {
	if !s.set.Color {
		return false
	}
	now := s.now()
	for i := len(s.msgs.lines) - 1; i >= 0 && i >= len(s.msgs.lines)-50; i-- {
		if now.Before(s.msgs.lines[i].BlinkUntil) {
			return true
		}
	}
	return false
}

// msgTitle labels the messages box: count, focus, scroll, new arrivals.
func (s *Shell) msgTitle() []Seg {
	d := " " + s.box.Dot + " "
	label := Seg{"messages", BrightWhite}
	if s.focus == focusMessages {
		label = Seg{s.focusMark() + "messages", BoldYellow}
	}
	segs := []Seg{label, {d, msgFrameColor}, {fmt.Sprintf("%d", s.msgs.Len()), BrightWhite}}
	if s.msgs.Scroll() == 0 {
		s.msgNew = false
	}
	if h := s.scrollHint(s.msgs); h != "" {
		segs = append(segs, Seg{d, msgFrameColor}, Seg{h, BoldYellow})
	}
	if s.msgNew {
		segs = append(segs, Seg{d, msgFrameColor}, Seg{"new ↓", BoldGreen})
	}
	return segs
}

// renderBare is the frameless layout for tiny terminals, in the same
// order as the framed one: events, messages (when there are any and the
// screen has 6 rows or more), then the results and the input line.
func (s *Shell) renderBare(rows, w int) Frame {
	s.width = w
	var f Frame
	divider := Line{Text: strings.Repeat(s.box.H, w), Color: frameColor}
	m := 0 // message rows (plus one divider row)
	if rows >= 6 && s.msgs.Len() > 0 {
		m = max(1, min(3, rows/4))
		rows -= m + 1
	}
	msgs := func() {
		if m > 0 {
			f.Rows = append(f.Rows, s.msgs.Visible(m, w)...)
			f.Rows = append(f.Rows, divider)
		}
	}
	if s.split && rows >= 5 {
		top, bottom := s.paneHeights(rows - 1)
		f.Rows = append(f.Rows, s.events.Visible(bottom, w)...)
		f.Rows = append(f.Rows, divider)
		msgs()
		f.Rows = append(f.Rows, s.results.Visible(top-1, w)...)
	} else {
		msgs()
		f.Rows = append(f.Rows, s.mixed.Visible(rows-1, w)...)
	}
	in, col := s.inputLine(w)
	f.CursorRow, f.CursorCol = len(f.Rows), col
	f.Rows = append(f.Rows, in)
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
