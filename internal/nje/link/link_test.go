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
	"gonje/internal/nje/records"
	"gonje/internal/nje/tcpip"
)

// fakeHandler records everything a session reports.
type fakeHandler struct {
	mu       sync.Mutex
	events   []Event
	nmrs     []*records.NMR
	datasets []*InboundDataset
	queue    []*OutboundFile
	done     []error
	notify   chan struct{}
}

func newHandler() *fakeHandler { return &fakeHandler{notify: make(chan struct{}, 1000)} }

func (h *fakeHandler) poke() {
	select {
	case h.notify <- struct{}{}:
	default:
	}
}

func (h *fakeHandler) Event(ev Event) {
	h.mu.Lock()
	h.events = append(h.events, ev)
	h.mu.Unlock()
	h.poke()
}

func (h *fakeHandler) NMR(_ string, m *records.NMR) {
	h.mu.Lock()
	h.nmrs = append(h.nmrs, m)
	h.mu.Unlock()
	h.poke()
}

func (h *fakeHandler) Dataset(_ string, ds *InboundDataset) {
	h.mu.Lock()
	h.datasets = append(h.datasets, ds)
	h.mu.Unlock()
	h.poke()
}

func (h *fakeHandler) NextFile(string) *OutboundFile {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.queue) == 0 {
		return nil
	}
	return h.queue[0]
}

func (h *fakeHandler) FileDone(_ string, f *OutboundFile, err error) {
	h.mu.Lock()
	if len(h.queue) > 0 && h.queue[0] == f {
		h.queue = h.queue[1:]
	}
	h.done = append(h.done, err)
	h.mu.Unlock()
	h.poke()
}

func (h *fakeHandler) enqueue(f *OutboundFile) {
	h.mu.Lock()
	h.queue = append(h.queue, f)
	h.mu.Unlock()
}

// waitFor polls cond until it is true or the timeout expires.
func (h *fakeHandler) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		h.mu.Lock()
		ok := cond()
		h.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-h.notify:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

// pair connects two sessions over loopback TCP: A connects (primary),
// B accepts (secondary).
func pair(t *testing.T, ka time.Duration) (a, b *Session, ha, hb *fakeHandler) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ha, hb = newHandler(), newHandler()
	pa := Params{Local: "NODEA", Remote: "NODEB", Keepalive: ka, Handler: ha, Handshake: 5 * time.Second}
	pb := Params{Local: "NODEB", Remote: "NODEA", Keepalive: ka, Handler: hb, Handshake: 5 * time.Second}

	errs := make(chan error, 2)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errs <- err
			return
		}
		ctl, _, err := tcpip.ReadControl(c)
		if err != nil || ctl.Type != tcpip.TypeOpen || ctl.RHost != "NODEA" || ctl.OHost != "NODEB" {
			errs <- errors.New("bad OPEN")
			return
		}
		ack := &tcpip.Control{Type: tcpip.TypeACK, RHost: "NODEB", OHost: "NODEA"}
		if _, err := c.Write(ack.Marshal()); err != nil {
			errs <- err
			return
		}
		b = New(c, pb)
		errs <- b.SignonSecondary()
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := ClientOpen(c, pa); err != nil {
		t.Fatal(err)
	}
	a = New(c, pa)
	if err := a.SignonPrimary(); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	return a, b, ha, hb
}

func runBoth(a, b *Session) (ea, eb chan error) {
	ea, eb = make(chan error, 1), make(chan error, 1)
	go func() { ea <- a.Run() }()
	go func() { eb <- b.Run() }()
	return ea, eb
}

