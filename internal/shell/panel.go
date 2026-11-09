// Copyright 2026 by moshix

package shell

import "fmt"

// PanelMax is the event panel's line buffer size; ResultsMax is the
// scrollback of the command pane (and of the single pane).
const (
	PanelMax   = 500
	ResultsMax = 2000
)

// Panel is a scrollable buffer of the most recent logical lines. Lines
// are stored unwrapped; Visible wraps them for the current width, so a
// wider terminal shows them in full later.
//
// scroll counts how many logical lines the view is moved up from the
// bottom (0 = following the newest line). While the operator is scrolled
// back, new lines do not move the view.
type Panel struct {
	lines  []panelLine
	max    int
	scroll int
}

type panelLine struct {
	Line
	kaNode  string // non-empty for a collapsed keepalive summary line
	kaCount int
}

// NewPanel creates a panel holding at most max lines.
func NewPanel(max int) *Panel { return &Panel{max: max} }

// Len returns the number of buffered lines.
func (p *Panel) Len() int { return len(p.lines) }

// Scroll returns the current scroll offset from the bottom.
func (p *Panel) Scroll() int { return p.scroll }

// Add appends lines, dropping the oldest beyond the limit.
func (p *Panel) Add(lines ...Line) {
	for _, l := range lines {
		p.lines = append(p.lines, panelLine{Line: l})
		if p.scroll > 0 {
			p.scroll++
		}
	}
	p.trim()
}

func (p *Panel) trim() {
	if over := len(p.lines) - p.max; over > 0 {
		p.lines = p.lines[over:]
	}
	if p.scroll > len(p.lines) {
		p.scroll = len(p.lines)
	}
}

// AddKeepalive records one keepalive for node. Consecutive keepalives of
// the same node collapse into one summary line ("keepalive x12 NODEB");
// it returns that line and whether it replaced the previous last line.
func (p *Panel) AddKeepalive(node, clock string) (Line, bool) {
	if n := len(p.lines); n > 0 && p.lines[n-1].kaNode == node {
		last := &p.lines[n-1]
		last.kaCount++
		last.Text = kaText(clock, last.kaCount, node)
		return last.Line, true
	}
	pl := panelLine{Line: Line{Text: kaText(clock, 1, node), Color: Dim}, kaNode: node, kaCount: 1}
	p.lines = append(p.lines, pl)
	if p.scroll > 0 {
		p.scroll++
	}
	p.trim()
	return pl.Line, false
}

func kaText(clock string, n int, node string) string {
	return fmt.Sprintf("%s keepalive x%d %s", clock, n, node)
}

// Visible returns exactly h screen rows for a panel of the given width:
// the newest logical lines (above the scroll position) wrapped to width,
// padded with empty rows at the top when there is not enough text.
func (p *Panel) Visible(h, width int) []Line {
	if h <= 0 {
		return nil
	}
	end := len(p.lines) - p.scroll
	// Collect rows bottom-up: wrap each logical line, take its rows from
	// the last one upwards, stop when the panel is full.
	rev := make([]Line, 0, h)
	for i := end - 1; i >= 0 && len(rev) < h; i-- {
		rows := wrapLine(p.lines[i].Line, width)
		for j := len(rows) - 1; j >= 0 && len(rev) < h; j-- {
			rev = append(rev, rows[j])
		}
	}
	out := make([]Line, h)
	for i, r := range rev {
		out[h-1-i] = r
	}
	return out
}

// ScrollUp moves the view n logical lines back in time, stopping when
// the oldest line reaches the top of a pane of height h.
func (p *Panel) ScrollUp(n, h int) {
	p.scroll = min(p.scroll+max(n, 1), max(len(p.lines)-h, 0))
}

// ScrollDown moves the view n lines towards the newest line; at the
// bottom it follows new lines again ("live").
func (p *Panel) ScrollDown(n int) { p.scroll = max(p.scroll-max(n, 1), 0) }

// Live snaps the view back to the newest line.
func (p *Panel) Live() { p.scroll = 0 }
