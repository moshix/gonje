// Copyright 2026 by moshix

package shell

import (
	"strings"
	"testing"
)

// feed decodes raw terminal bytes and applies them to the editor,
// returning the last action.
func feed(e *Editor, input string) Action {
	var d KeyDecoder
	var last Action
	for _, b := range []byte(input) {
		for _, k := range d.Feed(b) {
			last = e.Key(k)
		}
	}
	return last
}

// Prompt is a sample prompt for the editor render tests.
const Prompt = "NODEA [1/1]> "

func TestKeyDecoder(t *testing.T) {
	cases := []struct {
		in   string
		want Key
	}{
		{"a", Key{Code: KeyRune, Rune: 'a'}},
		{"\r", Key{Code: KeyEnter}},
		{"\n", Key{Code: KeyEnter}},
		{"\x7f", Key{Code: KeyBackspace}},
		{"\x08", Key{Code: KeyBackspace}},
		{"\x01", Key{Code: KeyCtrl, Rune: 'a'}},
		{"\x12", Key{Code: KeyCtrl, Rune: 'r'}},
		{"\x1b[A", Key{Code: KeyUp}},
		{"\x1b[B", Key{Code: KeyDown}},
		{"\x1b[C", Key{Code: KeyRight}},
		{"\x1b[D", Key{Code: KeyLeft}},
		{"\x1bOA", Key{Code: KeyUp}},
		{"\x1b[H", Key{Code: KeyHome}},
		{"\x1b[F", Key{Code: KeyEnd}},
		{"\x1bOH", Key{Code: KeyHome}},
		{"\x1b[1~", Key{Code: KeyHome}},
		{"\x1b[4~", Key{Code: KeyEnd}},
		{"\x1b[3~", Key{Code: KeyDelete}},
		{"\x1b[5~", Key{Code: KeyPgUp}},
		{"\x1b[6~", Key{Code: KeyPgDn}},
		{"\x1b[1;5A", Key{Code: KeyCtrlUp}},
		{"\x1b[1;5B", Key{Code: KeyCtrlDown}},
		{"\x1b[1;5C", Key{Code: KeyWordRight}},
		{"\x1b[1;5D", Key{Code: KeyWordLeft}},
		{"\x1bb", Key{Code: KeyWordLeft}},
		{"\x1bf", Key{Code: KeyWordRight}},
		{"é", Key{Code: KeyRune, Rune: 'é'}},
	}
	for _, c := range cases {
		var d KeyDecoder
		var keys []Key
		for _, b := range []byte(c.in) {
			keys = append(keys, d.Feed(b)...)
		}
		if len(keys) != 1 || keys[0] != c.want {
			t.Errorf("%q: got %+v, want %+v", c.in, keys, c.want)
		}
	}
	// A lone ESC followed by an ordinary key yields both keys (N4).
	var d KeyDecoder
	var keys []Key
	for _, b := range []byte("\x1bxy") {
		keys = append(keys, d.Feed(b)...)
	}
	want := []Key{{Code: KeyEscape}, {Code: KeyRune, Rune: 'x'}, {Code: KeyRune, Rune: 'y'}}
	if len(keys) != 3 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Fatalf("ESC x y: %+v", keys)
	}
}

