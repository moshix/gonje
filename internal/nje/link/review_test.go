// Copyright 2026 by moshix

package link

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"gonje/internal/ebcdic"
	"gonje/internal/logx"
	"gonje/internal/nje/records"
	"gonje/internal/nje/tcpip"
)

// signonRaw makes gonje the secondary of a raw primary and returns after
// the J record was read.
func signonRaw(t *testing.T, p *rawPeer, errs chan error) {
	t.Helper()
	p.send(records.SOHENQ)
	if k := records.Classify(p.read()); k != records.KindDLEACK0 {
		t.Fatalf("expected DLE ACK0, got %s", k)
	}
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", BufSize: 4096}).Record())
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	p.readBuf() // J
}

// readOne reads exactly one block from gonje and returns its single TTR
// record and how long it took to arrive.
func (p *rawPeer) readOne(timeout time.Duration) ([]byte, time.Duration) {
	start := time.Now()
	_ = p.c.SetReadDeadline(start.Add(timeout))
	rec := p.read2()
	return rec, time.Since(start)
}

func (p *rawPeer) read2() []byte {
	recs, _, err := readBlockFrom(p)
	if err != nil {
		p.t.Fatalf("read: %v", err)
	}
	return recs[0]
}

// assertSilent checks that gonje sends nothing for d (it has no turn).
func (p *rawPeer) assertSilent(d time.Duration) {
	_ = p.c.SetReadDeadline(time.Now().Add(d))
	if _, err := p.br.Peek(1); err == nil {
		p.t.Fatal("gonje sent a block without having the turn")
	}
}

// TestStrictTurnTakingPeer drives gonje with a peer that alternates
// strictly (M1): it only ever sends one block after receiving one, and
// checks that gonje answers every block exactly once, with the idle
// throttle (100 ms, 200 ms, ... <= 1 s), and that messages in both
// directions get through quickly by riding on the turns.
func TestStrictTurnTakingPeer(t *testing.T) {
	s, p, h, errs := acceptRaw(t, 5*time.Second)
	signonRaw(t, p, errs)
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	defer s.Kill()

	var delays []time.Duration
	p.sendBuf(records.FCSNormal) // our idle answer to J
	for i := 0; i < 5; i++ {
		rec, d := p.readOne(1500 * time.Millisecond)
		if !isIdleRecord(rec) {
			t.Fatalf("exchange %d: expected a null buffer", i)
		}
		delays = append(delays, d)
		p.assertSilent(30 * time.Millisecond)
		p.sendBuf(records.FCSNormal)
	}
	for i, d := range delays {
		want := time.Duration(i+1) * idleStep
		if d < want-40*time.Millisecond || d > want+300*time.Millisecond {
			t.Errorf("idle reply %d after %v, want about %v", i, d, want)
		}
	}
	// gonje -> peer: the message rides on gonje's next turn (< 1.1 s).
	for _, m := range records.NewMessages("GONJE", "OPER", "RAW", "BOB", "TURN TEST") {
		if err := s.SendNMR(m); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	var got *records.Buffer
	for got == nil {
		rec, _ := p.readOne(2 * time.Second)
		if b, err := records.Decode(rec); err == nil && !b.IsNull() {
			got = b
			break
		}
		p.sendBuf(records.FCSNormal)
	}
	if time.Since(start) > 2*time.Second || got.Records[0].RCB != records.RCBNMR {
		t.Fatalf("NMR took %v / %+v", time.Since(start), got)
	}
	// The NMR was data: the peer's answer gets an immediate reply.
	p.sendBuf(records.FCSNormal)
	if rec, d := p.readOne(time.Second); !isIdleRecord(rec) || d > 200*time.Millisecond {
		t.Fatalf("reply after data took %v", d)
	}
	// peer -> gonje on the peer's turn.
	m := records.NewMessages("RAW", "BOB", "GONJE", "OPER", "BACK AT YOU")[0]
	p.sendBuf(records.FCSNormal, m.Record())
	h.waitFor(t, "NMR from strict peer", func() bool { return len(h.nmrs) == 1 })
	if rec, _ := p.readOne(time.Second); !isIdleRecord(rec) {
		t.Fatal("expected the null answer to the NMR")
	}
	p.assertSilent(100 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("session ended: %v", err)
	default:
	}
}

// TestDLEACK0IsAnswered: JES2 acknowledges with DLE ACK0; that is a
// transmission too and must get an answer (M1).
func TestDLEACK0IsAnswered(t *testing.T) {
	s, p, _, errs := acceptRaw(t, 5*time.Second)
	signonRaw(t, p, errs)
	go s.Run()
	defer s.Kill()
	p.send(records.DLEACK0)
	if rec, d := p.readOne(1500 * time.Millisecond); !isIdleRecord(rec) || d > 400*time.Millisecond {
		t.Fatalf("no prompt answer to DLE ACK0 (%v)", d)
	}
}

// TestKickStart: if the peer stops answering, gonje sends a null buffer
// every keepalive interval and declares the link dead after 3x.
func TestKickStart(t *testing.T) {
	s, p, _, errs := acceptRaw(t, 300*time.Millisecond)
	signonRaw(t, p, errs)
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	start := time.Now()
	rec, _ := p.readOne(2 * time.Second)
	if !isIdleRecord(rec) || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("kick-start after %v", time.Since(start))
	}
	if err := <-done; !errors.Is(err, ErrDead) {
		t.Fatalf("expected ErrDead, got %v", err)
	}
}

