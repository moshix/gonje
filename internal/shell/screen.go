// Package shell is gonje's operator interface: a classic line-mode
// console and a split view (input/results on top, live events below).
//
// Lines are stored unwrapped and are laid out for the current terminal
// width only when they are drawn, so a resize re-flows everything. Every
// line that reaches the terminal goes through Fit/Wrap, which also
// replace control characters (a remote node must not be able to send
// escape sequences to the operator's terminal).
package shell

import (
	"strings"
	"unicode/utf8"
)

// DefaultWidth is used when the terminal size cannot be determined.
const DefaultWidth = 80

// Color of a line.
type Color int

// Colors (PLAN: green OK, red error, cyan MSG, magenta CMD/RSP, green
// FILE, yellow LINK).
const (
	Default Color = iota
	Green
	Red
	Cyan
	Magenta
	Yellow
	Dim
	Blue
	Bold
	BoldCyan
	BoldGreen
	BoldRed
	BoldYellow
)

// ansi returns the SGR sequence for a color.
func (c Color) ansi() string {
	switch c {
	case Green:
		return "\x1b[32m"
	case Red:
		return "\x1b[31m"
	case Cyan:
		return "\x1b[36m"
	case Magenta:
		return "\x1b[35m"
	case Yellow:
		return "\x1b[33m"
	case Dim:
		return "\x1b[2m"
	case Blue:
		return "\x1b[34m"
	case Bold:
		return "\x1b[1m"
	case BoldCyan:
		return "\x1b[1;36m"
	case BoldGreen:
		return "\x1b[1;32m"
	case BoldRed:
		return "\x1b[1;31m"
	case BoldYellow:
		return "\x1b[1;33m"
	}
	return ""
}

const ansiReset = "\x1b[0m"

// Line is one logical output line. Cont is the prefix for continuation
// rows when the line is wrapped to the screen width. A line may be made
// of colored segments (Segs); Text is then their concatenation, which is
// what gets logged (the log never sees colors) and measured.
type Line struct {
	Text  string
	Color Color
	Cont  string
	Segs  []Seg
}

// Seg is a colored piece of a Line.
type Seg struct {
	Text  string
	Color Color
}

// SegLine builds a multi-colored line.
func SegLine(segs ...Seg) Line {
	var sb strings.Builder
	for _, sg := range segs {
		sb.WriteString(sg.Text)
	}
	return Line{Text: sb.String(), Segs: segs}
}

// isControl reports characters that must never reach the terminal raw.
func isControl(r rune) bool {
	return r < 0x20 || (r >= 0x7F && r < 0xA0) || r == utf8.RuneError
}

// sanitize replaces control characters (ESC, CR, LF, ...) with '.'.
func sanitize(s string) string {
	if strings.IndexFunc(s, isControl) < 0 {
		return s
	}
	var sb strings.Builder
	for _, r := range s {
		if isControl(r) {
			r = '.'
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// Fit sanitizes s and, when width > 0, truncates it to width columns
// (one column per rune; gonje text is Latin-1). width <= 0 means no
// limit.
func Fit(s string, width int) string {
	s = sanitize(s)
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return s
	}
	return string([]rune(s)[:width])
}

// Width returns the number of columns s occupies.
func Width(s string) int { return utf8.RuneCountInString(s) }

// Wrap splits s into rows of at most width columns, breaking at blanks
// where possible. Continuation rows are prefixed with indent (which
// counts toward the width). width <= 0 returns s as one row.
func Wrap(s string, width int, indent string) []string {
	if width <= 0 {
		return []string{sanitize(s)}
	}
	if Width(indent) >= width/2 {
		indent = ""
	}
	r := []rune(sanitize(s))
	var out []string
	prefix := []rune{}
	for {
		room := width - len(prefix)
		if len(r) <= room {
			return append(out, string(append(prefix, r...)))
		}
		cut := room
		// Prefer breaking at the last blank in the second half of the row.
		for i := room; i > room/2; i-- {
			if r[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(append(prefix, r[:cut]...)), " "))
		r = r[cut:]
		for len(r) > 0 && r[0] == ' ' {
			r = r[1:]
		}
		if len(r) == 0 {
			return out
		}
		prefix = []rune(indent)
	}
}

// wrapLine wraps a logical line into screen rows of the given width. A
// segmented line that fits keeps its colors; one that must be wrapped
// falls back to its base color.
func wrapLine(l Line, width int) []Line {
	if len(l.Segs) > 0 && (width <= 0 || Width(l.Text) <= width) {
		return []Line{l}
	}
	rows := Wrap(l.Text, width, l.Cont)
	out := make([]Line, len(rows))
	for i, r := range rows {
		out[i] = Line{Text: r, Color: l.Color}
	}
	return out
}

// render returns the (optionally colored) form of one screen row, cut
// to width (width <= 0: no limit).
func render(l Line, width int, color bool) string {
	if len(l.Segs) == 0 {
		return paint(Fit(l.Text, width), l.Color, color)
	}
	var sb strings.Builder
	left := width
	for _, sg := range l.Segs {
		t := sanitize(sg.Text)
		if width > 0 {
			if left <= 0 {
				break
			}
			t = Fit(t, left)
			left -= Width(t)
		}
		sb.WriteString(paint(t, sg.Color, color))
	}
	return sb.String()
}

// renderPad is render followed by blanks up to exactly width columns
// (used inside frames so the right border always lines up).
func renderPad(l Line, width int, color bool) string {
	out := render(l, width, color)
	if n := width - min(Width(l.Text), width); n > 0 {
		out += strings.Repeat(" ", n)
	}
	return out
}

// paint wraps text in a color sequence when colors are on.
func paint(text string, c Color, color bool) string {
	if !color || c == Default || text == "" {
		return text
	}
	return c.ansi() + text + ansiReset
}
