package tcpip

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"testing/iotest"
	"time"
)

func TestControlRoundTrip(t *testing.T) {
	c := &Control{Type: TypeOpen, RHost: "NODEA", RIP: net.IPv4(128, 112, 14, 1),
		OHost: "NODEB", OIP: net.IPv4(10, 0, 0, 2)}
	b := c.Marshal()
	if len(b) != 33 {
		t.Fatalf("len %d", len(b))
	}
	want := []byte{0xD6, 0xD7, 0xC5, 0xD5, 0x40, 0x40, 0x40, 0x40, // OPEN
		0xD5, 0xD6, 0xC4, 0xC5, 0xC1, 0x40, 0x40, 0x40, // NODEA
		0x80, 0x70, 0x0E, 0x01,
		0xD5, 0xD6, 0xC4, 0xC5, 0xC2, 0x40, 0x40, 0x40, // NODEB
		0x0A, 0x00, 0x00, 0x02, 0x00}
	if !bytes.Equal(b, want) {
		t.Fatalf("got % X", b)
	}
	got, raw, err := ReadControl(iotest.OneByteReader(bytes.NewReader(b)))
	if err != nil || !bytes.Equal(raw, b) {
		t.Fatal(err)
	}
	if got.Type != TypeOpen || got.RHost != "NODEA" || got.OHost != "NODEB" ||
		!got.RIP.Equal(net.IPv4(128, 112, 14, 1)) || !got.OIP.Equal(net.IPv4(10, 0, 0, 2)) {
		t.Fatalf("decoded %+v", got)
	}
}

func TestACKNAK(t *testing.T) {
	ack := (&Control{Type: TypeACK, RHost: "B", OHost: "A"}).Marshal()
	if !bytes.Equal(ack[:8], []byte{0xC1, 0xC3, 0xD2, 0x40, 0x40, 0x40, 0x40, 0x40}) {
		t.Fatalf("ACK type % X", ack[:8])
	}
	nak := (&Control{Type: TypeNAK, RHost: "B", OHost: "A", Reason: NAKCollision}).Marshal()
	if !bytes.Equal(nak[:8], []byte{0xD5, 0xC1, 0xD2, 0x40, 0x40, 0x40, 0x40, 0x40}) || nak[32] != 3 {
		t.Fatalf("NAK % X", nak)
	}
	c, err := UnmarshalControl(nak)
	if err != nil || c.Type != TypeNAK || c.Reason != 3 {
		t.Fatalf("%+v %v", c, err)
	}
	if NAKRetryable(NAKNoSuchLink) || !NAKRetryable(NAKActive) || !NAKRetryable(NAKTemporary) {
		t.Fatal("retryable")
	}
	if c.String() == "" || NAKReasonText(9) != "unknown reason" {
		t.Fatal("text")
	}
}

func TestBadControl(t *testing.T) {
	b := (&Control{Type: "OPEN SSL"}).Marshal()
	if _, err := UnmarshalControl(b); !errors.Is(err, ErrBadControl) {
		t.Fatalf("expected ErrBadControl, got %v", err)
	}
	if _, err := UnmarshalControl(b[:10]); !errors.Is(err, ErrBadControl) {
		t.Fatal("short")
	}
	if _, _, err := ReadControl(bytes.NewReader(b[:10])); err == nil {
		t.Fatal("short read should fail")
	}
}

func TestBlockEncode(t *testing.T) {
	b, err := EncodeBlock([]byte{0x01, 0x2D})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 0, 18, 0, 0, 0, 0, 0, 0, 0, 2, 0x01, 0x2D, 0, 0, 0, 0}
	if !bytes.Equal(b, want) {
		t.Fatalf("got % X", b)
	}
	if _, err := EncodeBlock([]byte{}); err == nil {
		t.Fatal("empty record should fail")
	}
	if _, err := EncodeBlock(make([]byte, 70000)); err == nil {
		t.Fatal("huge record should fail")
	}
}

func TestBlockRoundTripShortReads(t *testing.T) {
	var stream bytes.Buffer
	b1, _ := EncodeBlock([]byte{0x10, 0x70})
	b2, _ := EncodeBlock([]byte{0x10, 0x02, 0x80, 0x8F, 0xCF, 0x00}, bytes.Repeat([]byte{7}, 300))
	stream.Write(b1)
	stream.Write(b2)
	br := bufio.NewReader(iotest.OneByteReader(&stream))
	recs, raw, err := ReadBlock(br)
	if err != nil || len(recs) != 1 || !bytes.Equal(recs[0], []byte{0x10, 0x70}) || !bytes.Equal(raw, b1) {
		t.Fatalf("block1 %v % X", err, recs)
	}
	recs, raw, err = ReadBlock(br)
	if err != nil || len(recs) != 2 || len(recs[1]) != 300 || !bytes.Equal(raw, b2) {
		t.Fatalf("block2 %v", err)
	}
	if _, _, err = ReadBlock(br); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestBlockTolerance(t *testing.T) {
	// TTB.LN excludes the EOB TTR (sender bug): EOB is peeked and consumed.
	blk := []byte{0, 0, 0, 14, 0, 0, 0, 0, 0, 0, 0, 2, 0x10, 0x70, 0, 0, 0, 0}
	next, _ := EncodeBlock([]byte{0x01, 0x2D})
	br := bufio.NewReader(bytes.NewReader(append(blk, next...)))
	recs, _, err := ReadBlock(br)
	if err != nil || len(recs) != 1 {
		t.Fatal(err)
	}
	recs, _, err = ReadBlock(br)
	if err != nil || len(recs) != 1 || recs[0][0] != 0x01 {
		t.Fatalf("next block misframed: %v % X", err, recs)
	}
	// Empty block (only EOB TTR).
	br = bufio.NewReader(bytes.NewReader([]byte{0, 0, 0, 12, 0, 0, 0, 0, 0, 0, 0, 0}))
	recs, _, err = ReadBlock(br)
	if err != nil || len(recs) != 0 {
		t.Fatal("empty block")
	}
	// TTB.LN too small.
	br = bufio.NewReader(bytes.NewReader([]byte{0, 0, 0, 4, 0, 0, 0, 0}))
	if _, _, err = ReadBlock(br); err == nil {
		t.Fatal("expected error")
	}
	// Truncated record -> unexpected EOF.
	br = bufio.NewReader(bytes.NewReader([]byte{0, 0, 0, 20, 0, 0, 0, 0, 0, 0, 0, 8, 1, 2}))
	if _, _, err = ReadBlock(br); err == nil {
		t.Fatal("expected truncation error")
	}
}

// TestBlockNoPeekWait: a block whose TTB.LN is used up must be returned
// at once, without waiting for more input (review R1).
func TestBlockNoPeekWait(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	br := bufio.NewReader(pr)
	done := make(chan error, 1)
	go func() {
		_, _, err := ReadBlock(br)
		done <- err
	}()
	go pw.Write([]byte{0, 0, 0, 8, 0, 0, 0, 0}) // LN=8: empty, no EOB TTR
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadBlock blocked waiting for the next block")
	}
}