func TestStrictBCB(t *testing.T) {
	s, p, _, errs := acceptRawP(t, 5*time.Second, func(pp *Params) { pp.StrictBCB, pp.StrictTurn = true, true })
	signonRaw(t, p, errs)
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	p.seq = 5 // wrong count (expected 1)
	p.sendBuf(records.FCSNormal)
	b := p.readBuf()
	if b.Records[0].RCB != records.RCBBCBErr || b.Records[0].SRCB != 0x81 {
		t.Fatalf("expected E0 81, got %s %02X", b.Records[0].Describe(), b.Records[0].SRCB)
	}
	if err := <-done; !errors.Is(err, ErrBCBSequence) {
		t.Fatalf("expected ErrBCBSequence, got %v", err)
	}
	// Strict mode also ends the session on a received E0.
	s, p, _, errs = acceptRawP(t, 5*time.Second, func(pp *Params) { pp.StrictBCB, pp.StrictTurn = true, true })
	signonRaw(t, p, errs)
	go func() { done <- s.Run() }()
	p.sendBuf(records.FCSNormal, records.Record{RCB: records.RCBBCBErr, SRCB: 0x83})
	if err := <-done; !errors.Is(err, ErrBCBSequence) {
		t.Fatalf("expected ErrBCBSequence on E0, got %v", err)
	}
	// Tolerant default: a wrong count is accepted.
	s, p, _, errs = acceptRaw(t, 5*time.Second)
	signonRaw(t, p, errs)
	go func() { done <- s.Run() }()
	defer s.Kill()
	p.seq = 9
	p.sendBuf(records.FCSNormal, records.NewMessages("RAW", "U", "GONJE", "V", "X")[0].Record())
	if rec, _ := p.readOne(time.Second); !isIdleRecord(rec) {
		t.Fatal("tolerant mode did not carry on")
	}
}

func TestStrictBCBDuplicateResends(t *testing.T) {
	s, p, _, errs := acceptRawP(t, 5*time.Second, func(pp *Params) { pp.StrictBCB, pp.StrictTurn = true, true })
	signonRaw(t, p, errs)
	go s.Run()
	defer s.Kill()
	p.sendBuf(records.FCSNormal) // count 1
	first, _ := p.readOne(time.Second)
	p.seq = 1 // duplicate of the block we just sent
	p.sendBuf(records.FCSNormal)
	again, _ := p.readOne(time.Second)
	if !bytes.Equal(first, again) {
		t.Fatalf("duplicate BCB not answered by a retransmit:\n% X\n% X", first, again)
	}
}

