// Copyright 2026 by moshix

package link

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"gonje/internal/ebcdic"
	"gonje/internal/logx"
	"gonje/internal/nje/records"
	"gonje/internal/nje/tcpip"
)

// Defaults for Params.
const (
	DefaultBufSize   = 8192
	DefaultHandshake = 30 * time.Second
	// bufMargin is subtracted from the negotiated buffer size: the manual
	// does not say whether NCCIBFSZ counts DLE STX and the TTB/TTR
	// framing (spec 03 section 9), so we stay well below it.
	bufMargin = 32
	// idleStep / idleMax: when there is nothing to send, our reply to an
	// idle block (null buffer, DLE ACK0) is delayed by idleStep times the
	// number of consecutive idle exchanges, at most idleMax, like the
	// JES2 TCP/IP idle throttle (App. A). Real traffic resets it.
	idleStep = 100 * time.Millisecond
	idleMax  = time.Second
	// DefaultEOFWait is how long we wait for C0 after sending EOF. Fixed,
	// not configurable: HOUCICS/NJE38 never sends C0 at all, so after
	// this grace the file counts as sent (unconfirmed).
	DefaultEOFWait = 10 * time.Second
	// streamSettle: after signon no file stream is started until the
	// peer has sent its first data block or this time has passed.
	// HOUCICS (NJE38) rejected a file started in the same millisecond as
	// our J ("NJE939E Protocol error ... reason code 01", receive-test.log
	// 11:20:39) and only sent its own post-signon NMR 3 s later; the same
	// file sent later went through. NMRs are not held back.
	streamSettle = 5 * time.Second
	// DefaultMaxJob caps the bytes buffered for one inbound job (R2).
	DefaultMaxJob = 256 << 20
	// maxSpanned is the largest spanned logical record (spec 02 1B).
	maxSpanned = 32760
	// waitABitDelay: a null buffer carrying wait-a-bit is answered after
	// this delay (spec 02 section 1.3: 1-2 s, never > 2 s).
	waitABitDelay = time.Second
	// streamTimeout: how long we wait for permission (A0) or EOF ack (C0).
	streamTimeout = 2 * time.Minute
)

// Params configures a session.
type Params struct {
	Local     string        // our node name
	Remote    string        // peer node name
	Password  string        // optional node password (both directions)
	Keepalive time.Duration // idle null-buffer interval; dead after 3x
	BufSize   int           // buffer size we advertise (NCCIBFSZ)
	Handshake time.Duration // timeout for each handshake step
	StrictBCB bool          // manual BCB rules: E0 + terminate on mismatch
	EOFWait   time.Duration // wait for C0 after our EOF (default 10 s)
	// MaxStreams: concurrent SYSIN/SYSOUT transfers per direction (1-8;
	// the FCS has 8 shared SYSIN/SYSOUT bits, spec 02 section 1.3).
	MaxStreams int
	// NatOut: our public address behind NAT; put in RIP of our OPEN.
	NatOut net.IP
	// StreamSettle overrides the post-signon settle time (tests only;
	// 0 = streamSettle).
	StreamSettle time.Duration
	// StrictTurn: BSC-style alternation (every block answered, sending
	// only on our turn, dead after 3x keepalive of silence). Off (the
	// default, remote_turn=free): send whenever we have data.
	StrictTurn bool
	MaxJob     int // max bytes buffered per inbound job
	// Framing: whether our buffers start with DLE STX (see framing.go).
	// FramingAuto = DLE STX.
	Framing    Framing
	FramingSrc string // FramingLearned, FramingGuessed (default)
	FramingWhy string // reason for the log ("confirmed earlier", "default")
	Log        *logx.Logger
	Handler    Handler
}

func (p *Params) defaults() {
	if p.BufSize == 0 {
		p.BufSize = DefaultBufSize
	}
	if p.Handshake == 0 {
		p.Handshake = DefaultHandshake
	}
	if p.Keepalive == 0 {
		p.Keepalive = 10 * time.Second
	}
	if p.Log == nil {
		p.Log = logx.Discard()
	}
	if p.MaxJob == 0 {
		p.MaxJob = DefaultMaxJob
	}
	if p.StreamSettle == 0 {
		p.StreamSettle = streamSettle
	}
	if p.MaxStreams < 1 || p.MaxStreams > 8 {
		p.MaxStreams = 1
	}
	if p.EOFWait == 0 {
		p.EOFWait = DefaultEOFWait
	}
}