func TestSignonMessageFileSignoff(t *testing.T) {
	a, b, ha, hb := pair(t, 2*time.Second)
	if !a.Stats().Primary || b.Stats().Primary {
		t.Fatal("roles")
	}
	ea, eb := runBoth(a, b)

	// Message A -> B.
	for _, m := range records.NewMessages("NODEA", "MOSHE", "NODEB", "BOB", "HELLO THERE") {
		if err := a.SendNMR(m); err != nil {
			t.Fatal(err)
		}
	}
	hb.waitFor(t, "NMR at B", func() bool { return len(hb.nmrs) == 1 })
	if hb.nmrs[0].Text() != "HELLO THERE" || hb.nmrs[0].Sender() != "MOSHE" {
		t.Fatalf("NMR text %q", hb.nmrs[0].Text())
	}

	// File A -> B, big enough for several buffers and progress events.
	var recs [][]byte
	for i := 0; i < 400; i++ {
		recs = append(recs, ebcdic.Encode(strings.Repeat("LINE ", 1+i%40)))
	}
	recs = append(recs, []byte{0x40})
	f := &OutboundFile{Name: "test.txt", ToNode: "NODEB", ToUser: "BOB", FromUser: "MOSHE",
		JobName: "MOSHE", JobID: 7, DSName: "TEST", Proc: "TEST", Step: "TXT",
		RecFM: records.RecFmVariable, LRECL: 200, Records: recs, Queued: time.Now()}
	ha.enqueue(f)
	a.Kick()
	hb.waitFor(t, "dataset at B", func() bool { return len(hb.datasets) == 1 })
	ha.waitFor(t, "FileDone at A", func() bool { return len(ha.done) == 1 })
	if ha.done[0] != nil {
		t.Fatal(ha.done[0])
	}
	ds := hb.datasets[0]
	if ds.Job == nil || ds.Job.JobName != "MOSHE" || ds.Job.OrigNode != "NODEA" || ds.Job.JobID != 7 {
		t.Fatalf("job header %+v", ds.Job)
	}
	if ds.Header == nil || ds.Header.Name != "TEST" || ds.Header.Remote != "BOB" || ds.Trailer == nil {
		t.Fatalf("dataset header %+v", ds.Header)
	}
	if len(ds.Records) != len(recs) {
		t.Fatalf("records %d != %d", len(ds.Records), len(recs))
	}
	for i := range recs {
		if !bytes.Equal(ds.Records[i].Data, recs[i]) || ds.Records[i].CC != CCNone {
			t.Fatalf("record %d differs", i)
		}
	}
	if a.Stats().FilesOut != 1 || b.Stats().FilesIn != 1 || a.Stats().MsgsOut != 1 {
		t.Fatalf("stats %+v %+v", a.Stats(), b.Stats())
	}

	// Signoff from A.
	a.Close("test")
	if err := <-ea; !errors.Is(err, ErrLocalDrop) {
		t.Fatalf("A ended with %v", err)
	}
	if err := <-eb; !errors.Is(err, ErrPeerSignoff) {
		t.Fatalf("B ended with %v", err)
	}
	if IsReconnectable(ErrPeerSignoff) || !IsReconnectable(ErrDead) {
		t.Fatal("IsReconnectable")
	}
	if err := a.SendNMR(&records.NMR{}); err != ErrClosed {
		t.Fatal("SendNMR after close")
	}
	// L3 dumps were produced for both directions.
	hb.mu.Lock()
	var dumps int
	for _, e := range hb.events {
		if e.Kind == EvDump && e.Level == L3 {
			dumps++
		}
	}
	hb.mu.Unlock()
	if dumps < 10 {
		t.Fatalf("only %d dumps", dumps)
	}
}

func TestKeepaliveAndKill(t *testing.T) {
	a, b, _, _ := pair(t, 200*time.Millisecond)
	ea, eb := runBoth(a, b)
	time.Sleep(1200 * time.Millisecond)
	select {
	case err := <-ea:
		t.Fatalf("A ended early: %v", err)
	case err := <-eb:
		t.Fatalf("B ended early: %v", err)
	default:
	}
	if a.Stats().KeepaliveOut == 0 || b.Stats().KeepaliveOut == 0 || a.Stats().KeepaliveIn == 0 {
		t.Fatalf("no keepalives: %+v %+v", a.Stats(), b.Stats())
	}
	a.Kill()
	if err := <-ea; !IsReconnectable(err) {
		t.Fatalf("A: %v", err)
	}
	if err := <-eb; !IsReconnectable(err) {
		t.Fatalf("B: %v", err)
	}
}

// rawPeer is a hand-driven peer for protocol edge cases.
type rawPeer struct {
	t   *testing.T
	c   net.Conn
	br  *bufio.Reader
	seq byte
}

func (p *rawPeer) send(rec []byte) {
	if _, err := tcpip.WriteBlock(p.c, rec); err != nil {
		p.t.Fatal(err)
	}
}

func (p *rawPeer) sendBuf(fcs records.FCS, recs ...records.Record) {
	b := &records.Buffer{BCB: 0x80 | p.seq, FCS: fcs, Records: recs}
	p.seq = (p.seq + 1) & 0xF
	p.send(b.Encode())
}

