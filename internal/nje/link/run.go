// Copyright 2026 by moshix

package link

import (
	"errors"
	"fmt"
	"net"
	"runtime/debug"
	"time"

	"gonje/internal/ebcdic"
	"gonje/internal/nje/records"
)

// tickInterval drives reply delays, keepalive, dead-link and stream
// timeouts.
const tickInterval = 20 * time.Millisecond

// idleSummaryEvery is how often idle-block counts are logged.
const idleSummaryEvery = time.Minute

// SendNMR queues an NMR for transmission on our next turn. It fails if
// the session ended. NMRs still queued when the session ends are
// reported as undelivered (EvError), never silently dropped.
func (s *Session) SendNMR(m *records.NMR) error {
	s.nmrMu.Lock()
	if s.nmrClosed {
		s.nmrMu.Unlock()
		return ErrClosed
	}
	s.nmrPending = append(s.nmrPending, m)
	s.nmrMu.Unlock()
	s.Kick()
	return nil
}

// Kick tells the session that something new was queued.
func (s *Session) Kick() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

// closeReq asks Run to end the session; signoff says whether to send the
// NJE signoff record (B) first.
type closeReq struct {
	reason  string
	signoff bool
}

// Close asks the session to sign off and close. It does not wait. A
// signoff tells the peer to stop this link: a correct NJE node (HOUCICS
// in live tests) then refuses to sign on again until its operator
// restarts the link, so only use it when the operator means "stop the
// link" (drop, drain, link delete).
func (s *Session) Close(reason string) {
	select {
	case s.closeCh <- closeReq{reason: reason, signoff: true}:
	default:
	}
}

// CloseQuiet ends the session by closing the connection WITHOUT sending
// a signoff, so the peer treats it as a lost connection and will accept
// a new signon at once (used for gonje shutdown and local renames).
func (s *Session) CloseQuiet(reason string) {
	select {
	case s.closeCh <- closeReq{reason: reason}:
	default:
	}
}

// Kill closes the socket abruptly without signoff (used when the peer is
// assumed dead, and by tests to simulate a broken connection).
func (s *Session) Kill() { _ = s.conn.Close() }

// Drain switches graceful drop on or off: while draining, running
// transfers finish, no new ones start (ours stay queued, the peer's
// requests are refused), and the session signs off once idle.
//
// It returns only after Run has applied the change, so a request (90)
// that arrives after Drain(true) returned is certainly refused. Callers
// must not hold locks that the session goroutine may need.
func (s *Session) Drain(on bool) {
	ack := make(chan struct{})
	select {
	case s.drainCh <- drainReq{on: on, ack: ack}:
	case <-s.done:
		return
	}
	select {
	case <-ack:
	case <-s.done:
	}
}

// drainReq asks Run to switch draining and confirms it via ack.
type drainReq struct {
	on  bool
	ack chan struct{}
}

// Done is closed when Run has returned.
func (s *Session) Done() <-chan struct{} { return s.done }

// reader runs in its own goroutine and feeds TTR records to Run.
func (s *Session) reader() {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error(fmt.Sprintf("panic in reader: %v\n%s", r, debug.Stack()))
			select {
			case s.errCh <- fmt.Errorf("internal error: %v", r):
			case <-s.quit:
			}
		}
	}()
	for {
		rec, err := s.readRecord()
		if err != nil {
			select {
			case s.errCh <- err:
			case <-s.quit:
			}
			return
		}
		select {
		case s.inCh <- rec:
		case <-s.quit:
			return
		}
	}
}