// Session is one live NJE connection.
type Session struct {
	p    Params
	conn net.Conn
	br   *bufio.Reader
	log  *logx.Logger

	// Records read during the handshake that belong to the same TTB block.
	pending [][]byte

	// --- state owned by the Run goroutine (and the handshake before it) ---
	outSeq      byte // next outbound BCB count (mod 16)
	inSeq       byte // next expected inbound BCB count (mod 16)
	peerFCS     records.FCS
	bufLimit    int       // max encoded buffer size we send
	myTurn      bool      // the peer sent a block we have not answered yet
	replyAt     time.Time // earliest time for an idle (null) reply
	idleRuns    int       // consecutive idle exchanges (reply throttle)
	lastSend    time.Time
	lastRecv    time.Time
	lastKick    time.Time
	lastBlock   []byte // last buffer sent, for a strict-mode retransmit
	idleIn      int    // idle blocks since the last log summary
	idleOut     int
	lastSummary time.Time
	ctlQueue    []records.Record // stream control replies (A0/B0/C0)
	txs         []*txState       // outbound slots, one per stream (max_streams)
	txTurn      int              // round-robin position among the slots
	ourFCS      records.FCS
	refusedFull bool      // we refused a request because all streams were busy
	settled     bool      // peer sent data after signon, or streamSettle passed
	signedOnAt  time.Time // when signon completed
	rx          map[byte]*rxStream
	draining    bool // graceful drop requested
	peerDrained bool // the peer refused our request (B0 to 90) until D0

	// NMRs waiting for our turn. Guarded by nmrMu because SendNMR is
	// called from other goroutines; on session end the leftovers are
	// reported as undelivered.
	nmrMu      sync.Mutex
	nmrPending []*records.NMR
	nmrDone    []func(error) // callback per pending NMR (may be nil)
	nmrClosed  bool
	// nmrSent: callbacks of NMRs taken into the buffer being built; they
	// run once that buffer has been written to the socket.
	nmrSent []func(error)

	// Framing (framing.go): dleSTX is the form we send; mirrored is set
	// once the peer's first data buffer told us its form. iSent and
	// framingRejected track a signon I the peer did not answer.
	fmu             sync.Mutex
	dleSTX          bool
	framingSrc      string
	framingWhy      string
	mirrored        bool
	iSent           bool
	framingRejected bool

	// --- cross-goroutine plumbing ---
	inCh    chan []byte
	errCh   chan error
	kickCh  chan struct{}
	drainCh chan drainReq
	closeCh chan closeReq
	done    chan struct{}
	quit    chan struct{}
	once    sync.Once

	mu    sync.Mutex
	stats Stats
}

// New wraps an established TCP connection (after OPEN/ACK).
func New(conn net.Conn, p Params) *Session {
	p.defaults()
	s := &Session{
		p:        p,
		conn:     conn,
		br:       bufio.NewReaderSize(conn, 70000),
		log:      p.Log,
		peerFCS:  records.FCSNormal,
		bufLimit: p.BufSize - bufMargin,
		rx:       map[byte]*rxStream{},
		inCh:     make(chan []byte, 256), // deep: keeps reading while Run is busy writing
		errCh:    make(chan error, 1),
		kickCh:   make(chan struct{}, 1),
		drainCh:  make(chan drainReq, 4),
		closeCh:  make(chan closeReq, 1),
		done:     make(chan struct{}),
		quit:     make(chan struct{}),
	}
	s.initTx()
	s.ourFCS = streamFCS(p.MaxStreams)
	s.initFraming()
	return s
}

// streamFCS is the FCS we send: always X'8FCF' (every SYSIN/SYSOUT
// stream and the console enabled), whatever max_streams says. NJE38
// (HOUCICS) rejected our file with "NJE939E Protocol error in file ...
// reason code 01" when our FCS enabled stream 1 only (X'88C1',
// live06.log 11:30:45) and accepted the identical file with X'8FCF'
// (houcics-test5.log 10:53:03). max_streams is enforced by never
// starting more than n outbound streams and by refusing the peer's
// n+1th request (B0, then D0 when a stream frees up).
func streamFCS(int) records.FCS { return records.FCSNormal }