func (p *rawPeer) read() []byte {
	_ = p.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	recs, _, err := tcpip.ReadBlock(p.br)
	if err != nil {
		p.t.Fatal(err)
	}
	return recs[0]
}

// readBuf reads until a non-null buffer arrives.
func (p *rawPeer) readBuf() *records.Buffer {
	for {
		rec := p.read()
		if records.Classify(rec) != records.KindBuffer {
			continue
		}
		b, err := records.Decode(rec)
		if err != nil {
			p.t.Fatal(err)
		}
		if !b.IsNull() {
			return b
		}
	}
}

// acceptRaw sets up a gonje secondary session talking to a raw peer.
func acceptRaw(t *testing.T, ka time.Duration) (*Session, *rawPeer, *fakeHandler, chan error) {
	t.Helper()
	// The hand-driven peer tests were written for BSC-style turn-taking.
	return acceptRawP(t, ka, func(p *Params) { p.StrictTurn = true })
}

// acceptRawP is acceptRaw with a hook to adjust the session parameters.
func acceptRawP(t *testing.T, ka time.Duration, adj func(*Params)) (*Session, *rawPeer, *fakeHandler, chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := newHandler()
	sc := make(chan *Session, 1)
	errs := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errs <- err
			return
		}
		p := Params{Local: "GONJE", Remote: "RAW", Keepalive: ka, Handler: h, Handshake: 5 * time.Second}
		adj(&p)
		s := New(c, p)
		sc <- s
		errs <- s.SignonSecondary()
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return <-sc, &rawPeer{t: t, c: c, br: bufio.NewReader(c)}, h, errs
}

func TestSecondaryWithRawPrimaryAndDeadPeer(t *testing.T) {
	s, p, h, errs := acceptRaw(t, 200*time.Millisecond)
	p.send(records.SOHENQ)
	if k := records.Classify(p.read()); k != records.KindDLEACK0 {
		t.Fatalf("expected DLE ACK0, got %s", k)
	}
	i := &records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", BufSize: 1024}
	p.sendBuf(records.FCSNormal, i.Record())
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	j := p.readBuf()
	so, err := records.ParseSignon(j.Records[0])
	if err != nil || so.SRCB != records.SRCBSignonJ || so.Node != "GONJE" || so.Event != records.CESPredefined {
		t.Fatalf("J: %+v %v", so, err)
	}
	if s.bufLimit != 1024-bufMargin {
		t.Fatalf("buffer limit %d", s.bufLimit)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	// A command arrives with JES2-style SRCB 00 and DLE ETB on the end.
	cmd, _ := records.NewCommand("RAW", "OPER", "GONJE", "Q SYS")
	raw := (&records.Buffer{BCB: 0x81, FCS: records.FCSNormal,
		Records: []records.Record{{RCB: 0x9A, SRCB: 0x00, Data: cmd.Encode()}}}).Encode()
	p.send(append(raw, 0x10, 0x26))
	h.waitFor(t, "command", func() bool { return len(h.nmrs) == 1 })
	if h.nmrs[0].Text() != "Q SYS" {
		t.Fatal("command text")
	}
	// Stream with request, a file, and EOF; we must get A0 and C0 back.
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x90, SRCB: 0xA9})
	b := p.readBuf()
	if b.Records[0].RCB != records.RCBPermit || b.Records[0].SRCB != 0xA9 {
		t.Fatalf("expected permit, got %s", b.Records[0].Describe())
	}
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0xA9, SRCB: 0xA0, Data: ebcdic.Encode("1ASA LINE")})
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0xA9, SRCB: 0x00})
	b = p.readBuf()
	if b.Records[0].RCB != records.RCBAckEOF || b.Records[0].SRCB != 0xA9 {
		t.Fatalf("expected EOF ack, got %s", b.Records[0].Describe())
	}
	h.waitFor(t, "dataset", func() bool { return len(h.datasets) == 1 })
	if r := h.datasets[0].Records[0]; r.CC != CCASA || string(ebcdic.Decode(r.Data)) != "1ASA LINE" {
		t.Fatalf("ASA record %+v", r)
	}
	// Duplicate request on an active stream is denied but harmless.
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x90, SRCB: 0xB9})
	p.readBuf()
	p.sendBuf(records.FCSNormal, records.Record{RCB: 0x90, SRCB: 0xB9})
	if b := p.readBuf(); b.Records[0].RCB != records.RCBDeny {
		t.Fatal("expected deny")
	}
	// Now go silent: the session must declare us dead after 3x keepalive.
	start := time.Now()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDead) {
			t.Fatalf("expected ErrDead, got %v", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("dead detection too slow")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not detect dead peer")
	}
}

