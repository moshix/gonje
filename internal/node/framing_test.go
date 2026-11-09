// Copyright 2026 by moshix

package node

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
	"gonje/internal/nje/tcpip"
)

// framingNode is node ME with one remote PEER at addr:port (port 0 =
// passive), its state file in a temp dir.
func framingNode(t *testing.T, statePath string, port int) (*Node, <-chan link.Event, *syncBuffer) {
	t.Helper()
	cfg := config.Defaults()
	cfg.LocalNode, cfg.LocalIP, cfg.LocalPort = "ME", "127.0.0.1", 0
	cfg.RecvDir, cfg.SendDir = t.TempDir(), t.TempDir()
	r := &config.Remote{Name: "PEER", Keepalive: 5, Port: 175}
	if port != 0 {
		r.Addr, r.Port = "127.0.0.1", port
	}
	cfg.Remotes = []*config.Remote{r}
	logBuf := &syncBuffer{}
	n := New(cfg, Options{RetryMin: 100 * time.Millisecond, RetryMax: 300 * time.Millisecond,
		Handshake: 3 * time.Second, DialHandshake: 2 * time.Second, StartJitter: 10 * time.Millisecond,
		StreamSettle: 10 * time.Millisecond, User: "OP", StatePath: statePath, RejectRetry: 200 * time.Millisecond,
		Log: logx.New(logBuf, logx.Debug)})
	if err := n.Listen(); err != nil {
		t.Fatal(err)
	}
	_, ev := n.Bus().Subscribe(10000)
	n.Start()
	t.Cleanup(n.Stop)
	return n, ev, logBuf
}

// fakePeer plays the remote node PEER on one connection after OPEN/ACK.
// primary: it sends SOH ENQ and its I (in its own form) and expects our
// J; otherwise (synNak) it sends SYN NAK, or (we dialled) it answers our
// SOH ENQ, and expects our I. accept says whether our buffer's form is
// acceptable; a refused one gets the connection reset, like z/VM RSCS
// (DMTTNE945E code 1818). It returns whether the signon completed.
type fakePeer struct {
	dleSTX  bool // the form it sends
	accept  func(dleSTX bool) bool
	synNak  bool
	primary bool
	// exact: like z/VM RSCS, reject a signon buffer that does not end
	// right after the F0 record (5 + NCCIDL bytes: no trailing RCB 00).
	exact   bool
	gotForm atomic.Value // bool: form of our first buffer
	gotLen  atomic.Int32 // length of our first buffer's TTR record
}

func (f *fakePeer) signon(t *testing.T, c net.Conn) bool {
	br := bufio.NewReader(c)
	block := func(rec []byte) { tcpip.WriteBlock(c, rec) }
	readRec := func() []byte {
		recs, _, err := tcpip.ReadBlock(br)
		if err != nil || len(recs) == 0 {
			return nil
		}
		return recs[0]
	}
	buf := func(srcb byte) []byte {
		b := &records.Buffer{BCB: 0x80, FCS: records.FCSNormal,
			Records: []records.Record{(&records.Signon{SRCB: srcb, Node: "PEER", BufSize: 1012}).Record()}}
		return b.EncodeFramed(f.dleSTX)
	}
	switch {
	case f.primary:
		block(records.SOHENQ)
		readRec() // DLE ACK0
		block(buf(records.SRCBSignonI))
	case f.synNak:
		block(records.SYNNAK)
		readRec() // our SOH ENQ
		block(records.DLEACK0)
	default: // we were dialled: answer SOH ENQ
		readRec()
		block(records.DLEACK0)
	}
	rec := readRec()
	if rec == nil {
		return false
	}
	form := len(rec) >= 2 && rec[0] == 0x10 && rec[1] == 0x02
	f.gotForm.Store(form)
	f.gotLen.Store(int32(len(rec)))
	if !f.accept(form) || (f.exact && len(rec) != 5+records.SignonLen) {
		c.Close() // reset, as RSCS does
		return false
	}
	if !f.primary {
		block(buf(records.SRCBSignonJ))
	}
	go func() { // keep the session alive: read and answer null buffers
		for {
			if _, _, err := tcpip.ReadBlock(br); err != nil {
				return
			}
			block((&records.Buffer{BCB: 0x80, FCS: records.FCSNormal}).EncodeFramed(f.dleSTX))
		}
	}()
	return true
}

// dialUs connects to n as PEER and exchanges OPEN/ACK; rip is the RIP
// of our OPEN.
func dialUs(t *testing.T, n *Node, rip net.IP) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", n.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.Write((&tcpip.Control{Type: tcpip.TypeOpen, RHost: "PEER", RIP: rip, OHost: "ME", OIP: net.IPv4(127, 0, 0, 1)}).Marshal())
	if r, _, err := tcpip.ReadControl(c); err != nil || r.Type != tcpip.TypeACK {
		t.Fatalf("no ACK: %v %v", r, err)
	}
	return c
}