// TestUndeliveredNMRReported: NMRs still queued when the link drops are
// reported as errors (item 9 / review C4), never dropped silently.
func TestUndeliveredNMRReported(t *testing.T) {
	s, p, h, errs := acceptRaw(t, 5*time.Second)
	signonRaw(t, p, errs)
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	// gonje is secondary and the peer never gives it the turn.
	for _, m := range records.NewMessages("GONJE", "OPER", "RAW", "BOB", "LOST MESSAGE") {
		s.SendNMR(m)
	}
	time.Sleep(100 * time.Millisecond)
	p.c.Close()
	<-done
	h.waitFor(t, "undelivered report", func() bool {
		for _, e := range h.events {
			if e.Kind == EvError && strings.Contains(e.Text, "not delivered") && strings.Contains(e.Text, "LOST MESSAGE") {
				return true
			}
		}
		return false
	})
	if err := s.SendNMR(&records.NMR{}); err != ErrClosed {
		t.Fatal("SendNMR after close")
	}
}

func TestJobSizeCap(t *testing.T) {
	s, p, h, errs := acceptRawP(t, 5*time.Second, func(pp *Params) { pp.MaxJob = 1000 })
	signonRaw(t, p, errs)
	go s.Run()
	defer s.Kill()
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x90, SRCB: 0x99})
	if b := p.readBuf(); b.Records[0].RCB != records.RCBPermit {
		t.Fatal("expected permit")
	}
	var deny *records.Buffer
	for i := 0; i < 10 && deny == nil; i++ {
		p.sendBuf(records.FCSNormal, records.Record{RCB: 0x99, SRCB: 0x80, Data: bytes.Repeat([]byte{0xC1}, 200)})
		rec, _ := p.readOne(time.Second)
		if b, err := records.Decode(rec); err == nil && !b.IsNull() {
			deny = b
		}
	}
	if deny == nil || deny.Records[0].RCB != records.RCBDeny || !bytes.Equal(deny.Records[0].Data, []byte{0x10, 0x04}) {
		t.Fatalf("expected B0 99 1004, got %+v", deny)
	}
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x99, SRCB: 0x00})
	p.readOne(time.Second)
	if len(h.datasets) != 0 {
		t.Fatal("oversized job delivered")
	}
}

// panicHandler panics on the first NMR to test recovery.
type panicHandler struct{ *fakeHandler }

func (panicHandler) NMR(string, *records.NMR) { panic("boom") }

func TestPanicBecomesSessionError(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	h := panicHandler{newHandler()}
	errs := make(chan error, 1)
	sc := make(chan *Session, 1)
	go func() {
		c, _ := ln.Accept()
		s := New(c, Params{Local: "GONJE", Remote: "RAW", Handler: h, Handshake: 2 * time.Second})
		sc <- s
		errs <- s.SignonSecondary()
	}()
	c, _ := net.Dial("tcp", ln.Addr().String())
	defer c.Close()
	p := &rawPeer{t: t, c: c, br: newBR(c)}
	s := <-sc
	signonRaw(t, p, errs)
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	p.sendBuf(records.FCSNormal, records.NewMessages("RAW", "U", "GONJE", "V", "X")[0].Record())
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "internal error: boom") {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not end")
	}
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestPasswordsNotLogged: signon passwords never reach the log or the L3
// panel, neither as text nor as hex.
func TestPasswordsNotLogged(t *testing.T) {
	var lb syncBuf
	s, p, h, errs := acceptRawP(t, 5*time.Second, func(pp *Params) {
		pp.Password = "SECRET"
		pp.Log = logx.New(&lb, logx.Debug)
	})
	p.send(records.SOHENQ)
	p.read()
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW",
		NodePW: "SECRET", LinePW: "LINEPW"}).Record())
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	p.readBuf()
	s.Kill()
	hexOf := func(x string) string {
		var parts []string
		for _, b := range ebcdic.Encode(x) {
			parts = append(parts, strings.ToUpper(string("0123456789ABCDEF"[b>>4])+string("0123456789ABCDEF"[b&15])))
		}
		return strings.Join(parts, " ")
	}
	logText := lb.String()
	h.mu.Lock()
	var panel strings.Builder
	for _, e := range h.events {
		panel.WriteString(e.Text + "\n" + strings.Join(e.Lines, "\n") + "\n")
	}
	h.mu.Unlock()
	for _, where := range []string{logText, panel.String()} {
		for _, secret := range []string{"SECRET", "LINEPW", hexOf("SECR"), hexOf("LINE")} {
			if strings.Contains(where, secret) {
				t.Fatalf("password leaked (%q):\n%s", secret, where)
			}
		}
		if !strings.Contains(where, "XX XX XX XX") {
			t.Fatalf("no masked bytes in:\n%s", where)
		}
	}
}