// TestEditorKeys covers every editing key. Each case types `setup`, then
// `keys`, and checks the buffer and cursor.
func TestEditorKeys(t *testing.T) {
	const (
		left   = "\x1b[D"
		right  = "\x1b[C"
		home   = "\x1b[H"
		end    = "\x1b[F"
		del    = "\x1b[3~"
		cleft  = "\x1b[1;5D"
		cright = "\x1b[1;5C"
	)
	cases := []struct {
		name, setup, keys, want string
		cursor                  int
	}{
		{"insert", "", "abc", "abc", 3},
		{"ctrl-a", "abc", "\x01", "abc", 0},
		{"home", "abc", home, "abc", 0},
		{"ctrl-e", "abc", "\x01\x05", "abc", 3},
		{"end", "abc", home + end, "abc", 3},
		{"ctrl-b", "abc", "\x02", "abc", 2},
		{"left", "abc", left + left, "abc", 1},
		{"left at start", "ab", home + left, "ab", 0},
		{"ctrl-f", "abc", "\x01\x06", "abc", 1},
		{"right", "abc", home + right + right, "abc", 2},
		{"right at end", "ab", right, "ab", 2},
		{"insert middle", "ac", left + "b", "abc", 2},
		{"alt-b", "one two three", "\x1bb", "one two three", 8},
		{"alt-b twice", "one two three", "\x1bb\x1bb", "one two three", 4},
		{"alt-f", "one two three", "\x01\x1bf", "one two three", 3},
		{"alt-f twice", "one two three", "\x01\x1bf\x1bf", "one two three", 7},
		{"ctrl-left", "one two", cleft, "one two", 4},
		{"ctrl-right", "one two", home + cright, "one two", 3},
		{"backspace", "abc", "\x7f", "ab", 2},
		{"ctrl-h", "abc", "\x08", "ab", 2},
		{"backspace middle", "abc", left + "\x7f", "ac", 1},
		{"backspace at start", "abc", home + "\x7f", "abc", 0},
		{"delete", "abc", home + del, "bc", 0},
		{"delete at end", "abc", del, "abc", 3},
		{"ctrl-d does not edit", "abc", home + "\x04", "abc", 0},
		{"ctrl-k", "hello world", home + "\x1bf\x0b", "hello", 5},
		{"ctrl-u does not edit", "hello world", "\x1bb\x15", "hello world", 6},
		{"ctrl-g/ctrl-v do not edit", "abc", "\x07\x16", "abc", 3},
		{"ctrl-w", "msg nodeb bob", "\x17", "msg nodeb ", 10},
		{"ctrl-w spaces", "one two  ", "\x17", "one ", 4},
		{"ctrl-y after ctrl-k", "hello world", "\x1bb\x0b\x01\x19", "worldhello ", 5},
		{"ctrl-y after ctrl-w", "abc def", "\x17\x19\x19", "abc defdef", 10},
		{"ctrl-t middle", "abcd", left + left + "\x14", "acbd", 3},
		{"ctrl-t end", "abcd", "\x14", "abdc", 4},
		{"ctrl-t start", "abcd", home + "\x14", "abcd", 0},
		{"ctrl-c discards", "abc", "\x03", "", 0},
		{"utf8", "", "héllo", "héllo", 5},
	}
	for _, c := range cases {
		e := NewEditor(nil)
		feed(e, c.setup)
		feed(e, c.keys)
		if e.Line() != c.want || e.Cursor() != c.cursor {
			t.Errorf("%s: got %q cursor %d, want %q cursor %d", c.name, e.Line(), e.Cursor(), c.want, c.cursor)
		}
	}
}

func TestEditorActions(t *testing.T) {
	e := NewEditor(nil)
	actions := []struct {
		keys string
		want ActionKind
	}{
		{"\x0c", ActRedraw},
		{"\x15", ActScrollUp},   // Ctrl-U
		{"\x04", ActScrollDown}, // Ctrl-D
		{"\x07", ActGrow},       // Ctrl-G
		{"\x16", ActShrink},     // Ctrl-V
		{"\x1b[1;5A", ActGrow},  // Ctrl-Up
		{"\x1b[1;5B", ActShrink},
		{"\t", ActFocus},     // Tab
		{"\x1b[5~", ActNone}, // PgUp/PgDn are no longer bound
		{"\x1b[6~", ActNone},
	}
	for _, c := range actions {
		if a := feed(e, c.keys); a.Kind != c.want {
			t.Errorf("%q: action %d, want %d", c.keys, a.Kind, c.want)
		}
	}
	// Ctrl-G inside reverse search still cancels the search.
	feed(e, "\x12no")
	if a := feed(e, "\x07"); a.Kind != ActNone || e.Searching() {
		t.Fatal("ctrl-g in search")
	}
	a := feed(e, "nodes\r")
	if a.Kind != ActSubmit || a.Line != "nodes" || !a.Added || e.Line() != "" {
		t.Fatalf("submit %+v", a)
	}
	if a := feed(e, "nodes\r"); a.Added {
		t.Fatal("consecutive duplicate added to history")
	}
	if a := feed(e, "   \r"); a.Kind != ActSubmit || a.Added {
		t.Fatal("blank line added to history")
	}
	if h := e.History(); len(h) != 1 {
		t.Fatalf("history %q", h)
	}
}