// Remote returns the peer node name.
func (s *Session) Remote() string { return s.p.Remote }

// Stats returns a copy of the counters.
func (s *Session) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *Session) count(f func(st *Stats)) {
	s.mu.Lock()
	f(&s.stats)
	s.mu.Unlock()
}

// emit sends an event to the handler.
func (s *Session) emit(k Kind, level int, text string, lines ...string) {
	s.p.Handler.Event(Event{Time: time.Now(), Kind: k, Level: level, Node: s.p.Remote, Text: text, Lines: lines})
}

// ---------------------------------------------------------------------
// Raw I/O
// ---------------------------------------------------------------------

// sendRaw writes one TTB block holding one TTR record. Idle blocks
// (null buffers, DLE ACK0) are not hex-dumped one by one, or the log
// would grow by several lines per second on an idle link; they are
// counted and summarised once a minute instead.
func (s *Session) sendRaw(rec []byte) error {
	_ = s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	raw, err := tcpip.WriteBlock(s.conn, rec)
	if err != nil {
		return err
	}
	// NMRs in this block are now on the wire: tell their senders.
	s.nmrMu.Lock()
	sent := s.nmrSent
	s.nmrSent = nil
	s.nmrMu.Unlock()
	for _, done := range sent {
		if done != nil {
			done(nil)
		}
	}
	if isIdleRecord(rec) {
		s.idleOut++
	} else {
		s.dumpBlock("TTB block sent", raw)
	}
	s.lastSend = time.Now()
	s.count(func(st *Stats) {
		st.BlocksOut++
		st.BytesOut += int64(len(raw))
		st.LastActivity = s.lastSend
	})
	return nil
}

// readRecord returns the next TTR record, reading a new TTB block when the
// previous one is used up. Empty blocks count as traffic and are skipped.
func (s *Session) readRecord() ([]byte, error) {
	for len(s.pending) == 0 {
		recs, raw, err := tcpip.ReadBlock(s.br)
		if len(raw) > 0 && !(len(recs) == 1 && isIdleRecord(recs[0])) {
			s.dumpBlock("TTB block received", raw)
		}
		if err != nil {
			return nil, err
		}
		now := time.Now()
		s.count(func(st *Stats) {
			st.BlocksIn++
			st.BytesIn += int64(len(raw))
			st.LastActivity = now
		})
		if len(recs) == 0 {
			// Empty block (VSE/POWER style keepalive): hand it on as a
			// zero-length record so the owner can count it as traffic.
			return []byte{}, nil
		}
		s.pending = recs
	}
	r := s.pending[0]
	s.pending = s.pending[1:]
	return r, nil
}

// isIdleRecord reports a TTR record that carries no data: DLE ACK0 or a
// null buffer.
func isIdleRecord(rec []byte) bool {
	switch records.Classify(rec) {
	case records.KindDLEACK0:
		return true
	case records.KindBuffer:
		b, err := records.Decode(rec)
		return err == nil && b.IsNull()
	}
	return false
}

// dumpBlock logs a raw TTB block with signon passwords masked: each TTR
// record that holds an I/J buffer has its NCCILPAS/NCCINPAS hidden.
func (s *Session) dumpBlock(title string, raw []byte) {
	if !s.log.Enabled(logx.Debug) {
		return
	}
	mask := make([]bool, len(raw))
	for off := tcpip.TTBLen; off+tcpip.TTRLen <= len(raw); {
		ln := int(raw[off+2])<<8 | int(raw[off+3])
		start := off + tcpip.TTRLen
		if ln == 0 || start+ln > len(raw) {
			break
		}
		for i, m := range records.BufferMask(raw[start : start+ln]) {
			mask[start+i] = m
		}
		off = start + ln
	}
	lines := []string{fmt.Sprintf("%s (%d bytes)", title, len(raw))}
	for _, l := range ebcdic.HexDumpMasked(raw, mask) {
		lines = append(lines, "    "+l)
	}
	s.log.Lines(logx.Debug, lines)
}