func readBlockFrom(p *rawPeer) ([][]byte, []byte, error) { return tcpip.ReadBlock(p.br) }

func newBR(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }

func TestStatsAddSub(t *testing.T) {
	a := Stats{MsgsIn: 3, BytesOut: 100, FilesIn: 1, NAKsIn: 2, Primary: true}
	b := Stats{MsgsIn: 1, BytesOut: 40, FilesIn: 1, BCBErrors: 5}
	sum := a.Add(b)
	if sum.MsgsIn != 4 || sum.BytesOut != 140 || sum.FilesIn != 2 || sum.BCBErrors != 5 || !sum.Primary {
		t.Fatalf("add %+v", sum)
	}
	if d := sum.Sub(b); d.MsgsIn != 3 || d.BytesOut != 100 || d.FilesIn != 1 || d.BCBErrors != 0 || d.NAKsIn != 2 {
		t.Fatalf("sub %+v", d)
	}
}

// TestFreeModeSilentPeer reproduces the first interop test (HOUCICS):
// after J the peer sends one NMR and then never sends anything again.
// In free mode our message, command and file must still go out at once,
// and the silent link must stay up well past 3x keepalive.
func TestFreeModeSilentPeer(t *testing.T) {
	s, p, h, errs := acceptRawP(t, 200*time.Millisecond, func(pp *Params) { pp.StrictTurn = false })
	p.send(records.SOHENQ)
	p.read()
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", BufSize: 1012}).Record())
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	j := p.readBuf()
	if so, _ := records.ParseSignon(j.Records[0]); so.BufSize != 1012 {
		t.Fatalf("J advertises %d, want the negotiated 1012", so.BufSize)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	defer s.Kill()
	p.sendBuf(records.FCSNormal, records.NewMessages("RAW", "SYSTEM", "GONJE", "OPER", "SIGNON OF LINK COMPLETE")[0].Record())
	h.waitFor(t, "signon NMR", func() bool { return len(h.nmrs) == 1 })

	// From here on the peer only reads (except one A0 to permit the file).
	for _, m := range records.NewMessages("GONJE", "OPER", "RAW", "HERC01", "HELLO") {
		s.SendNMR(m)
	}
	cmd, _ := records.NewCommand("GONJE", "OPER", "RAW", "D T")
	s.SendNMR(cmd)
	h.enqueue(&OutboundFile{Name: "f", ToNode: "RAW", ToUser: "HERC01", Records: [][]byte{{0xC1}, {0xC2}}})
	s.Kick()
	start := time.Now()
	var gotMsg, gotCmd, gotRI bool
	for !(gotMsg && gotCmd && gotRI) {
		rec, _ := p.readOne(time.Second)
		b, err := records.Decode(rec)
		if err != nil || b.IsNull() {
			continue
		}
		for _, r := range b.Records {
			switch {
			case r.RCB == records.RCBNMR:
				m, _ := records.ParseNMR(r.Data)
				gotCmd = gotCmd || m.IsCommand()
				gotMsg = gotMsg || !m.IsCommand()
			case r.RCB == records.RCBRequest:
				gotRI = true
			}
		}
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("message/command/request took %v", time.Since(start))
	}
	// Permit the file; header, dataset header, data, trailer and EOF must
	// all arrive without the peer saying anything else.
	p.sendBuf(records.FCSNormal, records.Record{RCB: records.RCBPermit, SRCB: 0x99})
	sawEOF := false
	for !sawEOF {
		rec, _ := p.readOne(time.Second)
		if b, err := records.Decode(rec); err == nil {
			for _, r := range b.Records {
				if r.RCB == 0x99 && isEOF(r) {
					sawEOF = true
				}
			}
		}
	}
	// Silent peer: the link must survive far beyond 3x keepalive, with
	// only our own keepalive null buffers going out.
	deadline := time.Now().Add(1500 * time.Millisecond)
	nulls := 0
	for time.Now().Before(deadline) {
		rec, _ := p.readOne(time.Second)
		if isIdleRecord(rec) {
			nulls++
		}
	}
	select {
	case err := <-done:
		t.Fatalf("link dropped on a silent peer: %v", err)
	default:
	}
	if nulls < 3 || nulls > 12 {
		t.Fatalf("%d keepalive nulls in 1.5 s with keepalive 200ms", nulls)
	}
}

// TestNoEOFAck: like HOUCICS, the peer takes the file but never sends C0.
// After EOFWait the file counts as sent (unconfirmed), the next file
// starts, and a late C0 is ignored.
func TestNoEOFAck(t *testing.T) {
	s, p, h, errs := acceptRawP(t, 5*time.Second, func(pp *Params) { pp.EOFWait = 300 * time.Millisecond })
	signonRaw(t, p, errs)
	go s.Run()
	defer s.Kill()
	h.enqueue(&OutboundFile{Name: "one", ToNode: "RAW", ToUser: "U", Records: [][]byte{{0xC1}}})
	h.enqueue(&OutboundFile{Name: "two", ToNode: "RAW", ToUser: "U", Records: [][]byte{{0xC2}}})
	s.Kick()
	if b := p.readBuf(); b.Records[0].RCB != records.RCBRequest {
		t.Fatal("expected request")
	}
	p.sendBuf(records.FCSNormal, records.Record{RCB: records.RCBPermit, SRCB: 0x99})
	for eof := false; !eof; {
		for _, r := range p.readBuf().Records {
			eof = eof || (r.RCB == 0x99 && isEOF(r))
		}
	}
	start := time.Now()
	// No C0. The second file's request must follow after EOFWait.
	if b := p.readBuf(); b.Records[0].RCB != records.RCBRequest {
		t.Fatalf("expected the next request, got %s", b.Records[0].Describe())
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("gave up waiting for C0 after only %v", d)
	}
	h.mu.Lock()
	if len(h.done) != 1 || h.done[0] != nil || len(h.queue) != 1 {
		t.Fatalf("done %v queue %d", h.done, len(h.queue))
	}
	unconfirmed := false
	for _, e := range h.events {
		unconfirmed = unconfirmed || (e.Kind == EvFileOut && strings.Contains(e.Text, "one sent to RAW/U (peer did not confirm receipt)"))
	}
	h.mu.Unlock()
	if !unconfirmed || s.Stats().FilesOut != 1 {
		t.Fatal("unconfirmed file not reported/counted")
	}
	// A late C0 while the next request waits: ignored, nothing breaks.
	p.sendBuf(records.FCSNormal, records.Record{RCB: records.RCBAckEOF, SRCB: 0x99})
	time.Sleep(100 * time.Millisecond)
	if len(h.done) != 1 {
		t.Fatal("late C0 completed a file")
	}
}

// TestConfiguredBufferSize: our I and J carry the configured size (J
// capped at the peer's), and we send at most min(ours, peer's).
func TestConfiguredBufferSize(t *testing.T) {
	// We are secondary: peer offers 4000, we are configured for 2000.
	s, p, _, errs := acceptRawP(t, 5*time.Second, func(pp *Params) { pp.BufSize = 2000 })
	p.send(records.SOHENQ)
	p.read()
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", BufSize: 4000}).Record())
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	j, _ := records.ParseSignon(p.readBuf().Records[0])
	if j.BufSize != 2000 || s.bufLimit != 2000-bufMargin {
		t.Fatalf("J %d, limit %d", j.BufSize, s.bufLimit)
	}
	s.Kill()
	// Peer offers less (1012): J says 1012, the send limit follows it.
	s, p, _, errs = acceptRawP(t, 5*time.Second, func(pp *Params) { pp.BufSize = 2000 })
	p.send(records.SOHENQ)
	p.read()
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", BufSize: 1012}).Record())
	<-errs
	j, _ = records.ParseSignon(p.readBuf().Records[0])
	if j.BufSize != 1012 || s.bufLimit != 1012-bufMargin {
		t.Fatalf("J %d, limit %d", j.BufSize, s.bufLimit)
	}
	s.Kill()
	// Our I carries the configured size.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	got := make(chan uint16, 1)
	go func() {
		c, _ := ln.Accept()
		defer c.Close()
		rp := &rawPeer{t: t, c: c, br: bufio.NewReader(c)}
		rp.read() // SOH ENQ
		rp.send(records.DLEACK0)
		i, _ := records.ParseSignon(rp.readBuf().Records[0])
		got <- i.BufSize
	}()
	c, _ := net.Dial("tcp", ln.Addr().String())
	defer c.Close()
	ps := New(c, Params{Local: "GONJE", Remote: "RAW", BufSize: 3000, Handler: newHandler(), Handshake: time.Second})
	go ps.SignonPrimary()
	if n := <-got; n != 3000 {
		t.Fatalf("I offers %d", n)
	}
}

// TestInboundStreamLimit: with max_streams=2 a third concurrent request
// is refused (the sender keeps the file); when a stream frees up we send
// D0 so the sender asks again. Our FCS only enables streams 1-2.
func TestInboundStreamLimit(t *testing.T) {
	s, p, _, errs := acceptRawP(t, 5*time.Second, func(pp *Params) { pp.MaxStreams = 2 })
	signonRaw(t, p, errs)
	go s.Run()
	defer s.Kill()
	want := func(rcb, srcb byte) *records.Buffer {
		t.Helper()
		b := p.readBuf()
		if b.Records[0].RCB != rcb || b.Records[0].SRCB != srcb {
			t.Fatalf("got %s %02X, want %02X %02X", b.Records[0].Describe(), b.Records[0].SRCB, rcb, srcb)
		}
		return b
	}
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x90, SRCB: 0x99})
	want(records.RCBPermit, 0x99)
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x90, SRCB: 0xA9})
	want(records.RCBPermit, 0xA9)
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x90, SRCB: 0xB9})
	b := want(records.RCBDeny, 0xB9)
	if b.FCS != (records.FCS{0x8F, 0xCF}) { // always all streams (NJE38 needs it)
		t.Fatalf("FCS % X", b.FCS)
	}
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x99, SRCB: 0x00})
	want(records.RCBAckEOF, 0x99)
	want(records.RCBReady, 0x99)
}