// Run is the main loop after a successful signon. It owns all protocol
// state; other goroutines talk to it through channels. It returns the
// reason the session ended (ErrPeerSignoff, ErrLocalDrop, ErrDead, an I/O
// error, ...). The connection is closed on return. A panic in protocol
// code is turned into a session error so the process (and the operator's
// terminal) survive.
func (s *Session) Run() (err error) {
	defer func() { s.finish(err) }()
	defer func() {
		if r := recover(); r != nil {
			s.log.Error(fmt.Sprintf("panic in session: %v\n%s", r, debug.Stack()))
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	// TCP keepalive finds a dead peer even when the link is idle and the
	// peer sends nothing (free mode does not time out on silence).
	if tc, ok := s.conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(s.p.Keepalive)
	}
	go s.reader()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	now := time.Now()
	if s.lastSend.IsZero() {
		s.lastSend = now
	}
	s.lastSummary = now
	for {
		if err := s.pump(time.Now()); err != nil {
			return err
		}
		s.activity()
		if s.draining && s.idleForDrain() {
			s.emit(EvSignon, L2, "drain complete: signing off")
			_ = s.sendBuffer([]records.Record{records.SignoffRecord()})
			return ErrLocalDrop
		}
		select {
		case rec := <-s.inCh:
			if err := s.handleInbound(rec, time.Now()); err != nil {
				return err
			}
			s.activity()
		case err := <-s.errCh:
			// Handle what the reader got before the error first: a peer
			// that signs off and closes at once must end as a signoff,
			// not as a lost connection (select picks channels randomly).
			for {
				select {
				case rec := <-s.inCh:
					if herr := s.handleInbound(rec, time.Now()); herr != nil {
						return herr
					}
					continue
				default:
				}
				break
			}
			return fmt.Errorf("connection lost: %w", err)
		case <-s.kickCh:
		case req := <-s.drainCh:
			s.draining = req.on
			s.activity()
			close(req.ack)
		case <-ticker.C:
			if err := s.tick(time.Now()); err != nil {
				return err
			}
		case req := <-s.closeCh:
			if !req.signoff {
				s.emit(EvSignon, L2, "connection closed without signoff ("+req.reason+")")
				return ErrLocalDrop
			}
			// Signoff needs no turn and gets no reply (Table 44).
			s.emit(EvSignon, L2, "signoff sent ("+req.reason+")")
			_ = s.sendBuffer([]records.Record{records.SignoffRecord()})
			return ErrLocalDrop
		}
	}
}

// finish closes the connection, stops the reader goroutine and reports
// every NMR that never left the queue.
func (s *Session) finish(err error) {
	s.once.Do(func() {
		close(s.quit)
		_ = s.conn.Close()
		s.log.Info(fmt.Sprintf("session ended: %v", err))
		s.nmrMu.Lock()
		s.nmrClosed = true
		lost := s.nmrPending
		s.nmrPending = nil
		s.nmrMu.Unlock()
		for _, m := range lost {
			what := "message to " + m.OutUser()
			if m.IsCommand() {
				what = "command"
			}
			s.emit(EvError, L2, fmt.Sprintf("%s not delivered (link down): %s", what, m.Text()))
		}
		close(s.done)
	})
}

// idleForDrain reports that no transfer is in progress in either
// direction and every stream reply (A0/B0/C0) has gone out.
func (s *Session) idleForDrain() bool {
	return s.txActive() == 0 && len(s.rx) == 0 && len(s.ctlQueue) == 0
}

// activity publishes the number of running transfers for "nodes"/drain.
func (s *Session) activity() {
	in, out := len(s.rx), s.txActive()
	s.count(func(st *Stats) { st.ActiveIn, st.ActiveOut, st.Draining = in, out, s.draining })
}

// takeTurn records that the peer sent us a block, which we must answer
// with exactly one block (BSC/CTC alternation, Table 46 and fig 16/17,
// kept by NJE over TCP). idle = the block carried no data (null buffer,
// DLE ACK0); wab = it was a null buffer with wait-a-bit set.
//
// Reply timing when we have nothing to send:
//   - after data: at once, and the idle throttle restarts;
//   - after an idle block: idleStep x consecutive idle exchanges, capped
//     at idleMax (100 ms, 200 ms, ... 1 s), like JES2;
//   - after wait-a-bit in a null buffer: waitABitDelay (spec 02 1.3).
//
// If we do have something to send, pump sends it at once instead
// (our data rides on our turn).
//
// The idle delay is also kept below a quarter of the keepalive interval
// so that a short remote_keepalive never declares a healthy, idle link
// dead. If we already owed a reply (two blocks arrived before we
// answered), the earlier reply time stands: one answer covers both.
func (s *Session) takeTurn(now time.Time, idle, wab bool) {
	if !s.p.StrictTurn && idle && !wab {
		// Free mode: idle blocks are never answered (a peer that does not
		// ping-pong would otherwise get an endless null exchange).
		return
	}
	var at time.Time
	switch {
	case wab:
		at = now.Add(waitABitDelay)
	case !idle:
		s.idleRuns = 0
		at = now
	default:
		s.idleRuns++
		at = now.Add(min(time.Duration(s.idleRuns)*idleStep, idleMax, s.p.Keepalive/4))
	}
	if !s.myTurn || at.Before(s.replyAt) {
		s.replyAt = at
	}
	delay := at.Sub(now)
	s.count(func(st *Stats) { st.IdleDelay = delay })
	s.myTurn = true
}

// pump uses our turn: data if we have any, otherwise a null buffer once
// the reply delay has passed. Without a turn we send nothing (tick may
// still restart a stalled exchange).
//
// Free mode (remote_turn=free, the default): TCP is full duplex and some
// peers (e.g. the MVS/Hercules NJE seen in the first interop test) do not
// alternate at all, so data is sent as soon as we have it. A data block
// from the peer is still answered (with data, or a null buffer).
func (s *Session) pump(now time.Time) error {
	if !s.p.StrictTurn {
		// One buffer per pass, then a self-kick: Run goes back to its
		// select between buffers and keeps draining inbound blocks.
		// Writing a whole file in one go would deadlock with a peer
		// doing the same (both TCP windows full, nobody reading).
		if recs := s.nextDataBuffer(); recs != nil {
			s.myTurn, s.idleRuns = false, 0
			s.Kick()
			return s.sendBuffer(recs)
		}
	}
	if !s.myTurn {
		return nil
	}
	if recs := s.nextDataBuffer(); recs != nil {
		s.myTurn, s.idleRuns = false, 0
		return s.sendBuffer(recs)
	}
	if now.Before(s.replyAt) {
		return nil
	}
	s.myTurn = false
	return s.sendBuffer(nil)
}

// tick handles time-based duties.
//
// remote_keepalive (k) only matters when the alternation stops: if no
// block at all has arrived for k, we send a null buffer to restart the
// exchange (again every k); after 3k without a block the peer is dead.
//
// Free mode: peer silence proves nothing (an idle peer may send nothing
// at all), so dead peers are found by TCP keepalive and I/O errors; we
// only send a null buffer after k of our own idleness.
func (s *Session) tick(now time.Time) error {
	k := s.p.Keepalive
	if !s.p.StrictTurn {
		if now.Sub(s.lastSend) >= k {
			if err := s.sendBuffer(nil); err != nil {
				return err
			}
		}
	} else if now.Sub(s.lastRecv) >= 3*k {
		return ErrDead
	}
	if s.p.StrictTurn && !s.myTurn && now.Sub(s.lastRecv) >= k && now.Sub(s.lastKick) >= k {
		s.lastKick = now
		s.log.Debug("no block from peer for keepalive interval: sending a null buffer")
		if err := s.sendBuffer(nil); err != nil {
			return err
		}
	}
	if now.Sub(s.lastSummary) >= idleSummaryEvery {
		if s.idleIn+s.idleOut > 0 {
			s.log.Debug(fmt.Sprintf("idle exchange: %d null/ACK blocks in, %d out in the last %s",
				s.idleIn, s.idleOut, idleSummaryEvery))
		}
		s.idleIn, s.idleOut, s.lastSummary = 0, 0, now
	}
	// Stream handshakes that never complete.
	// Some peers (HOUCICS, the reference MVS NJE node) take the file but
	// never send C0 after our EOF. After EOFWait without C0, B0 or an
	// error, the file counts as sent (unconfirmed) and the stream is
	// freed; a late C0/B0 is logged and ignored.
	for _, t := range s.txs {
		if t.state == txWaitAck && now.Sub(t.since) > s.p.EOFWait {
			s.log.Warn(fmt.Sprintf("no EOF acknowledgement (C0) for %s within %s: counted as sent", t.f.Name, s.p.EOFWait))
			s.txComplete(t, " (peer did not confirm receipt)")
		}
		if t.state == txWaitPermit && now.Sub(t.since) > streamTimeout {
			s.txFail(t, fmt.Errorf("timeout waiting for permission"))
		}
	}
	return nil
}

// nextDataBuffer picks the records for the next buffer, or nil.
// Without the NCCIMRCB feature a buffer may only hold records of one RCB,
// so each category goes in its own buffer.
func (s *Session) nextDataBuffer() []records.Record {
	if s.peerFCS.WaitABit() {
		return nil // never answer wait-a-bit with data
	}
	if len(s.ctlQueue) > 0 {
		r := s.ctlQueue[0]
		s.ctlQueue = s.ctlQueue[1:]
		return []records.Record{r}
	}
	if s.peerFCS.Console() {
		if recs := s.takeNMRs(); recs != nil {
			return recs
		}
	}
	return s.txNext()
}

// takeNMRs removes as many queued NMRs as fit in one buffer.
func (s *Session) takeNMRs() []records.Record {
	s.nmrMu.Lock()
	defer s.nmrMu.Unlock()
	var recs []records.Record
	size := 6 // DLE STX BCB FCS(2) + EOB
	for len(s.nmrPending) > 0 {
		r := s.nmrPending[0].Record()
		n := records.EncodedSize(r)
		if len(recs) > 0 && size+n > s.bufLimit {
			break
		}
		recs, size = append(recs, r), size+n
		cmd := s.nmrPending[0].IsCommand()
		s.nmrPending = s.nmrPending[1:]
		s.count(func(st *Stats) {
			if cmd {
				st.CmdsOut++
			} else {
				st.MsgsOut++
			}
		})
	}
	return recs
}

// handleInbound processes one TTR record from the peer.
func (s *Session) handleInbound(rec []byte, now time.Time) error {
	s.lastRecv = now
	if len(rec) == 0 {
		// Empty TTB (VSE/POWER keepalive): traffic, but not a BSC
		// transmission, so it is not answered.
		s.idleIn++
		s.emit(EvKeepalive, L3, "keepalive <- (empty block)")
		s.count(func(st *Stats) { st.KeepaliveIn++ })
		return nil
	}
	switch k := records.Classify(rec); k {
	case records.KindDLEACK0:
		// JES2 acknowledges with DLE ACK0 when it has nothing to send;
		// it is a transmission and gets an answer like a null buffer.
		s.idleIn++
		s.emit(EvKeepalive, L3, "keepalive <- (DLE ACK0)")
		s.count(func(st *Stats) { st.KeepaliveIn++ })
		s.takeTurn(now, true, false)
		return nil
	case records.KindSOHENQ:
		return ErrPeerRestart
	case records.KindBuffer:
	default:
		// NAK / SYN NAK / garbage after signon: CTC rules say take the
		// link down on NAK; we stay tolerant, log it and keep alternating.
		s.log.Warn(fmt.Sprintf("NONSTANDARD %s after signon ignored", k))
		if k == records.KindNAK || k == records.KindSYNNAK {
			s.count(func(st *Stats) { st.NAKsIn++ })
		}
		s.emit(EvDump, L3, "<- "+k.String()+" (ignored)", ebcdic.HexDump(rec)...)
		s.takeTurn(now, true, false)
		return nil
	}
	buf, derr := records.Decode(rec)
	if buf == nil {
		s.log.Warn(fmt.Sprintf("NONSTANDARD undecodable buffer: %v", derr))
		s.takeTurn(now, true, false)
		return nil
	}
	if derr != nil {
		s.log.Warn(fmt.Sprintf("NONSTANDARD buffer partly decoded: %v", derr))
		s.emit(EvError, L2, fmt.Sprintf("bad buffer from peer: %v", derr))
	}
	dup, err := s.checkBCB(buf.BCB)
	if err != nil {
		return err
	}
	if dup {
		// Strict mode: the peer lost our last block; resend it (this is
		// our answer) and drop the duplicate.
		s.myTurn = false
		return s.sendRaw(s.lastBlock)
	}
	s.peerFCS = buf.FCS
	if buf.IsNull() {
		s.idleIn++
		s.emit(EvKeepalive, L3, "keepalive <-")
		s.count(func(st *Stats) { st.KeepaliveIn++ })
		s.takeTurn(now, true, buf.FCS.WaitABit())
		return nil
	}
	s.takeTurn(now, false, false)
	if !s.settled && !s.signedOnAt.IsZero() {
		s.settled = true // the peer's first post-signon data block
		s.Kick()
	}
	for _, r := range buf.Records {
		s.dumpRecord("<-", r)
		if err := s.dispatch(r); err != nil {
			return err
		}
	}
	return nil
}

// dispatch acts on one inbound record.
func (s *Session) dispatch(r records.Record) error {
	switch {
	case r.RCB == records.RCBControl:
		switch r.SRCB {
		case records.SRCBSignoff:
			s.emit(EvSignon, L2, "signoff received")
			return ErrPeerSignoff
		case records.SRCBSignonI, records.SRCBSignonJ:
			s.log.Warn("extra signon record on active session ignored")
		default:
			// K, L, M, N: path manager records; a non-NPM node discards them.
			s.log.Debug(fmt.Sprintf("NCCR %02X discarded (no path manager)", r.SRCB))
		}
	case r.RCB == records.RCBNMR:
		m, err := records.ParseNMR(r.Data)
		if err != nil {
			s.log.Warn(err.Error())
			return nil
		}
		s.count(func(st *Stats) {
			if m.IsCommand() {
				st.CmdsIn++
			} else {
				st.MsgsIn++
			}
		})
		s.p.Handler.NMR(s.p.Remote, m)
	case r.RCB == records.RCBRequest:
		s.rxRequest(r.SRCB)
	case r.RCB == records.RCBPermit:
		s.txPermit(r.SRCB)
	case r.RCB == records.RCBDeny:
		s.txDenied(r.SRCB, r.Data)
	case r.RCB == records.RCBAckEOF:
		s.txAcked(r.SRCB)
	case r.RCB == records.RCBReady:
		s.log.Debug("receiver ready for " + records.StreamName(r.SRCB))
		s.peerDrained = false
	case r.RCB == records.RCBBCBErr:
		if s.p.StrictBCB {
			// Manual: the receiver of E0 terminates the connection.
			s.log.Error(fmt.Sprintf("peer reports BCB sequence error (expected %X): terminating", r.SRCB&0x0F))
			return fmt.Errorf("peer reported a %w", ErrBCBSequence)
		}
		// Tolerant: adopt the peer's expectation and carry on.
		s.log.Warn(fmt.Sprintf("NONSTANDARD peer reports BCB sequence error, expected %X: adopting it", r.SRCB&0x0F))
		s.emit(EvError, L2, "peer reports BCB sequence error")
		s.outSeq = r.SRCB & 0x0F
	case records.IsStream(r.RCB):
		s.rxRecord(r)
	default:
		s.log.Warn(fmt.Sprintf("NONSTANDARD unexpected RCB %02X ignored", r.RCB))
	}
	return nil
}

// IsReconnectable reports whether a session end reason should trigger an
// automatic reconnect (everything except an orderly signoff/drop).
func IsReconnectable(err error) bool {
	return !errors.Is(err, ErrPeerSignoff) && !errors.Is(err, ErrLocalDrop)
}
