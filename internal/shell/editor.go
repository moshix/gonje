// Copyright 2026 by moshix

package shell

import (
	"strings"
	"unicode"
)

// HistoryMax caps the command history.
const HistoryMax = 500

// ActionKind tells the caller what a key press requires.
type ActionKind int

// Editor actions.
const (
	ActNone       ActionKind = iota // buffer may have changed: redraw input
	ActSubmit                       // Line holds the submitted command
	ActRedraw                       // Ctrl-L: redraw the whole screen
	ActScrollUp                     // Ctrl-U: focused pane up half a page
	ActScrollDown                   // Ctrl-D: focused pane down half a page
	ActGrow                         // Ctrl-G / Ctrl-Up: bigger events pane
	ActShrink                       // Ctrl-V / Ctrl-Down: smaller events pane
	ActFocus                        // Tab: move the scroll focus to the other pane
)

// Action is the result of one key.
type Action struct {
	Kind  ActionKind
	Line  string
	Added bool // the submitted line was added to the history
}

// Editor is a terminal-independent readline-style line editor: bytes go
// in through a KeyDecoder, keys go in here, and Render produces what the
// input line should look like. It never touches the terminal, so every
// key is unit-testable.
type Editor struct {
	buf    []rune
	pos    int // cursor index in buf
	scroll int // first visible rune when the line is wider than the panel

	hist    []string
	histIdx int    // == len(hist) while editing a new line
	saved   []rune // in-progress line, kept while browsing history

	kill []rune // last killed text (Ctrl-K/U/W), yanked by Ctrl-Y

	searching bool
	query     []rune
	match     int // history index of the current search match, -1 none
	failed    bool
}

// NewEditor returns an editor with the given history (oldest first).
func NewEditor(hist []string) *Editor {
	e := &Editor{}
	e.SetHistory(hist)
	return e
}

// SetHistory replaces the history.
func (e *Editor) SetHistory(h []string) {
	e.hist = nil
	for _, l := range h {
		e.AddHistory(l)
	}
	e.histIdx = len(e.hist)
}

// History returns a copy of the history, oldest first.
func (e *Editor) History() []string { return append([]string(nil), e.hist...) }

// AddHistory appends a line unless it is blank or repeats the previous
// entry. The history is capped at HistoryMax entries.
func (e *Editor) AddHistory(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	if n := len(e.hist); n > 0 && e.hist[n-1] == line {
		return false
	}
	e.hist = append(e.hist, line)
	if len(e.hist) > HistoryMax {
		e.hist = e.hist[len(e.hist)-HistoryMax:]
	}
	return true
}

// Line returns the current buffer.
func (e *Editor) Line() string { return string(e.buf) }

// Cursor returns the cursor index in runes.
func (e *Editor) Cursor() int { return e.pos }

// Searching reports whether reverse search (Ctrl-R) is active.
func (e *Editor) Searching() bool { return e.searching }

func (e *Editor) setLine(r []rune) {
	e.buf = append([]rune(nil), r...)
	e.pos = len(e.buf)
}

// reset clears the line after submit or Ctrl-C.
func (e *Editor) reset() {
	e.buf, e.pos, e.scroll = nil, 0, 0
	e.histIdx = len(e.hist)
	e.saved = nil
}

// Key applies one key.
func (e *Editor) Key(k Key) Action {
	if e.searching {
		if act, done := e.searchKey(k); done {
			return act
		}
	}
	switch k.Code {
	case KeyRune:
		e.insert([]rune{k.Rune})
	case KeyEnter:
		line := string(e.buf)
		// The history (memory and ~/.gonje_history) never holds a
		// password: it keeps the masked line.
		added := e.AddHistory(maskSecrets(line))
		e.reset()
		return Action{Kind: ActSubmit, Line: line, Added: added}
	case KeyBackspace:
		e.backspace()
	case KeyDelete:
		e.deleteAt()
	case KeyLeft:
		e.pos = max(e.pos-1, 0)
	case KeyRight:
		e.pos = min(e.pos+1, len(e.buf))
	case KeyHome:
		e.pos = 0
	case KeyEnd:
		e.pos = len(e.buf)
	case KeyWordLeft:
		e.pos = e.wordLeft()
	case KeyWordRight:
		e.pos = e.wordRight()
	case KeyUp:
		e.histPrev()
	case KeyDown:
		e.histNext()
	case KeyCtrlUp:
		return Action{Kind: ActGrow}
	case KeyCtrlDown:
		return Action{Kind: ActShrink}
	case KeyCtrl:
		return e.ctrl(k.Rune)
	}
	return Action{}
}