func TestStreamFCS(t *testing.T) {
	if streamFCS(1) != (records.FCS{0x8F, 0xCF}) || streamFCS(8) != (records.FCS{0x8F, 0xCF}) {
		t.Fatalf("% X % X", streamFCS(1), streamFCS(8))
	}
}

// TestStreamSettle: after signon no file request goes out until the
// peer's first data block (HOUCICS sends its signon NMR ~3 s after J),
// or until the settle time when the peer stays silent. NMRs go at once.
func TestStreamSettle(t *testing.T) {
	for _, peerTalks := range []bool{true, false} {
		s, p, h, errs := acceptRawP(t, 5*time.Second, func(pp *Params) { pp.StreamSettle = 600 * time.Millisecond })
		signonRaw(t, p, errs)
		go s.Run()
		h.enqueue(&OutboundFile{Name: "f", ToNode: "RAW", ToUser: "U", Records: [][]byte{{0xC1}}})
		for _, m := range records.NewMessages("GONJE", "OPER", "RAW", "U", "EARLY") {
			s.SendNMR(m)
		}
		s.Kick()
		start := time.Now()
		if b := p.readBuf(); b.Records[0].RCB != records.RCBNMR {
			t.Fatalf("expected the NMR first, got %s", b.Records[0].Describe())
		}
		if peerTalks {
			time.Sleep(200 * time.Millisecond)
			p.sendBuf(records.FCSNormal, records.NewMessages("RAW", "SYS", "GONJE", "*", "SIGNON COMPLETE")[0].Record())
		}
		var req time.Duration
		for req == 0 {
			b := p.readBuf()
			if b.Records[0].RCB == records.RCBRequest {
				req = time.Since(start)
			}
		}
		if peerTalks && (req < 180*time.Millisecond || req > 500*time.Millisecond) {
			t.Fatalf("request %v after signon, want right after the peer's NMR (~200ms)", req)
		}
		if !peerTalks && req < 550*time.Millisecond {
			t.Fatalf("request after only %v, before the settle time", req)
		}
		s.Kill()
	}
}

