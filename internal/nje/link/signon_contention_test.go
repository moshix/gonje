// Copyright 2026 by moshix

// Tests of the connecting side's answer to SYN NAK and to SOH ENQ
// (contention) after its own SOH ENQ.

package link

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"gonje/internal/nje/records"
)

// dialRaw runs SignonPrimary (the dialling side, OPEN/ACK skipped) against
// a hand-driven peer on loopback. The session's handshake timeout is short
// so a wrong turn fails the test instead of hanging it.
func dialRaw(t *testing.T) (*Session, *rawPeer, chan error) {
	t.Helper()
	return rawSignon(t, (*Session).SignonPrimary)
}

// acceptSignonRaw is dialRaw for the accepting side (SignonSecondary).
func acceptSignonRaw(t *testing.T) (*Session, *rawPeer, chan error) {
	t.Helper()
	return rawSignon(t, (*Session).SignonSecondary)
}

// rawSignon runs one signon half (signon) on a session wired to a
// hand-driven peer on loopback.
func rawSignon(t *testing.T, signon func(*Session) error) (*Session, *rawPeer, chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	pc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); pc.Close() })
	s := New(c, Params{Local: "GONJE", Remote: "RAW", Keepalive: time.Second, Handler: newHandler(),
		Handshake: 2 * time.Second, StreamSettle: time.Millisecond})
	errs := make(chan error, 1)
	go func() { errs <- signon(s) }()
	return s, &rawPeer{t: t, c: pc, br: bufio.NewReader(pc)}, errs
}

// expectLine reads one record from the session and checks its kind.
func (p *rawPeer) expectLine(want records.LineKind) {
	p.t.Helper()
	if k := records.Classify(p.read()); k != want {
		p.t.Fatalf("expected %s, got %s", want, k)
	}
}

// waitErr returns the signon result or fails after a timeout (no hang).
func waitErr(t *testing.T, errs chan error) error {
	t.Helper()
	select {
	case err := <-errs:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("signon hung")
		return nil
	}
}

// expectI reads the session's I record.
func (p *rawPeer) expectI() {
	p.t.Helper()
	so, err := records.ParseSignon(p.readBuf().Records[0])
	if err != nil || so.SRCB != records.SRCBSignonI || so.Node != "GONJE" {
		p.t.Fatalf("expected I record: %+v %v", so, err)
	}
}

func (p *rawPeer) sendJ() {
	j := &records.Signon{SRCB: records.SRCBSignonJ, Node: "RAW", BufSize: 4096, Event: records.CESPredefined}
	p.sendBuf(records.FCSNormal, j.Record())
}

// The peer leaves the primary role to us: SYN NAK, then DLE ACK0 to our
// repeated SOH ENQ. We sign on as primary.
func TestDialSYNNAKThenPrimary(t *testing.T) {
	s, p, errs := dialRaw(t)
	p.expectLine(records.KindSOHENQ)
	p.send(records.SYNNAK)
	p.expectLine(records.KindSOHENQ)
	p.send(records.DLEACK0)
	p.expectI()
	p.sendJ()
	if err := waitErr(t, errs); err != nil {
		t.Fatal(err)
	}
	if !s.Stats().Primary {
		t.Fatal("expected primary role")
	}
}

// A peer that answers SYN NAK forever: bounded, with a clear error.
func TestDialSYNNAKForeverBounded(t *testing.T) {
	_, p, errs := dialRaw(t)
	bids := 0
	for {
		p.expectLine(records.KindSOHENQ)
		bids++
		p.send(records.SYNNAK)
		if bids == maxSYNNAKRetries+1 {
			break
		}
	}
	err := waitErr(t, errs)
	if err == nil || !strings.Contains(err.Error(), "SYN NAK") {
		t.Fatalf("expected SYN NAK error, got %v", err)
	}
	if bids != maxSYNNAKRetries+1 {
		t.Fatalf("%d bids", bids)
	}
	p.assertSilent(100 * time.Millisecond) // no further SOH ENQ
}

