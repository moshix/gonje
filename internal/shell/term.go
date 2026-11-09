package shell

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"time"

	"golang.org/x/term"

	"gonje/internal/nje/link"
)

// Run drives the shell until the operator quits or done is closed.
// On a terminal it takes over the screen (alternate screen buffer, raw
// mode, framed panes); otherwise it reads plain lines from in and
// writes plain lines to out.
func (s *Shell) Run(in, out *os.File, events <-chan link.Event, done <-chan struct{}) error {
	if term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd())) {
		return s.runTTY(in, out, events, done)
	}
	return s.runPlain(in, out, events, done)
}

// ---------------------------------------------------------------------
// Plain (non-tty) mode: line in, lines out, no colors, no frames, no
// width limit.
// ---------------------------------------------------------------------

func (s *Shell) runPlain(in io.Reader, out io.Writer, events <-chan link.Event, done <-chan struct{}) error {
	w := bufio.NewWriter(out)
	put := func(lines []Line) {
		for _, l := range lines {
			w.WriteString(render(l, 0, false) + "\n")
		}
		w.Flush()
	}
	put(s.Banner())
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				// End of input: keep running (daemon style) until signalled.
				lines = nil
				continue
			}
			res := s.Exec(l)
			put(res.Lines)
			if res.Split != 0 {
				s.set.Log.Error("shell: split view needs a terminal")
				put([]Line{{Text: "split view needs a terminal", Color: Red}})
			}
			if res.Quit {
				return nil
			}
		case ev := <-events:
			if s.HandleEvent(ev) {
				// Print what was just added (keepalives collapse on
				// screen only; here each one is a line).
				put(FormatEventPlain(ev))
			}
		case <-done:
			return nil
		}
	}
}

// FormatEventPlain is the plain-mode form of an event.
func FormatEventPlain(ev link.Event) []Line {
	if ev.Kind == link.EvKeepalive {
		return []Line{{Text: ev.Time.Format("15:04:05") + " keepalive " + ev.Node}}
	}
	return FormatEvent(ev)
}

// ---------------------------------------------------------------------
// TTY mode
// ---------------------------------------------------------------------

// Terminal control sequences.
const (
	altScreenOn  = "\x1b[?1049h\x1b[2J\x1b[H"
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
	clearScreen  = "\x1b[2J"
)

// tty is the raw-mode terminal driver. Everything it draws comes from
// the pure Render function (frames, panes, input line), so what it does
// here is only: read keys, feed events, and paint frames.
type tty struct {
	s          *Shell
	w          *bufio.Writer
	fd         int
	rows, cols int
	dirty      bool
}

func (s *Shell) runTTY(in, out *os.File, events <-chan link.Event, done <-chan struct{}) error {
	fd := int(in.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return s.runPlain(in, out, events, done)
	}
	t := &tty{s: s, w: bufio.NewWriter(out), fd: int(out.Fd())}
	// Restore the terminal however we leave: normal return, the done
	// channel (signals), or a panic unwinding through this function.
	defer func() {
		t.w.WriteString(cursorShow + altScreenOff)
		t.w.Flush()
		term.Restore(fd, old)
	}()
	t.w.WriteString(altScreenOn)
	t.updateSize()
	s.addResult(s.Banner()...)
	t.draw()

	keys := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := in.Read(buf)
			if err != nil {
				close(keys)
				return
			}
			keys <- append([]byte(nil), buf[:n]...)
		}
	}()

	var dec KeyDecoder
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	tick := 0
	for {
		select {
		case data, ok := <-keys:
			if !ok {
				return nil
			}
			for _, b := range data {
				// While paging, keys answer the pager (C/Q), not the editor.
				if s.Paging() {
					if b == 0x0c {
						t.w.WriteString(clearScreen)
					}
					s.PagerKey(b)
					continue
				}
				for _, k := range dec.Feed(b) {
					if t.action(s.editor.Key(k)) {
						return nil
					}
				}
			}
			t.draw()
		case ev := <-events:
			if s.HandleEvent(ev) {
				t.dirty = true
			}
		case <-ticker.C:
			// Poll the window size (portable; no SIGWINCH needed) and
			// repaint after a resize, for new events, or when a divider
			// note expires.
			tick++
			if tick%6 == 0 && t.updateSize() {
				t.w.WriteString(clearScreen)
				t.dirty = true
			}
			if tick%25 == 0 && s.noteText != "" {
				t.dirty = true
			}
			if t.dirty {
				t.draw()
			}
		case <-done:
			return nil
		}
	}
}

// updateSize reads the terminal size; it reports whether it changed.
func (t *tty) updateSize() bool {
	c, r, err := term.GetSize(t.fd)
	if err != nil || c <= 0 || r <= 0 {
		c, r = DefaultWidth, 24
	}
	changed := c != t.cols || r != t.rows
	t.cols, t.rows = c, r
	return changed
}

// action handles an editor action; it returns true to quit.
func (t *tty) action(a Action) bool {
	s := t.s
	switch a.Kind {
	case ActRedraw:
		t.w.WriteString(clearScreen)
	case ActScrollUp:
		s.Scroll(-1)
	case ActScrollDown:
		s.Scroll(1)
	case ActFocus:
		s.ToggleFocus()
	case ActGrow:
		s.Resize(1)
	case ActShrink:
		s.Resize(-1)
	case ActSubmit:
		if a.Added {
			AppendHistory(s.set.HistoryPath, a.Line)
		}
		res := s.submit(a.Line)
		switch {
		case res.Split > 0 && !s.split:
			s.split = true
			s.addResult(ok("split view"))
		case res.Split < 0 && s.split:
			s.split = false
			s.addResult(ok("single pane"))
		}
		t.w.WriteString(clearScreen)
		if res.Quit {
			return true
		}
	}
	return false
}

// draw repaints the whole screen from Render.
func (t *tty) draw() {
	f := t.s.Render(t.rows, t.cols)
	w := usableWidth(t.cols)
	t.w.WriteString(cursorHide)
	for i, row := range f.Rows {
		if i >= t.rows {
			break
		}
		t.w.WriteString("\x1b[" + strconv.Itoa(i+1) + ";1H")
		t.w.WriteString(renderPad(row, w, t.s.set.Color))
		t.w.WriteString("\x1b[K")
	}
	t.w.WriteString("\x1b[" + strconv.Itoa(f.CursorRow+1) + ";" + strconv.Itoa(f.CursorCol+1) + "H" + cursorShow)
	t.w.Flush()
	t.dirty = false
}
