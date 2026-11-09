// Copyright 2026 by moshix

package shell

import (
	"fmt"
	"strings"
	"testing"

	"gonje/internal/nje/link"
)

func numbered(n int) []Line {
	var ls []Line
	for i := 1; i <= n; i++ {
		ls = append(ls, Line{Text: fmt.Sprintf("line %03d", i)})
	}
	return ls
}

// lastResult returns the text of the newest line in the results pane.
func lastResult(s *Shell) string { return s.results.lines[len(s.results.lines)-1].Text }

func TestPagerPageBreaks(t *testing.T) {
	for _, rows := range []int{10, 20, 41} {
		for _, split := range []bool{true, false} {
			s, _ := newTestShell(t)
			s.split = split
			s.Render(rows, 100)
			h := s.pageRows()
			s.showResult(numbered(100), 1)
			first := max(h-1, 1) // the echo line uses one row; at least one line
			if !s.Paging() || lastResult(s) != fmt.Sprintf("line %03d", first) {
				t.Fatalf("rows %d split %v: first page ends at %q, want line %03d", rows, split, lastResult(s), first)
			}
			if pl := s.pagerLine().Text; !strings.HasPrefix(pl, "-- more (") || !strings.HasSuffix(pl, "-- C continue, Q quit --") {
				t.Fatalf("pager prompt %q", pl)
			}
			// The pager prompt replaces the input line, inside the frame.
			f := s.Render(rows, 100)
			if !strings.Contains(f.Rows[f.CursorRow].Text, "-- more (") {
				t.Fatalf("prompt row %q", f.Rows[f.CursorRow].Text)
			}
			s.PagerKey('c')
			if lastResult(s) != fmt.Sprintf("line %03d", first+h) {
				t.Fatalf("second page ends at %q", lastResult(s))
			}
			for s.Paging() {
				s.PagerKey(' ')
			}
			if lastResult(s) != "line 100" {
				t.Fatalf("last page %q", lastResult(s))
			}
		}
	}
}

func TestPagerQuitAndShortOutput(t *testing.T) {
	s, _ := newTestShell(t)
	s.Render(24, 100)
	s.showResult(numbered(3), 1) // fits: no paging
	if s.Paging() || lastResult(s) != "line 003" {
		t.Fatal("short output paged")
	}
	s.showResult(numbered(100), 1)
	s.PagerKey('x') // ignored
	if !s.Paging() {
		t.Fatal("other key ended the pager")
	}
	s.PagerKey('Q')
	if s.Paging() || !strings.Contains(lastResult(s), "more line(s) skipped") {
		t.Fatalf("quit: %q", lastResult(s))
	}
	for _, r := range s.results.lines {
		if r.Text == "line 100" {
			t.Fatal("skipped lines were shown")
		}
	}
	s.showResult(numbered(100), 1)
	s.PagerKey(0x1b) // Esc
	if s.Paging() {
		t.Fatal("Esc did not stop the pager")
	}
	if s.PagerKey('c') {
		t.Fatal("PagerKey consumed a key while not paging")
	}
}

func TestPagerResizeMidPage(t *testing.T) {
	s, _ := newTestShell(t)
	s.Render(20, 100)
	s.showResult(numbered(200), 1)
	s.Render(40, 100) // the terminal grew
	h := s.pageRows()
	before := s.pg.next
	s.PagerKey('c')
	if s.pg.next-before != h {
		t.Fatalf("page after resize has %d lines, want %d", s.pg.next-before, h)
	}
}

// TestPagerHoldsEvents: in single-pane mode events wait until the pager
// ends; in split view they go to the events pane at once.
func TestPagerHoldsEvents(t *testing.T) {
	s, _ := newTestShell(t)
	s.split = false
	s.Render(20, 100)
	s.showResult(numbered(100), 1)
	s.HandleEvent(ev(link.EvLink, 2, "during paging"))
	for _, l := range s.mixed.lines {
		if strings.Contains(l.Text, "during paging") {
			t.Fatal("event interleaved with paged output")
		}
	}
	s.PagerKey('q')
	if !strings.Contains(s.mixed.lines[len(s.mixed.lines)-1].Text, "during paging") {
		t.Fatal("held event not shown after the pager")
	}
	s.split = true
	s.showResult(numbered(100), 1)
	s.HandleEvent(ev(link.EvLink, 2, "split event"))
	if !strings.Contains(s.events.lines[len(s.events.lines)-1].Text, "split event") {
		t.Fatal("split view event held")
	}
}