func stateOf(t *testing.T, path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// TestFramingMirrorHercules: a Hercules-style peer (SOH ENQ, I with
// 10 02) gets our J with DLE STX, even when we had learned "none".
func TestFramingMirrorHercules(t *testing.T) {
	state := filepath.Join(t.TempDir(), StateFile)
	os.WriteFile(state, []byte("PEER.dlestx=no confirmed=2026-11-09T20:00:00Z\n"), 0o644)
	n, ev, _ := framingNode(t, state, 0)
	f := &fakePeer{dleSTX: true, primary: true, accept: func(dle bool) bool { return dle }}
	if !f.signon(t, dialUs(t, n, net.IPv4(127, 0, 0, 1))) {
		t.Fatalf("J without DLE STX: %v", f.gotForm.Load())
	}
	(&testNode{Node: n, events: ev}).wait(t, "mirrored", 5*time.Second, kindText(link.EvLink, "framing: DLE STX (mirrored peer, was none)"))
	waitStateFile(t, state, "PEER.dlestx=yes")
	if c := n.Counters().Links[0].Framing; c != "DLE STX (mirrored)" {
		t.Fatalf("stats framing %q", c)
	}
}

// TestFramingRSCSSignon: a z/VM RSCS-style caller sends SYN NAK, wants
// DLE STX and resets on a trailing RCB 00 after the F0 record (the real
// cause of DMTTNE945E code 1818). Our blind I is DLE STX, 46 bytes, and
// the signon completes at once, also with RIP 0.0.0.0 in the OPEN.
func TestFramingRSCSSignon(t *testing.T) {
	for _, rip := range []net.IP{net.IPv4(127, 0, 0, 1), net.IPv4zero} {
		state := filepath.Join(t.TempDir(), StateFile)
		n, _, logBuf := framingNode(t, state, 0)
		f := &fakePeer{dleSTX: true, synNak: true, exact: true, accept: func(dle bool) bool { return dle }}
		if !f.signon(t, dialUs(t, n, rip)) {
			t.Fatalf("rip %v: I rejected: DLE STX %v, %d bytes", rip, f.gotForm.Load(), f.gotLen.Load())
		}
		if f.gotLen.Load() != 46 {
			t.Fatalf("I is %d bytes, want 46", f.gotLen.Load())
		}
		waitStateFile(t, state, "PEER.dlestx=yes")
		if !strings.Contains(logBuf.String(), "framing: DLE STX (guessed: default)") {
			t.Fatal("not the default form")
		}
	}
}

// TestFramingBounded: a peer that rejects both forms gets DLE STX, then
// none once, then DLE STX for good, at least RejectRetry apart.
func TestFramingBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var forms []bool
	var times []time.Time
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				open, _, err := tcpip.ReadControl(c)
				if err != nil {
					c.Close()
					return
				}
				c.Write((&tcpip.Control{Type: tcpip.TypeACK, RHost: "PEER", OHost: open.RHost}).Marshal())
				f := &fakePeer{accept: func(bool) bool { return false }}
				f.signon(t, c)
				if v, ok := f.gotForm.Load().(bool); ok {
					mu.Lock()
					forms, times = append(forms, v), append(times, time.Now())
					mu.Unlock()
				}
			}()
		}
	}()
	framingNode(t, filepath.Join(t.TempDir(), StateFile), ln.Addr().(*net.TCPAddr).Port)
	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(forms) < 3 || len(forms) > 8 {
		t.Fatalf("%d attempts in 1.5 s", len(forms))
	}
	want := []bool{true, false, true}
	for i, f := range forms {
		w := true
		if i < len(want) {
			w = want[i]
		}
		if f != w {
			t.Fatalf("attempt %d form %v (all %v)", i+1, f, forms)
		}
		if i > 0 && times[i].Sub(times[i-1]) < 150*time.Millisecond {
			t.Fatalf("attempts %d and %d only %s apart", i, i+1, times[i].Sub(times[i-1]))
		}
	}
}

// peerServer listens as PEER: OPEN/ACK, then it answers our SOH ENQ
// and accepts only buffers in its own form (dleSTX); it resets on the
// other one. It counts rejected and completed attempts.
func peerServer(t *testing.T, dleSTX bool) (port int, rejects *atomic.Int32, ok *atomic.Int32) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	rejects, ok = &atomic.Int32{}, &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				open, _, err := tcpip.ReadControl(c)
				if err != nil {
					c.Close()
					return
				}
				c.Write((&tcpip.Control{Type: tcpip.TypeACK, RHost: "PEER", OHost: open.RHost}).Marshal())
				f := &fakePeer{dleSTX: dleSTX, accept: func(dle bool) bool { return dle == dleSTX }}
				if f.signon(t, c) {
					ok.Add(1)
				} else {
					rejects.Add(1)
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, rejects, ok
}