// Contention: the peer bids too. We yield (DLE ACK0), get its I, send J
// and are secondary.
func TestDialContentionWeYield(t *testing.T) {
	s, p, errs := dialRaw(t)
	p.expectLine(records.KindSOHENQ)
	p.send(records.SOHENQ)
	p.expectLine(records.KindDLEACK0)
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", BufSize: 1024}).Record())
	if err := waitErr(t, errs); err != nil {
		t.Fatal(err)
	}
	so, err := records.ParseSignon(p.readBuf().Records[0])
	if err != nil || so.SRCB != records.SRCBSignonJ || so.Node != "GONJE" {
		t.Fatalf("expected J: %+v %v", so, err)
	}
	if s.Stats().Primary {
		t.Fatal("expected secondary role")
	}
	if s.bufLimit != 1024-bufMargin {
		t.Fatalf("buffer limit %d", s.bufLimit)
	}
	if s.FramingRejected() {
		t.Fatal("secondary path must not report the framing rejected")
	}
}

// Double yield: the peer bids, then also yields (DLE ACK0 after ours).
// We take the primary role once, send I, and finish on its J.
func TestDialContentionDoubleYield(t *testing.T) {
	s, p, errs := dialRaw(t)
	p.expectLine(records.KindSOHENQ)
	p.send(records.SOHENQ)
	p.expectLine(records.KindDLEACK0)
	p.send(records.DLEACK0)
	p.expectI()
	p.sendJ()
	if err := waitErr(t, errs); err != nil {
		t.Fatal(err)
	}
	if !s.Stats().Primary {
		t.Fatal("expected primary role")
	}
}

// Double yield where the peer then sends its own I instead of J: the
// attempt ends with an error (once, no loop).
func TestDialContentionDoubleYieldNoLoop(t *testing.T) {
	_, p, errs := dialRaw(t)
	p.expectLine(records.KindSOHENQ)
	p.send(records.SOHENQ)
	p.expectLine(records.KindDLEACK0)
	p.send(records.DLEACK0)
	p.expectI()
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", BufSize: 1024}).Record())
	if err := waitErr(t, errs); err == nil {
		t.Fatal("expected an error for I instead of J")
	}
}

// Other answers keep the old error text.
func TestDialUnexpectedReplyKeepsError(t *testing.T) {
	_, p, errs := dialRaw(t)
	p.expectLine(records.KindSOHENQ)
	p.send([]byte{records.NAK})
	err := waitErr(t, errs)
	if err == nil || !strings.HasPrefix(err.Error(), "expected DLE ACK0 after SOH ENQ, got") {
		t.Fatalf("got %v", err)
	}
}

// Padded line control end to end: the peer answers our SOH ENQ with DLE
// ACK0 plus a BSC pad byte (10 70 FF, as PUBNET does). We sign on as
// primary.
func TestDialPaddedDLEACK0(t *testing.T) {
	s, p, errs := dialRaw(t)
	p.expectLine(records.KindSOHENQ)
	p.send([]byte{records.DLE, records.ACK0, 0xFF})
	p.expectI()
	p.sendJ()
	if err := waitErr(t, errs); err != nil {
		t.Fatal(err)
	}
	if !s.Stats().Primary {
		t.Fatal("expected primary role")
	}
}

// Accepting side: the dialler sends SYN NAK, we bid, and it bids too
// (SOH ENQ). Our SOH ENQ reached it first, so we yield and finish as
// secondary, as the dialling side does.
func TestAcceptSYNNAKThenContention(t *testing.T) {
	s, p, errs := acceptSignonRaw(t)
	p.send(records.SYNNAK)
	p.expectLine(records.KindSOHENQ)
	p.send(records.SOHENQ)
	p.expectLine(records.KindDLEACK0)
	p.sendBuf(records.FCSNormal, (&records.Signon{SRCB: records.SRCBSignonI, Node: "RAW", BufSize: 1024}).Record())
	if err := waitErr(t, errs); err != nil {
		t.Fatal(err)
	}
	so, err := records.ParseSignon(p.readBuf().Records[0])
	if err != nil || so.SRCB != records.SRCBSignonJ {
		t.Fatalf("expected J: %+v %v", so, err)
	}
	if s.Stats().Primary {
		t.Fatal("expected secondary role")
	}
}

// Accepting side: SYN NAK, our bid, a padded DLE ACK0. We are primary.
func TestAcceptSYNNAKPaddedDLEACK0(t *testing.T) {
	s, p, errs := acceptSignonRaw(t)
	p.send(records.SYNNAK)
	p.expectLine(records.KindSOHENQ)
	p.send([]byte{records.DLE, records.ACK0, 0xFF})
	p.expectI()
	p.sendJ()
	if err := waitErr(t, errs); err != nil {
		t.Fatal(err)
	}
	if !s.Stats().Primary {
		t.Fatal("expected primary role")
	}
}