// readRecordTimeout is readRecord with a deadline, for the handshake.
// Empty keepalive blocks are skipped.
func (s *Session) readRecordTimeout() ([]byte, error) {
	_ = s.conn.SetReadDeadline(time.Now().Add(s.p.Handshake))
	defer s.conn.SetReadDeadline(time.Time{})
	for {
		r, err := s.readRecord()
		if err != nil || len(r) > 0 {
			return r, err
		}
	}
}

// sendLine sends a 2-byte line-control sequence.
func (s *Session) sendLine(seq []byte) error {
	k := records.Classify(seq)
	s.emit(EvDump, L3, "-> "+k.String(), ebcdic.HexDump(seq)...)
	s.log.Debug("-> " + k.String())
	return s.sendRaw(seq)
}

// sendBuffer builds and sends one transmission buffer. The BCB carries
// our outbound sequence count, which advances for every buffer we send
// (null buffers included, spec 03 section 3). FCS is always "all streams
// enabled": gonje never throttles its peer.
func (s *Session) sendBuffer(recs []records.Record) error {
	b := &records.Buffer{BCB: 0x80 | s.outSeq, FCS: s.ourFCS, Records: recs}
	dle, _, _ := s.Framing()
	s.outSeq = (s.outSeq + 1) & 0x0F
	if len(recs) == 0 {
		s.emit(EvKeepalive, L3, "keepalive ->")
		s.count(func(st *Stats) { st.KeepaliveOut++ })
	}
	for _, r := range recs {
		s.dumpRecord("->", r)
	}
	s.lastBlock = b.EncodeFramed(dle)
	return s.sendRaw(s.lastBlock)
}

// dumpRecord reports one (decompressed) record at L3 and in the log.
// Signon passwords are masked (XX / '*') in both.
func (s *Session) dumpRecord(dir string, r records.Record) {
	full := r.Bytes()
	title := fmt.Sprintf("%s %s %02X/%02X (%d bytes)", dir, r.Describe(), r.RCB, r.SRCB, len(full))
	lines := ebcdic.HexDumpMasked(full, records.SignonMask(full))
	s.emit(EvDump, L3, title, lines...)
	logLines := []string{title}
	for _, l := range lines {
		logLines = append(logLines, "    "+l)
	}
	s.log.Lines(logx.Debug, logLines)
}

// ---------------------------------------------------------------------
// Handshake
// ---------------------------------------------------------------------

// ClientOpen performs the connecting side's control record exchange:
// send OPEN, expect ACK. A NAK is returned as *NAKError.
func ClientOpen(conn net.Conn, p Params) error {
	p.defaults()
	rip := addrIP(conn.LocalAddr())
	if p.NatOut != nil {
		rip = p.NatOut // behind NAT: tell the peer our public address
	}
	open := &tcpip.Control{Type: tcpip.TypeOpen, RHost: p.Local, RIP: rip,
		OHost: p.Remote, OIP: addrIP(conn.RemoteAddr())}
	b := open.Marshal()
	p.Log.Dump(logx.Debug, "-> "+open.String(), b)
	p.Handler.Event(Event{Time: time.Now(), Kind: EvDump, Level: L3, Node: p.Remote,
		Text: "-> " + open.String(), Lines: ebcdic.HexDump(b)})
	_ = conn.SetDeadline(time.Now().Add(p.Handshake))
	defer conn.SetDeadline(time.Time{})
	if _, err := conn.Write(b); err != nil {
		return err
	}
	reply, raw, err := tcpip.ReadControl(conn)
	if raw != nil {
		p.Log.Dump(logx.Debug, "<- control record", raw)
	}
	if err != nil {
		return fmt.Errorf("reading OPEN reply: %w", err)
	}
	p.Handler.Event(Event{Time: time.Now(), Kind: EvDump, Level: L3, Node: p.Remote,
		Text: "<- " + reply.String(), Lines: ebcdic.HexDump(raw)})
	switch reply.Type {
	case tcpip.TypeACK:
		if reply.RHost != "" && !strings.EqualFold(reply.RHost, p.Remote) {
			p.Log.Warn(fmt.Sprintf("ACK from node %s, expected %s", reply.RHost, p.Remote))
		}
		return nil
	case tcpip.TypeNAK:
		return &NAKError{Reason: reply.Reason}
	}
	return fmt.Errorf("unexpected control record %s", reply.Type)
}