func waitStateFile(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(stateOf(t, path), want) {
		if time.Now().After(deadline) {
			t.Fatalf("state file lacks %q:\n%s", want, stateOf(t, path))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitCount(t *testing.T, c *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for c.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("count %d, want %d", c.Load(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestFramingFallbackAndRestart: we dial an RSCS-style peer without any
// hint: the DLE STX I is reset, the next attempt without succeeds and is
// stored; a restart uses the stored form at once.
func TestFramingFallbackAndRestart(t *testing.T) {
	port, rejects, ok := peerServer(t, false) // z/VM RSCS style
	state := filepath.Join(t.TempDir(), StateFile)
	n, _, logBuf := framingNode(t, state, port)
	waitCount(t, ok, 1)
	if rejects.Load() != 1 {
		t.Fatalf("%d rejected attempts, want 1", rejects.Load())
	}
	waitStateFile(t, state, "PEER.dlestx=no confirmed=")
	for _, want := range []string{"framing: DLE STX (guessed: default)", "framing: DLE STX rejected, next attempt uses none",
		"framing: none (guessed: the other form was rejected)", "framing: none confirmed"} {
		if !strings.Contains(logBuf.String(), want) {
			t.Fatalf("log lacks %q", want)
		}
	}
	n.Stop()
	// Restart: learned form first, no rejection.
	_, _, logBuf2 := framingNode(t, state, port)
	waitCount(t, ok, 2)
	if rejects.Load() != 1 || !strings.Contains(logBuf2.String(), "framing: none (learned: confirmed earlier)") {
		t.Fatalf("restart: %d rejects\n%s", rejects.Load(), logBuf2.String())
	}
}

// TestFramingRelearn: a remembered form that fails falls back and the
// new form is stored; link reset-framing forgets it.
func TestFramingRelearn(t *testing.T) {
	port, rejects, ok := peerServer(t, true) // the peer now wants DLE STX (Hercules style)
	state := filepath.Join(t.TempDir(), StateFile)
	os.WriteFile(state, []byte("PEER.dlestx=no confirmed=2026-11-09T20:00:00Z\nOTHER.dlestx=yes confirmed=2026-11-01T00:00:00Z\n"), 0o644)
	n, _, _ := framingNode(t, state, port)
	waitCount(t, ok, 1)
	if rejects.Load() != 1 {
		t.Fatalf("%d rejects", rejects.Load())
	}
	waitStateFile(t, state, "PEER.dlestx=yes")
	if !strings.Contains(stateOf(t, state), "OTHER.dlestx=yes confirmed=2026-11-01T00:00:00Z") {
		t.Fatal("entry of another node lost")
	}
	if err := n.ResetFraming("peer"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stateOf(t, state), "PEER.") {
		t.Fatalf("not forgotten:\n%s", stateOf(t, state))
	}
}

// TestFramingNoStateWithoutNeed: no gonje.state appears before anything
// was learned; an unreadable one is ignored.
func TestFramingNoStateWithoutNeed(t *testing.T) {
	state := filepath.Join(t.TempDir(), StateFile)
	n, _, _ := framingNode(t, state, 0)
	n.saveFraming("")
	if _, err := os.Stat(state); err == nil {
		t.Fatal("empty state file created")
	}
	os.WriteFile(state, []byte("garbage\x00\nPEER.dlestx\n"), 0o644)
	if st := readFramingState(state); len(st) != 1 {
		t.Fatalf("%+v", st) // "PEER.dlestx" without value: dlestx=no
	}
	if st := readFramingState(filepath.Join(t.TempDir(), "none")); len(st) != 0 {
		t.Fatal("missing file not empty")
	}
}

// TestFramingByteCompat: without DLE STX a buffer is the same bytes
// minus the leading 10 02.
func TestFramingByteCompat(t *testing.T) {
	b := &records.Buffer{BCB: 0x80, FCS: records.FCSNormal,
		Records: []records.Record{(&records.Signon{SRCB: records.SRCBSignonI, Node: "3270BBS", Qual: 1, BufSize: 8192}).Record()}}
	with, without := b.EncodeFramed(true), b.EncodeFramed(false)
	if !bytes.Equal(with, b.Encode()) || !bytes.HasPrefix(with, []byte{0x10, 0x02, 0x80, 0x8F, 0xCF, 0xF0, 0xC9}) {
		t.Fatalf("% X", with)
	}
	if !bytes.Equal(without, with[2:]) {
		t.Fatalf("% X", without)
	}
}
