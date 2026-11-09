// Copyright 2026 by moshix

package node

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gonje/internal/nje/link"
)

// DLE STX framing per remote (the link layer explains it in
// nje/link/framing.go). Every known peer uses DLE STX, so that is the
// default; the rest is a bounded safety net:
//
//	1. after a rejection: the other form, once (r.next)
//	2. a form confirmed earlier (r.learned, from gonje.state)
//	3. DLE STX
//
// One alternation per connect window at most (r.triedOther): when the
// other form is rejected too, gonje goes back to DLE STX and keeps it.
// Retries after a rejection wait at least RejectRetry (5 s). A
// confirmed form is stored in gonje.state; "none" only when the peer
// itself sent buffers without DLE STX (mirrored), never on a guess:
//
//	ZVM72MSH.dlestx=yes confirmed=2026-11-09T20:35:35Z

// StateFile is the name of the framing state file (next to gonje.pid).
const StateFile = "gonje.state"

// framingLocked fills the framing parameters of an attempt (n.mu held).
func (n *Node) framingLocked(r *remote, p *link.Params) {
	set := func(dle bool, src, why string) {
		p.Framing = link.FramingNone
		if dle {
			p.Framing = link.FramingDLESTX
		}
		p.FramingSrc, p.FramingWhy = src, why
	}
	switch {
	case r.next != nil:
		set(*r.next, link.FramingGuessed, "the other form was rejected")
	case r.learned != nil:
		set(*r.learned, link.FramingLearned, "confirmed earlier")
	default:
		p.Framing, p.FramingSrc, p.FramingWhy = link.FramingAuto, link.FramingGuessed, "default"
	}
}

// framingFailed handles a signon that failed. If the peer rejected our
// I, the other form is tried once per connect window (then DLE STX
// again), a remembered form that failed is forgotten, and the retry
// waits at least RejectRetry: the returned delay for attemptFailed
// (0 = normal backoff).
func (n *Node) framingFailed(r *remote, s *link.Session) time.Duration {
	if !s.FramingRejected() {
		return 0
	}
	dle, _, _ := s.Framing()
	n.mu.Lock()
	var text string
	switch {
	case !r.triedOther:
		other := !dle
		r.next, r.triedOther = &other, true
		text = fmt.Sprintf("framing: %s rejected, next attempt uses %s", link.FormName(dle), link.FormName(other))
	default:
		// Both forms failed: the problem is not the framing. Stay with
		// DLE STX (the form every known peer uses).
		r.next = nil
		text = fmt.Sprintf("framing: %s rejected too, back to DLE STX", link.FormName(dle))
	}
	forget := r.learned != nil && *r.learned == dle
	if forget {
		r.learned = nil
	}
	retry := max(n.backoffLocked(r), n.opts.RejectRetry)
	n.mu.Unlock()
	n.log.Warn(r.cfg.Name + ": " + text)
	n.emit(link.EvLink, link.L2, r.cfg.Name, text)
	if forget {
		n.saveFraming("")
	}
	return retry
}

// framingConfirmed records the form of a session that signed on. A
// guessed "none" is not stored: only a form the peer used itself.
func (n *Node) framingConfirmed(r *remote, s *link.Session) {
	dle, src, _ := s.Framing()
	n.mu.Lock()
	r.next, r.triedOther = nil, false
	if !dle && src != link.FramingMirrored {
		n.mu.Unlock()
		return
	}
	changed := r.learned == nil || *r.learned != dle
	r.learned = &dle
	n.mu.Unlock()
	if changed {
		text := fmt.Sprintf("framing: %s confirmed (%s)", link.FormName(dle), src)
		n.log.Info(r.cfg.Name + ": " + text)
		n.emit(link.EvLink, link.L2, r.cfg.Name, text)
	}
	n.saveFraming(r.cfg.Name) // also refreshes the confirmed time
}

// framingText describes the current form of a link for "stats".
func (r *remote) framingText() string {
	switch {
	case r.sess != nil:
		return r.sess.Stats().Framing
	case r.next != nil:
		return link.FormName(*r.next) + " (guessed, next attempt)"
	case r.learned != nil:
		return link.FormName(*r.learned) + " (learned)"
	}
	return "DLE STX (default)"
}

// ResetFraming forgets the framing learned for a remote.
func (n *Node) ResetFraming(name string) error {
	n.mu.Lock()
	r, err := n.lookup(name)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	r.learned, r.next, r.triedOther = nil, nil, false
	n.mu.Unlock()
	n.log.Info(r.cfg.Name + ": framing forgotten (link reset-framing)")
	n.saveFraming("")
	return nil
}

// framingEntry is one line of gonje.state.
type framingEntry struct {
	dleSTX    bool
	confirmed string
}

// readFramingState parses gonje.state; an unreadable file is empty.
func readFramingState(path string) map[string]framingEntry {
	out := map[string]framingEntry{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e framingEntry
		node := ""
		for _, tok := range strings.Fields(sc.Text()) {
			k, v, _ := strings.Cut(tok, "=")
			switch {
			case strings.HasSuffix(strings.ToLower(k), ".dlestx"):
				node = strings.ToUpper(strings.TrimSuffix(k, k[len(k)-len(".dlestx"):]))
				e.dleSTX = strings.EqualFold(v, "yes")
			case k == "confirmed":
				e.confirmed = v
			}
		}
		if node != "" {
			out[node] = e
		}
	}
	return out
}

// loadFraming takes the learned forms from gonje.state.
func (n *Node) loadFraming() {
	if n.opts.StatePath == "" {
		return
	}
	st := readFramingState(n.opts.StatePath)
	n.mu.Lock()
	defer n.mu.Unlock()
	for name, e := range st {
		if r := n.remotes[name]; r != nil {
			d := e.dleSTX
			r.learned = &d
		}
	}
}

// saveFraming writes gonje.state (atomically): every remote with a
// learned form, plus entries of other nodes already in the file. The
// confirmed time is set for a new or changed form and for refresh (the
// remote that just signed on). The file is not created when there is
// nothing to remember.
func (n *Node) saveFraming(refresh string) {
	if n.opts.StatePath == "" {
		return
	}
	n.stmu.Lock()
	defer n.stmu.Unlock()
	st := readFramingState(n.opts.StatePath)
	now := time.Now().UTC().Format(time.RFC3339)
	n.mu.Lock()
	for name, r := range n.remotes {
		if r.learned == nil {
			delete(st, name)
			continue
		}
		e, ok := st[name]
		if !ok || e.dleSTX != *r.learned || name == refresh {
			e = framingEntry{dleSTX: *r.learned, confirmed: now}
		}
		st[name] = e
	}
	n.mu.Unlock()
	if len(st) == 0 {
		if _, err := os.Stat(n.opts.StatePath); err != nil {
			return // nothing to remember, no file
		}
	}
	names := make([]string, 0, len(st))
	for k := range st {
		names = append(names, k)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("# gonje.state: DLE STX framing learned per remote node (written by gonje)\n")
	for _, k := range names {
		yn := "no"
		if st[k].dleSTX {
			yn = "yes"
		}
		fmt.Fprintf(&sb, "%s.dlestx=%s confirmed=%s\n", k, yn, st[k].confirmed)
	}
	if err := writeDurable(n.opts.StatePath, []byte(sb.String()), 0o644); err != nil {
		n.log.Error("gonje.state: " + err.Error())
	}
}