// addrIP extracts the IP of a TCP address.
func addrIP(a net.Addr) net.IP {
	if ta, ok := a.(*net.TCPAddr); ok {
		return ta.IP
	}
	return nil
}

// signonRecord builds our I or J record.
func (s *Session) signonRecord(srcb byte, peer *records.Signon) records.Record {
	so := &records.Signon{SRCB: srcb, Node: s.p.Local, Qual: 1,
		BufSize: uint16(s.p.BufSize), NodePW: s.p.Password}
	if srcb == records.SRCBSignonJ {
		// J advertises the negotiated size: min(peer's I size, ours).
		if peer != nil && peer.BufSize >= 300 && int(peer.BufSize) < s.p.BufSize {
			so.BufSize = peer.BufSize
		}
		// Non-path-manager connection: CES = X'FFFFFFFF'. Agreed features
		// = I features AND ours; gonje supports none of the optional
		// features (multi-job, packing, mixed RCBs ...), so all zero.
		so.Event = records.CESPredefined
	}
	return so.Record()
}

// SignonPrimary runs the connecting side's signon (spec 01 section 3.5):
//
//	-> SOH ENQ   <- DLE ACK0   -> I   <- J
//
// The peer may also answer our SOH ENQ with SYN NAK (it leaves the
// primary role to us) or with its own SOH ENQ (it bids too); see
// signonPrimaryReply.
func (s *Session) SignonPrimary() error {
	if err := s.sendLine(records.SOHENQ); err != nil {
		return err
	}
	return s.signonPrimaryReply()
}

// maxSYNNAKRetries bounds how often we repeat our SOH ENQ to a peer that
// answers it with SYN NAK. A peer that keeps doing so will never let the
// signon finish; the BSC init rules (spec 03 section 7.1, Table 62:
// "rewrite SOH ENQ") are bounded too.
const maxSYNNAKRetries = 3

// signonPrimaryReply reads the peer's answer to the SOH ENQ we just sent.
//
//   - DLE ACK0: we are primary (the node that RECEIVES DLE ACK0 sends I,
//     spec 03 section 7.1).
//   - SYN NAK: the peer leaves the choice to us. We bid again with SOH
//     ENQ, at most maxSYNNAKRetries times.
//   - SOH ENQ: contention, both sides bid. The TCP/IP chapter only says
//     JES closes the connection; the BSC rule (spec 03 section 7.1) is
//     that the side whose SOH ENQ ARRIVES FIRST becomes secondary. The
//     peer's SOH ENQ came after ours reached it (it is its answer), so
//     ours arrived first and we yield: DLE ACK0, then the secondary half
//     (wait for I, send J). funetnje does the same in its start state.
//
// Anything else ends the attempt.
func (s *Session) signonPrimaryReply() error {
	for naks := 0; ; naks++ {
		rec, err := s.readRecordTimeout()
		if err != nil {
			return fmt.Errorf("waiting for DLE ACK0: %w", err)
		}
		switch k := records.Classify(rec); k {
		case records.KindDLEACK0:
			s.emit(EvDump, L3, "<- DLE ACK0", ebcdic.HexDump(rec)...)
			return s.primaryIJ()
		case records.KindSYNNAK:
			s.emit(EvDump, L3, "<- SYN NAK", ebcdic.HexDump(rec)...)
			if naks >= maxSYNNAKRetries {
				return fmt.Errorf("no DLE ACK0, peer answered SYN NAK %d times", naks+1)
			}
			if err := s.sendLine(records.SOHENQ); err != nil {
				return err
			}
		case records.KindSOHENQ:
			s.emit(EvDump, L3, "<- SOH ENQ (contention: we yield)", ebcdic.HexDump(rec)...)
			return s.yieldPrimary()
		default:
			return fmt.Errorf("expected DLE ACK0 after SOH ENQ, got %s", k)
		}
	}
}