func TestHistoryNavigation(t *testing.T) {
	e := NewEditor([]string{"one", "two", "three"})
	const up, down = "\x1b[A", "\x1b[B"
	feed(e, "draft")
	steps := []struct {
		keys, want string
	}{
		{up, "three"},
		{up, "two"},
		{"\x10", "one"}, // Ctrl-P
		{up, "one"},     // stays at the oldest
		{down, "two"},
		{"\x0e", "three"}, // Ctrl-N
		{down, "draft"},   // in-progress line restored
		{down, "draft"},   // nothing newer
	}
	for i, s := range steps {
		feed(e, s.keys)
		if e.Line() != s.want || e.Cursor() != len([]rune(s.want)) {
			t.Fatalf("step %d: got %q want %q", i, e.Line(), s.want)
		}
	}
	// Editing a recalled entry does not change the history.
	feed(e, "\x03"+up+"X\r")
	if h := e.History(); h[len(h)-2] != "three" || h[len(h)-1] != "threeX" {
		t.Fatalf("history %q", h)
	}
}

func TestHistoryCap(t *testing.T) {
	e := NewEditor(nil)
	for i := 0; i < HistoryMax+20; i++ {
		e.AddHistory(strings.Repeat("x", i%7+1) + string(rune('a'+i%26)))
	}
	if len(e.History()) != HistoryMax {
		t.Fatalf("history len %d", len(e.History()))
	}
}

func TestReverseSearch(t *testing.T) {
	e := NewEditor([]string{"msg nodeb bob hi", "nodes", "cmd nodeb q sys", "ls"})
	feed(e, "\x12nodeb")
	if !e.Searching() {
		t.Fatal("not searching")
	}
	r, _ := e.Render(Prompt, 79)
	if !strings.Contains(r, "(reverse-i-search)`nodeb': cmd nodeb q sys") {
		t.Fatalf("render %q", r)
	}
	feed(e, "\x12") // next older match
	r, _ = e.Render(Prompt, 79)
	if !strings.Contains(r, "msg nodeb bob hi") {
		t.Fatalf("older match %q", r)
	}
	feed(e, "\x12zzz")
	if r, _ = e.Render(Prompt, 79); !strings.HasPrefix(r, "(failed reverse-i-search)") {
		t.Fatalf("failed search %q", r)
	}
	feed(e, "\x7f\x7f\x7f") // back to "nodeb"
	a := feed(e, "\r")
	if a.Kind != ActSubmit || !strings.Contains(a.Line, "nodeb") || e.Searching() {
		t.Fatalf("accept %+v", a)
	}
	// Ctrl-G cancels and keeps the original line.
	feed(e, "abc\x12no\x07")
	if e.Searching() || e.Line() != "abc" {
		t.Fatalf("cancel: %q", e.Line())
	}
	// A movement key accepts the match into the buffer.
	feed(e, "\x03\x12ls\x05")
	if e.Searching() || e.Line() != "ls" {
		t.Fatalf("accept by movement: %q", e.Line())
	}
}

func TestRenderHorizontalScroll(t *testing.T) {
	e := NewEditor(nil)
	long := strings.Repeat("0123456789", 20) // 200 characters
	feed(e, long)
	for _, width := range []int{79, 40, 120, 250} {
		text, col := e.Render(Prompt, width)
		lim := width
		if Width(text) > lim || col >= lim || col < Width(Prompt) {
			t.Fatalf("width %d: text %d cols, cursor %d", width, Width(text), col)
		}
		if !strings.HasSuffix(text, "9") {
			t.Fatalf("end of line not visible: %q", text)
		}
	}
	feed(e, "\x01") // home: view scrolls back to the start
	text, col := e.Render(Prompt, 79)
	if !strings.HasPrefix(text, Prompt+"0123") || col != Width(Prompt) {
		t.Fatalf("home view %q col %d", text, col)
	}
	// Move right through the whole line: cursor always visible, <= 79.
	for i := 0; i < 200; i++ {
		feed(e, "\x06")
		text, col = e.Render(Prompt, 79)
		if Width(text) > 79 || col > 78 {
			t.Fatalf("pos %d: width %d col %d", i, Width(text), col)
		}
		vis := []rune(text)[Width(Prompt):]
		cur := col - Width(Prompt)
		if cur < len(vis) && vis[cur] != rune(long[e.Cursor()]) {
			t.Fatalf("pos %d: cursor shows %q, want %q", i, vis[cur], long[e.Cursor()])
		}
	}
}