func TestSecondaryBecomesPrimaryOnSYNNAK(t *testing.T) {
	_, p, _, errs := acceptRaw(t, time.Second)
	p.send(records.SYNNAK)
	if k := records.Classify(p.read()); k != records.KindSOHENQ {
		t.Fatalf("expected SOH ENQ, got %s", k)
	}
	p.send(records.DLEACK0)
	b := p.readBuf()
	so, err := records.ParseSignon(b.Records[0])
	if err != nil || so.SRCB != records.SRCBSignonI || so.Node != "GONJE" {
		t.Fatalf("expected I record: %+v %v", so, err)
	}
	j := &records.Signon{SRCB: records.SRCBSignonJ, Node: "RAW", BufSize: 4096, Event: records.CESPredefined}
	p.sendBuf(records.FCSNormal, j.Record())
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestSignonRejects(t *testing.T) {
	// Wrong first record.
	_, p, _, errs := acceptRaw(t, time.Second)
	p.send(records.DLEACK0)
	if err := <-errs; err == nil {
		t.Fatal("expected error for DLE ACK0 as first record")
	}
	// Wrong node name in I.
	_, p, _, errs = acceptRaw(t, time.Second)
	p.send(records.SOHENQ)
	p.read()
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "OTHER"}).Record())
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "OTHER") {
		t.Fatalf("expected wrong-node error, got %v", err)
	}
}

func TestPasswordCheck(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	h := newHandler()
	errs := make(chan error, 1)
	go func() {
		c, _ := ln.Accept()
		s := New(c, Params{Local: "GONJE", Remote: "RAW", Password: "SECRET", Handler: h, Handshake: 2 * time.Second})
		errs <- s.SignonSecondary()
	}()
	c, _ := net.Dial("tcp", ln.Addr().String())
	defer c.Close()
	p := &rawPeer{t: t, c: c, br: bufio.NewReader(c)}
	p.send(records.SOHENQ)
	p.read()
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", NodePW: "WRONG"}).Record())
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected password error, got %v", err)
	}
}

func TestDeniedFileAndWaitABit(t *testing.T) {
	s, p, h, errs := acceptRaw(t, 5*time.Second)
	p.send(records.SOHENQ)
	p.read()
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW"}).Record())
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	p.readBuf() // J
	go s.Run()
	defer s.Kill()
	// Wait-a-bit: gonje must not send data, only null buffers.
	p.sendBuf(records.FCS{0xCF, 0xCF})
	h.enqueue(&OutboundFile{Name: "x", ToNode: "RAW", Records: [][]byte{{0xC1}}})
	s.Kick()
	rec := p.read()
	if b, _ := records.Decode(rec); b == nil || !b.IsNull() {
		t.Fatal("expected null buffer while wait-a-bit is on")
	}
	// Release: the request (90) must come, which we deny.
	p.sendBuf(records.FCSNormal)
	b := p.readBuf()
	if b.Records[0].RCB != records.RCBRequest || b.Records[0].SRCB != 0x99 {
		t.Fatalf("expected request, got %s", b.Records[0].Describe())
	}
	// A refused request keeps the file queued (receiver drained) ...
	p.sendBuf(records.FCSNormal, records.Record{RCB: records.RCBDeny, SRCB: 0x99, Data: []byte{0x08, 0x00}})
	h.waitFor(t, "refusal event", func() bool {
		for _, e := range h.events {
			if e.Kind == EvStream && strings.Contains(e.Text, "stays queued") {
				return true
			}
		}
		return false
	})
	if len(h.done) != 0 || len(h.queue) != 1 {
		t.Fatal("refused file was dropped from the queue")
	}
	// ... and is not offered again until the receiver says D0 (ready).
	for i := 0; i < 3; i++ {
		p.sendBuf(records.FCSNormal)
		if b, _ := records.Decode(p.read()); b == nil || !b.IsNull() {
			t.Fatal("request repeated before D0")
		}
	}
	p.sendBuf(records.FCSNormal, records.Record{RCB: records.RCBReady, SRCB: 0x99})
	if b := p.readBuf(); b.Records[0].RCB != records.RCBRequest {
		t.Fatalf("no new request after D0: %s", b.Records[0].Describe())
	}
	// A B0 in the middle of a transfer still fails the file.
	p.sendBuf(records.FCSNormal, records.Record{RCB: records.RCBPermit, SRCB: 0x99})
	p.readBuf() // job header
	p.sendBuf(records.FCSNormal, records.Record{RCB: records.RCBDeny, SRCB: 0x99, Data: []byte{0x18, 0x10}})
	h.waitFor(t, "FileDone", func() bool { return len(h.done) == 1 })
	if h.done[0] == nil {
		t.Fatal("expected rejection error")
	}
}