// yieldPrimary answers the peer's SOH ENQ (contention) with DLE ACK0 and
// continues as secondary.
//
// Double-yield guard: a peer with the same rule also yields, so it
// answers our SOH ENQ with DLE ACK0 while we wait for its I. Waiting on
// would deadlock (each side waits for the other's I). The node that
// receives DLE ACK0 is primary, so in that case we take the role and
// send our I. This happens once only: from then on primaryIJ waits for
// J and any other answer ends the attempt.
func (s *Session) yieldPrimary() error {
	if err := s.sendLine(records.DLEACK0); err != nil {
		return err
	}
	rec, err := s.readRecordTimeout()
	if err != nil {
		return fmt.Errorf("waiting for signon: %w", err)
	}
	if records.Classify(rec) == records.KindDLEACK0 {
		s.emit(EvDump, L3, "<- DLE ACK0 (peer yielded too: we are primary)", ebcdic.HexDump(rec)...)
		return s.primaryIJ()
	}
	// Not DLE ACK0: put the record back in front of the rest of its
	// block, so the secondary half reads it (normally the peer's I).
	s.pending = append([][]byte{rec}, s.pending...)
	return s.secondaryIJ()
}

// primaryIJ sends I and waits for J.
//
// We send first, so the framing is a choice made blind (framing.go). If
// the peer resets, closes, refuses or stays silent instead of answering
// with J, that form is reported as rejected (FramingRejected) so the
// node tries the other form next time.
func (s *Session) primaryIJ() error {
	s.announceFraming()
	if err := s.sendBuffer([]records.Record{s.signonRecord(records.SRCBSignonI, nil)}); err != nil {
		return err
	}
	s.fmu.Lock()
	s.iSent = true
	s.fmu.Unlock()
	s.emit(EvSignon, L2, fmt.Sprintf("signon I sent (we are primary, bufsz=%d)", s.p.BufSize))
	j, err := s.waitSignon(records.SRCBSignonJ)
	if err != nil {
		s.rejectFraming(err)
		return err
	}
	s.finishSignon(j, true)
	// J answered our I, so it is our turn: Run answers J right away.
	s.takeTurn(time.Now(), false, false)
	return nil
}

// SignonSecondary runs the accepting side's signon. The connector either
// bids for primary (SOH ENQ: we answer DLE ACK0, get I, send J) or lets
// us choose (SYN NAK: we send SOH ENQ, get DLE ACK0 and become primary).
func (s *Session) SignonSecondary() error {
	rec, err := s.readRecordTimeout()
	if err != nil {
		return fmt.Errorf("waiting for SOH ENQ: %w", err)
	}
	k := records.Classify(rec)
	s.emit(EvDump, L3, "<- "+k.String(), ebcdic.HexDump(rec)...)
	switch k {
	case records.KindSOHENQ:
		if err := s.sendLine(records.DLEACK0); err != nil {
			return err
		}
	case records.KindSYNNAK:
		// The peer leaves the primary role to us (z/VM RSCS does): we
		// bid with SOH ENQ and from then on act as the dialling side
		// does, so its answer may also be SYN NAK or SOH ENQ.
		if err := s.sendLine(records.SOHENQ); err != nil {
			return err
		}
		return s.signonPrimaryReply()
	default:
		return fmt.Errorf("expected SOH ENQ or SYN NAK, got %s", k)
	}
	return s.secondaryIJ()
}

// secondaryIJ is the secondary's half after our DLE ACK0 went out: wait
// for I, answer J. Shared by SignonSecondary and by a dial that lost the
// contention (yieldPrimary). The J goes out in the form of the peer's I
// (waitSignon mirrors it).
func (s *Session) secondaryIJ() error {
	i, err := s.waitSignon(records.SRCBSignonI)
	if err != nil {
		return err
	}
	if err := s.sendBuffer([]records.Record{s.signonRecord(records.SRCBSignonJ, i)}); err != nil {
		return err
	}
	// Our J answered the I: now it is the primary's turn.
	s.finishSignon(i, false)
	return nil
}

