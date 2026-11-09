package shell

import (
	"fmt"

	"gonje/internal/nje/link"
)

// pager shows a command's output one pane-full at a time ("more").
// Lines are logical lines; how many fit on a page is worked out from the
// current pane height and width every time, so a resize in the middle of
// paging simply changes the size of the next page.
type pager struct {
	lines []Line
	next  int // first line not shown yet
}

// rowsOf counts the screen rows lines take at the current width.
func (s *Shell) rowsOf(lines []Line) int {
	n := 0
	for _, l := range lines {
		n += len(wrapLine(l, s.width))
	}
	return n
}

// pageRows is how many output rows fit in the pane that shows command
// output (0 if the screen has not been laid out yet).
func (s *Shell) pageRows() int {
	if s.rows == 0 {
		return 0
	}
	inner := s.innerRows(s.rows)
	if s.split {
		top, _ := s.paneHeights(inner)
		return top - 1 // the bottom row of the top pane is the input line
	}
	return inner - 1
}

// showResult adds a command's output, paging it if it is taller than the
// pane. used = rows already taken on the current page (the echoed
// command line).
func (s *Shell) showResult(lines []Line, used int) {
	h := s.pageRows()
	if h <= 0 || s.rowsOf(lines) <= h-used {
		s.addResult(lines...)
		return
	}
	s.pg = &pager{lines: lines}
	s.addPage(h - used)
}

// addPage adds as many lines as fit in rows rows (at least one).
func (s *Shell) addPage(rows int) {
	p := s.pg
	added := 0
	for p.next < len(p.lines) && rows > 0 {
		n := len(wrapLine(p.lines[p.next], s.width))
		if n > rows && added > 0 {
			break // the next line does not fit: it starts the next page
		}
		s.addResult(p.lines[p.next])
		p.next++
		added++
		rows -= n
	}
	if p.next >= len(p.lines) {
		s.endPager()
	}
}

// Paging reports whether the pager is waiting for C or Q.
func (s *Shell) Paging() bool { return s.pg != nil }

// pagerPercent is how much of the output has been shown.
func (s *Shell) pagerPercent() int {
	total := s.rowsOf(s.pg.lines)
	if total == 0 {
		return 100
	}
	return s.rowsOf(s.pg.lines[:s.pg.next]) * 100 / total
}

// pagerLine is the highlighted prompt shown instead of the input line.
func (s *Shell) pagerLine() Line {
	return SegLine(Seg{fmt.Sprintf("-- more (%d%%) -- C continue, Q quit --", s.pagerPercent()), BoldYellow})
}

// PagerKey handles one key byte while paging: C, c, Space or Enter show
// the next page; Q, q or Esc stop (the rest is skipped). Other keys are
// ignored. It returns false when not paging.
func (s *Shell) PagerKey(b byte) bool {
	if s.pg == nil {
		return false
	}
	switch b {
	case 'c', 'C', ' ', '\r', '\n':
		s.addPage(s.pageRows())
	case 'q', 'Q', 0x1b:
		s.addResult(Line{Text: fmt.Sprintf("(%d more line(s) skipped)", len(s.pg.lines)-s.pg.next), Color: Dim})
		s.endPager()
	}
	return true
}

// endPager stops paging and shows the events that were held back so
// they would not interleave with the output (single-pane mode).
func (s *Shell) endPager() {
	s.pg = nil
	held := s.held
	s.held = nil
	for _, ev := range held {
		s.HandleEvent(ev)
	}
}

// holdEvent keeps an event back while paging in single-pane mode.
func (s *Shell) holdEvent(ev link.Event) bool {
	if s.pg == nil || s.split {
		return false
	}
	s.held = append(s.held, ev)
	return true
}
