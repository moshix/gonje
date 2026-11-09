// Copyright 2026 by moshix

package shell

import (
	"fmt"
	"sync"
)

// Send marks: msg, cmd and send add no result line when they work.
// Instead the echoed command line gets a mark: " …" while the message,
// command or file is on its way, " ✓" once the NMR has been written to
// the link (a file: confirmed with C0, or counted as sent after the EOF
// grace), " ✗" plus an error line when it failed. The link reports from
// its own goroutine, so the result is queued (markQ) and applied on the
// UI goroutine (ApplyMarks), which then redraws.

// Mark states.
const (
	markPending = iota + 1
	markOK
	markFail
)

// sendMark is the status of one echoed command line.
type sendMark struct {
	state   int
	unicode bool
}

// apply returns l with the mark glyph appended (l unchanged without a
// mark).
func (m *sendMark) apply(l Line) Line {
	if m == nil || m.state == 0 {
		return l
	}
	var g Seg
	switch m.state {
	case markPending:
		g = Seg{" …", Dim}
		if !m.unicode {
			g.Text = " ..."
		}
	case markOK:
		g = Seg{" ✓", BoldGreen}
		if !m.unicode {
			g.Text = " [ok]"
		}
	default:
		g = Seg{" ✗", BoldRed}
		if !m.unicode {
			g.Text = " [x]"
		}
	}
	segs := l.Segs
	if len(segs) == 0 {
		segs = []Seg{{l.Text, l.Color}}
	}
	out := SegLine(append(append([]Seg(nil), segs...), g)...)
	out.Color, out.Cont, out.Clip = l.Color, l.Cont, l.Clip
	return out
}

// markUpdate is a send result reported by the link goroutine.
type markUpdate struct {
	m    *sendMark
	what string // "message to NODEB/BOB" (for the error line)
	err  error
}

// marks queues send results for the UI goroutine.
type marks struct {
	mu   sync.Mutex
	q    []markUpdate
	wake chan struct{} // signalled when q gets something (never blocks)
}

// startMark gives the echoed line of the command being submitted a
// pending mark; nil when the command was not typed (Exec from code).
func (s *Shell) startMark() *sendMark {
	m := s.echoMark
	if m != nil {
		m.state = markPending
	}
	return m
}

// notify returns the callback for a send whose line has mark m. It only
// queues the result: safe from any goroutine, never blocks.
func (s *Shell) notify(m *sendMark, what string) func(error) {
	return func(err error) {
		s.mk.mu.Lock()
		s.mk.q = append(s.mk.q, markUpdate{m: m, what: what, err: err})
		s.mk.mu.Unlock()
		select {
		case s.mk.wake <- struct{}{}:
		default:
		}
	}
}

// ApplyMarks sets the queued send results (UI goroutine): ✓, or ✗ plus
// an error line (also returned, for the plain line mode). It reports
// whether anything changed on screen.
func (s *Shell) ApplyMarks() (bool, []Line) {
	s.mk.mu.Lock()
	q := s.mk.q
	s.mk.q = nil
	s.mk.mu.Unlock()
	var errs []Line
	for _, u := range q {
		if u.err == nil {
			if u.m != nil && u.m.state != markFail {
				u.m.state = markOK
			}
			s.set.Log.Info("shell: " + u.what + " sent")
			continue
		}
		if u.m != nil {
			u.m.state = markFail
		}
		l := errText(fmt.Sprintf("%s: %v", u.what, u.err))
		s.addResult(l)
		errs = append(errs, l)
		s.set.Log.Error(fmt.Sprintf("shell: %s: %v", u.what, u.err))
	}
	return len(q) > 0, errs
}

// Wake is signalled when send results are waiting for ApplyMarks.
func (s *Shell) Wake() <-chan struct{} { return s.mk.wake }