// ctrl handles Ctrl-letter keys (emacs/readline bindings).
func (e *Editor) ctrl(c rune) Action {
	switch c {
	case 'a':
		e.pos = 0
	case 'e':
		e.pos = len(e.buf)
	case 'b':
		e.pos = max(e.pos-1, 0)
	case 'f':
		e.pos = min(e.pos+1, len(e.buf))
	case 'h':
		e.backspace()
	case 'd':
		// Ctrl-U / Ctrl-D scroll the focused pane (user choice); they no
		// longer kill to line start / delete a character.
		return Action{Kind: ActScrollDown}
	case 'u':
		return Action{Kind: ActScrollUp}
	case 'g':
		// Ctrl-G / Ctrl-V resize the split view. (Inside Ctrl-R search,
		// Ctrl-G still cancels the search; that is handled earlier.)
		return Action{Kind: ActGrow}
	case 'v':
		return Action{Kind: ActShrink}
	case 'i': // Tab
		return Action{Kind: ActFocus}
	case 'k':
		if e.pos < len(e.buf) {
			e.kill = append([]rune(nil), e.buf[e.pos:]...)
			e.buf = e.buf[:e.pos]
		}
	case 'w':
		// Kill the previous whitespace-delimited word (unix-word-rubout).
		start := e.pos
		for start > 0 && e.buf[start-1] == ' ' {
			start--
		}
		for start > 0 && e.buf[start-1] != ' ' {
			start--
		}
		if start < e.pos {
			e.kill = append([]rune(nil), e.buf[start:e.pos]...)
			e.buf = append(e.buf[:start], e.buf[e.pos:]...)
			e.pos = start
		}
	case 'y':
		e.insert(e.kill)
	case 't':
		// Transpose the two characters before/at the cursor; at the end
		// of the line, swap the last two.
		if len(e.buf) < 2 || e.pos == 0 {
			break
		}
		p := e.pos
		if p == len(e.buf) {
			p--
		}
		e.buf[p-1], e.buf[p] = e.buf[p], e.buf[p-1]
		e.pos = min(p+1, len(e.buf))
	case 'p':
		e.histPrev()
	case 'n':
		e.histNext()
	case 'l':
		return Action{Kind: ActRedraw}
	case 'c':
		e.reset()
	case 'r':
		e.searching, e.query, e.match, e.failed = true, nil, -1, false
	}
	return Action{}
}

func (e *Editor) insert(r []rune) {
	if len(r) == 0 {
		return
	}
	nb := make([]rune, 0, len(e.buf)+len(r))
	nb = append(nb, e.buf[:e.pos]...)
	nb = append(nb, r...)
	nb = append(nb, e.buf[e.pos:]...)
	e.buf = nb
	e.pos += len(r)
}

func (e *Editor) backspace() {
	if e.pos > 0 {
		e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
		e.pos--
	}
}