// TestEOFIsEmptyDataRecord: our EOF must be RCB, SRCB X'80', SCB 00 (an
// empty data record) as NJE38 sends and expects; SRCB X'00' made NJE38
// raise NJE939E reason code 01 on HOUCICS. Both forms are accepted inbound.
func TestEOFIsEmptyDataRecord(t *testing.T) {
	b := &records.Buffer{BCB: 0x80, FCS: records.FCSNormal,
		Records: []records.Record{{RCB: 0x99, SRCB: records.SRCBData}}}
	wire := b.Encode()
	want := []byte{0x99, 0x80, 0x00, 0x00} // record, SCB 00, end of block
	if !bytes.Equal(wire[len(wire)-4:], want) {
		t.Fatalf("EOF bytes % X, want ... % X", wire, want)
	}
	back, err := records.Decode(wire)
	if err != nil || len(back.Records) != 1 || !isEOF(back.Records[0]) {
		t.Fatalf("decoded EOF %+v err %v", back, err)
	}
	if !isEOF(records.Record{RCB: 0x99, SRCB: records.SRCBEOF}) {
		t.Fatal("SRCB 00 EOF not accepted")
	}
	if isEOF(records.Record{RCB: 0x99, SRCB: records.SRCBData, Data: []byte{0x40}}) {
		t.Fatal("a one-byte data record is not EOF")
	}
}