// waitSignon reads until a buffer holding the wanted signon record
// arrives. Null buffers and DLE ACK0 before it are tolerated (fig 37
// "optional null buffers").
func (s *Session) waitSignon(want byte) (*records.Signon, error) {
	for {
		rec, err := s.readRecordTimeout()
		if err != nil {
			return nil, fmt.Errorf("waiting for signon: %w", err)
		}
		s.lastRecv = time.Now()
		k := records.Classify(rec)
		if k == records.KindDLEACK0 {
			continue
		}
		if k != records.KindBuffer {
			return nil, fmt.Errorf("expected signon buffer, got %s", k)
		}
		s.mirrorFraming(rec)
		buf, err := records.Decode(rec)
		if err != nil && buf == nil {
			return nil, err
		}
		if _, err := s.checkBCB(buf.BCB); err != nil {
			return nil, err
		}
		s.peerFCS = buf.FCS
		if buf.IsNull() {
			continue
		}
		r := buf.Records[0]
		s.dumpRecord("<-", r)
		if r.RCB != records.RCBControl || r.SRCB != want {
			return nil, fmt.Errorf("expected %s, got %s", records.Record{RCB: records.RCBControl, SRCB: want}.Describe(), r.Describe())
		}
		so, err := records.ParseSignon(r)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(so.Node, s.p.Remote) {
			return nil, fmt.Errorf("signon from node %s, expected %s", so.Node, s.p.Remote)
		}
		if s.p.Password != "" && !strings.EqualFold(so.NodePW, s.p.Password) &&
			!strings.EqualFold(so.LinePW, s.p.Password) {
			return nil, fmt.Errorf("signon password mismatch from %s", so.Node)
		}
		return so, nil
	}
}

// finishSignon records the negotiated parameters.
func (s *Session) finishSignon(peer *records.Signon, primary bool) {
	limit := s.p.BufSize
	if int(peer.BufSize) >= 300 && int(peer.BufSize) < limit {
		limit = int(peer.BufSize)
	}
	s.bufLimit = limit - bufMargin
	now := time.Now()
	s.lastRecv = now
	s.signedOnAt = now
	s.count(func(st *Stats) {
		st.Primary, st.Since, st.PeerBufSize = primary, now, int(peer.BufSize)
	})
	role := "secondary"
	if primary {
		role = "primary"
	}
	s.fmu.Lock()
	s.iSent = false // answered: the framing is confirmed
	s.fmu.Unlock()
	// One line, logged by the event bus.
	s.emit(EvSignon, L2, fmt.Sprintf("signon complete as %s: %s, buffer limit %d", role, peer, s.bufLimit))
}

// checkBCB validates an inbound block sequence byte (Table 45).
//
// Default (tolerant): TCP cannot lose or duplicate blocks, so a mismatch
// almost certainly means the peer counts differently (e.g. does not count
// null buffers). We log it as NONSTANDARD, resynchronise and carry on.
//
// With StrictBCB (remote_strict_bcb=yes) the manual's rules apply: a
// duplicate count (= last received) means the peer lost our last block,
// so the block is discarded (dup=true) and the caller retransmits; any
// other wrong count sends E0 <expected> and ends the session.
func (s *Session) checkBCB(b byte) (dup bool, err error) {
	switch b & 0xF0 {
	case 0x90: // bypass sequence check; counter does not advance
		return false, nil
	case 0xA0: // reset: expected count := cccc, then this block counts
		s.inSeq = (b + 1) & 0x0F
		return false, nil
	case 0x80:
		c := b & 0x0F
		if c == s.inSeq {
			s.inSeq = (c + 1) & 0x0F
			return false, nil
		}
		s.count(func(st *Stats) { st.BCBErrors++ })
		if s.p.StrictBCB {
			if c == (s.inSeq-1)&0x0F {
				s.log.Warn(fmt.Sprintf("duplicate BCB %X: block discarded, last block resent", c))
				return true, nil
			}
			// E0 SRCB = expected count; sent BCB-style (X'80'|count)
			// like funetnje (spec 03 section 3, ambiguity 5).
			s.log.Error(fmt.Sprintf("BCB sequence error: got %X, expected %X; sending E0", c, s.inSeq))
			_ = s.sendBuffer([]records.Record{{RCB: records.RCBBCBErr, SRCB: 0x80 | s.inSeq}})
			return false, ErrBCBSequence
		}
		s.log.Warn(fmt.Sprintf("NONSTANDARD BCB sequence: got %X, expected %X (resynchronising)", c, s.inSeq))
		s.inSeq = (c + 1) & 0x0F
	default:
		s.log.Warn(fmt.Sprintf("NONSTANDARD invalid BCB %02X ignored", b))
	}
	return false, nil
}