func (e *Editor) deleteAt() {
	if e.pos < len(e.buf) {
		e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
	}
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordLeft: skip non-word characters left of the cursor, then the word.
func (e *Editor) wordLeft() int {
	p := e.pos
	for p > 0 && !isWord(e.buf[p-1]) {
		p--
	}
	for p > 0 && isWord(e.buf[p-1]) {
		p--
	}
	return p
}

// wordRight: skip non-word characters, then to the end of the word.
func (e *Editor) wordRight() int {
	p := e.pos
	for p < len(e.buf) && !isWord(e.buf[p]) {
		p++
	}
	for p < len(e.buf) && isWord(e.buf[p]) {
		p++
	}
	return p
}

// histPrev moves to the previous (older) history entry. The line being
// typed is saved when we first leave it and restored by histNext.
func (e *Editor) histPrev() {
	if e.histIdx == 0 {
		return
	}
	if e.histIdx == len(e.hist) {
		e.saved = append([]rune(nil), e.buf...)
	}
	e.histIdx--
	e.setLine([]rune(e.hist[e.histIdx]))
}

// histNext moves to the next (newer) entry, or back to the saved line.
func (e *Editor) histNext() {
	if e.histIdx >= len(e.hist) {
		return
	}
	e.histIdx++
	if e.histIdx == len(e.hist) {
		e.setLine(e.saved)
		e.saved = nil
		return
	}
	e.setLine([]rune(e.hist[e.histIdx]))
}

// searchKey handles a key during Ctrl-R incremental reverse search.
// done=false means the key ends the search and should then be processed
// normally (with the match placed in the buffer).
func (e *Editor) searchKey(k Key) (Action, bool) {
	switch {
	case k.Code == KeyRune:
		e.query = append(e.query, k.Rune)
		e.search(e.startFrom())
		return Action{}, true
	case k.Code == KeyBackspace || (k.Code == KeyCtrl && k.Rune == 'h'):
		if len(e.query) > 0 {
			e.query = e.query[:len(e.query)-1]
		}
		e.search(len(e.hist) - 1)
		return Action{}, true
	case k.Code == KeyCtrl && k.Rune == 'r':
		// Next older match.
		from := len(e.hist) - 1
		if e.match >= 0 {
			from = e.match - 1
		}
		e.search(from)
		return Action{}, true
	case (k.Code == KeyCtrl && (k.Rune == 'g' || k.Rune == 'c')) || k.Code == KeyEscape:
		e.searching = false // cancel: keep the original line
		return Action{}, true
	}
	// Any other key accepts the match.
	e.searching = false
	if e.match >= 0 {
		e.setLine([]rune(e.hist[e.match]))
		e.histIdx = len(e.hist)
	}
	return Action{}, false
}

// startFrom returns where a refined query starts searching: the current
// match (it may still match) or the newest entry.
func (e *Editor) startFrom() int {
	if e.match >= 0 {
		return e.match
	}
	return len(e.hist) - 1
}

// search looks for the query in history entries from index `from` back.
func (e *Editor) search(from int) {
	q := string(e.query)
	for i := from; i >= 0 && i < len(e.hist); i-- {
		if strings.Contains(e.hist[i], q) {
			e.match, e.failed = i, false
			return
		}
	}
	if q == "" {
		e.match, e.failed = -1, false
		return
	}
	e.failed = true
}

// Render returns the input line as it should be drawn in a field of
// width columns (DefaultWidth if width <= 0), and the cursor column
// (0-based).
//
// Long lines scroll horizontally: the visible window keeps the cursor
// inside it, and nothing is ever drawn past the field width.
func (e *Editor) Render(prompt string, width int) (string, int) {
	if width <= 0 {
		width = DefaultWidth
	}
	if e.searching {
		label := "(reverse-i-search)`"
		if e.failed {
			label = "(failed reverse-i-search)`"
		}
		m := ""
		if e.match >= 0 {
			m = e.hist[e.match]
		}
		s := Fit(label+string(e.query)+"': "+m, width)
		return s, min(Width(label)+len(e.query), width-1)
	}
	pw := Width(prompt)
	if pw >= width-1 {
		prompt, pw = "", 0
	}
	avail := width - pw - 1 // keep one column for the cursor at the end
	if avail < 1 {
		avail = 1
	}
	if e.pos < e.scroll {
		e.scroll = e.pos
	}
	if e.pos > e.scroll+avail {
		e.scroll = e.pos - avail
	}
	if e.scroll > len(e.buf) {
		e.scroll = len(e.buf)
	}
	end := min(e.scroll+avail, len(e.buf))
	vis := string(e.buf[e.scroll:end])
	return Fit(prompt+vis, width), pw + e.pos - e.scroll
}