// TestRecordLengthByte: unspanned data records carry a 1-byte length in
// front (figs 20/21). Vector from the live NJE38 trace on 2026-11-09:
// NJE38 sent "50" + 80 bytes. Records without the prefix are kept as is,
// and a blank-truncated record is padded back to the LRECL.
func TestRecordLengthByte(t *testing.T) {
	var lb bytes.Buffer
	s := &Session{log: logx.New(&lb, logx.Debug)}
	st := &rxStream{rcb: 0x99}
	rec := append([]byte{0x50}, bytes.Repeat([]byte{0xC1}, 80)...)
	if got := s.stripRecordLength(st, rec); len(got) != 80 || got[0] != 0xC1 {
		t.Fatalf("NJE38 record: got %d bytes", len(got))
	}
	// Truncated trailing blanks: length 80, only 3 bytes sent, LRECL 80.
	st.cur = &InboundDataset{Header: &records.DatasetHeader{LRECL: 80}}
	if got := s.stripRecordLength(st, []byte{0x50, 0xC1, 0xC2, 0xC3}); len(got) != 80 || got[79] != 0x40 {
		t.Fatalf("truncated record: got %d bytes", len(got))
	}
	// No prefix ("Hello"): the first byte is not a length, keep it.
	hello := []byte{0xC8, 0x85, 0x93, 0x93, 0x96}
	if got := s.stripRecordLength(st, hello); !bytes.Equal(got, hello) {
		t.Fatalf("unprefixed record changed: % X", got)
	}
}

// TestOpenCarriesNatOut: behind NAT our OPEN names the public address.
func TestOpenCarriesNatOut(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, _ := ln.Accept()
		defer c.Close()
		_, raw, _ := tcpip.ReadControl(c)
		got <- raw
		c.Write((&tcpip.Control{Type: tcpip.TypeACK, RHost: "B", OHost: "A"}).Marshal())
	}()
	c, _ := net.Dial("tcp", ln.Addr().String())
	defer c.Close()
	if err := ClientOpen(c, Params{Local: "A", Remote: "B", NatOut: net.IPv4(35, 192, 26, 118), Handler: newHandler()}); err != nil {
		t.Fatal(err)
	}
	raw := <-got
	if !bytes.Equal(raw[16:20], []byte{35, 192, 26, 118}) {
		t.Fatalf("RIP % X", raw[16:20])
	}
}
