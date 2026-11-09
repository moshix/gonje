// Copyright 2026 by moshix

package link

import (
	"fmt"

	"gonje/internal/nje/records"
)

// Adaptive DLE STX framing.
//
// Every known TCPNJE peer (Hercules TCPNJE / NJE38, z/VM RSCS TCPNJE,
// Sine Nomine NJE) starts its buffers with DLE STX, and so does gonje.
// (An earlier theory blamed DLE STX for RSCS's DMTTNE945E code 1818; the
// real cause was the RCB 00 after the F0 signon record, see
// records.Buffer.EncodeFramed and DLE_STX.md.) What remains is a safety
// net for an unknown peer:
//
//   - Mirror: every data buffer from the peer shows its form (starts
//     with 10 02 or not); we send in that form from then on.
//   - Blind start (our I goes first): the form the node learned or
//     mirrored earlier, else DLE STX. No other guessing.
//   - Fallback: an I that is answered by a reset, a close, a NAK or
//     nothing is reported (FramingRejected); the node tries the other
//     form once, then returns to DLE STX (bounded, with backoff).
//
// Receiving always accepts both forms (records.Decode strips a leading
// 10 02 at the start of the record only). Line control sequences (SOH
// ENQ, DLE ACK0, SYN NAK, NAK) never carry DLE STX.

// Framing selects the form of our buffers.
type Framing int

// Framing forms.
const (
	FramingAuto   Framing = iota // DLE STX (the default)
	FramingDLESTX                // 10 02 BCB FCS ...
	FramingNone                  // BCB FCS ...
)

// Framing sources (how the form was chosen), shown by "stats <node>".
const (
	FramingLearned  = "learned"  // remembered from an earlier signon
	FramingGuessed  = "guessed"  // default or fallback
	FramingMirrored = "mirrored" // copied from the peer's own buffers
)

// FormName names a form for logs and the shell.
func FormName(dleSTX bool) string {
	if dleSTX {
		return "DLE STX"
	}
	return "none"
}

// initFraming sets the initial form from the parameters.
func (s *Session) initFraming() {
	s.dleSTX = s.p.Framing != FramingNone
	s.framingSrc = s.p.FramingSrc
	if s.framingSrc == "" {
		s.framingSrc = FramingGuessed
	}
	s.framingWhy = s.p.FramingWhy
	if s.framingWhy == "" {
		s.framingWhy = "default"
	}
	s.statFraming()
}

// Framing returns the form we send, how it was chosen and why.
func (s *Session) Framing() (dleSTX bool, src, why string) {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	return s.dleSTX, s.framingSrc, s.framingWhy
}

// FramingRejected reports that our signon I went unanswered (reset,
// close, NAK, E0 or timeout) in the form Framing returns.
func (s *Session) FramingRejected() bool {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	return s.framingRejected
}

// statFraming copies the form into the stats (for "stats <node>").
func (s *Session) statFraming() {
	dle, src, _ := s.dleSTX, s.framingSrc, s.framingWhy
	s.count(func(st *Stats) { st.Framing = FormName(dle) + " (" + src + ")" })
}

// announceFraming logs the blind choice before our I goes out.
func (s *Session) announceFraming() {
	dle, src, why := s.Framing()
	s.framingEvent(fmt.Sprintf("framing: %s (%s: %s)", FormName(dle), src, why))
}

func (s *Session) framingEvent(text string) {
	s.log.Info(text)
	s.emit(EvLink, L2, text)
}

// mirrorFraming adopts the form of a data buffer the peer sent (rec is
// one TTR record classified as a buffer). Logged when first seen and on
// every change.
func (s *Session) mirrorFraming(rec []byte) {
	peer := len(rec) >= 2 && rec[0] == records.DLE && rec[1] == records.STX
	s.fmu.Lock()
	first, changed := !s.mirrored, peer != s.dleSTX
	old := s.dleSTX
	s.mirrored, s.dleSTX = true, peer
	if first || changed {
		s.framingSrc, s.framingWhy = FramingMirrored, "peer's buffers"
		s.statFraming()
	}
	s.fmu.Unlock()
	switch {
	case changed:
		s.framingEvent(fmt.Sprintf("framing: %s (mirrored peer, was %s)", FormName(peer), FormName(old)))
	case first:
		s.framingEvent(fmt.Sprintf("framing: %s (mirrored peer)", FormName(peer)))
	}
}

// rejectFraming notes that the peer did not answer our I.
func (s *Session) rejectFraming(err error) {
	s.fmu.Lock()
	if !s.iSent || s.mirrored {
		// The peer did send data: its form is known, the failure is
		// something else.
		s.fmu.Unlock()
		return
	}
	s.framingRejected = true
	dle := s.dleSTX
	s.fmu.Unlock()
	s.framingEvent(fmt.Sprintf("framing: %s rejected by %s (no J: %v)", FormName(dle), s.p.Remote, err))
}