func TestSpannedRecord(t *testing.T) {
	h := newHandler()
	s := New(nil, Params{Local: "A", Remote: "B", Handler: h})
	st := &rxStream{rcb: 0x99, s: s}
	s.rx[0x99] = st
	// LRECL 10, first segment SEGL 3 "ABC", last segment SEGL 4 "DE" (truncated).
	s.rxData(st, records.Record{RCB: 0x99, SRCB: 0x88, Data: append([]byte{3, 0, 10}, ebcdic.Encode("ABC")...)})
	s.rxData(st, records.Record{RCB: 0x99, SRCB: 0x84, Data: append([]byte{1}, ebcdic.Encode("X")...)})
	s.rxData(st, records.Record{RCB: 0x99, SRCB: 0x8C, Data: append([]byte{4}, ebcdic.Encode("DE")...)})
	if len(st.cur.Records) != 1 {
		t.Fatalf("records %d", len(st.cur.Records))
	}
	if got := ebcdic.Decode(st.cur.Records[0].Data); got != "ABCXDE    " {
		t.Fatalf("spanned record %q", got)
	}
}

func TestBCBCheck(t *testing.T) {
	s := New(nil, Params{Local: "A", Remote: "B", Handler: newHandler()})
	s.checkBCB(0x80)
	s.checkBCB(0x81)
	if s.inSeq != 2 {
		t.Fatal("sequence")
	}
	s.checkBCB(0x90) // bypass
	if s.inSeq != 2 {
		t.Fatal("bypass advanced")
	}
	s.checkBCB(0xA5) // reset to 5
	if s.inSeq != 6 {
		t.Fatalf("reset: %d", s.inSeq)
	}
	s.checkBCB(0x89) // out of sequence: resync
	if s.inSeq != 10 {
		t.Fatal("resync")
	}
	s.checkBCB(0x8F)
	s.checkBCB(0x80) // wrap
	if s.inSeq != 1 {
		t.Fatal("wrap")
	}
}

func TestClientOpenNAK(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, _ := ln.Accept()
		tcpip.ReadControl(c)
		c.Write((&tcpip.Control{Type: tcpip.TypeNAK, RHost: "B", OHost: "A", Reason: tcpip.NAKCollision}).Marshal())
		c.Close()
	}()
	c, _ := net.Dial("tcp", ln.Addr().String())
	defer c.Close()
	err := ClientOpen(c, Params{Local: "A", Remote: "B", Handler: newHandler()})
	var nak *NAKError
	if !errors.As(err, &nak) || nak.Reason != 3 {
		t.Fatalf("expected NAK 03, got %v", err)
	}
}

// TestCloseQuietSendsNoSignoff: gonje shutdown must not send the NJE
// signoff record. Live test against HOUCICS showed a correct NJE node
// refuses to sign on again after a signoff until its operator restarts
// the link, so a plain quit has to look like a lost connection to the
// peer (an I/O error there, never ErrPeerSignoff).
func TestCloseQuietSendsNoSignoff(t *testing.T) {
	a, b, _, _ := pair(t, 2*time.Second)
	ea, eb := runBoth(a, b)
	a.CloseQuiet("shutdown")
	if err := <-ea; !errors.Is(err, ErrLocalDrop) {
		t.Fatalf("A ended with %v", err)
	}
	err := <-eb
	if errors.Is(err, ErrPeerSignoff) {
		t.Fatal("B saw a signoff after CloseQuiet")
	}
	if !IsReconnectable(err) {
		t.Fatalf("B should treat a quiet close as reconnectable, got %v", err)
	}
}
