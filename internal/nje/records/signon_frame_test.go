// Copyright 2026 by moshix

package records

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// unhex decodes a hex string with blanks.
func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// From the Sine Nomine NJE / z/VM RSCS (ZVM72MSH) trace of 2026-11-09:
// both TTR records are 46 bytes, no RCB 00 after the F0 record.
const (
	snaI = "10 02 80 8F CF F0 C9 29 D9C5D3C1E8404040 01 00000000 0000 2000" +
		" 40404040404040404040404040404040 00 15 00 00 00"
	rscsJ = "10 02 80 8F CF F0 D1 29 E9E5D4F7F2D4E2C8 01 FFFFFFFF 0000 1000" +
		" 40404040404040404040404040404040 00 14 00 00 00"
	snaKeepalive = "10 02 81 8F CF 00"
)

// TestSignonBufferHasNoEOB: our I and J encode to exactly the bytes
// RSCS accepts (golden from the trace, our encoder, same node name).
func TestSignonBufferHasNoEOB(t *testing.T) {
	i := &Signon{SRCB: SRCBSignonI, Node: "RELAY", Qual: 1, BufSize: 8192, Features: [4]byte{0x15}}
	got := (&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{i.Record()}}).Encode()
	if want := unhex(t, snaI); !bytes.Equal(got, want) || len(got) != 46 {
		t.Fatalf("I\n got % X\nwant % X", got, want)
	}
	j := &Signon{SRCB: SRCBSignonJ, Node: "ZVM72MSH", Qual: 1, BufSize: 4096, Event: CESPredefined, Features: [4]byte{0x14}}
	got = (&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{j.Record()}}).Encode()
	if want := unhex(t, rscsJ); !bytes.Equal(got, want) {
		t.Fatalf("J\n got % X\nwant % X", got, want)
	}
	// A signoff (B) is an F0 record alone too: no RCB 00 either.
	b := (&Buffer{BCB: 0x82, FCS: FCSNormal, Records: []Record{{RCB: RCBControl, SRCB: 0xC2}}}).Encode()
	if !bytes.Equal(b, unhex(t, "10 02 82 8F CF F0 C2")) {
		t.Fatalf("B % X", b)
	}
	// Data and null buffers keep their end-of-buffer RCB 00.
	if k := (&Buffer{BCB: 0x81, FCS: FCSNormal}).Encode(); !bytes.Equal(k, unhex(t, snaKeepalive)) {
		t.Fatalf("null buffer % X", k)
	}
	nmr := (&Buffer{BCB: 0x81, FCS: FCSNormal, Records: []Record{{RCB: RCBNMR, SRCB: 0x80, Data: []byte{1, 2, 3}}}}).Encode()
	if nmr[len(nmr)-1] != RCBEOB || nmr[len(nmr)-2] != 0x00 {
		t.Fatalf("data buffer % X", nmr)
	}
}

// TestDecodeSignonWithAndWithoutEOB: both forms decode to the same I/J.
func TestDecodeSignonWithAndWithoutEOB(t *testing.T) {
	for _, raw := range []string{snaI, snaI + " 00", rscsJ, rscsJ + " 00", strings.TrimPrefix(rscsJ, "10 02 ")} {
		b, err := Decode(unhex(t, raw))
		if err != nil || len(b.Records) != 1 {
			t.Fatalf("%s: %v %+v", raw, err, b)
		}
		so, err := ParseSignon(b.Records[0])
		if err != nil || (so.Node != "RELAY" && so.Node != "ZVM72MSH") {
			t.Fatalf("%s: %+v %v", raw, so, err)
		}
		if so.Node == "ZVM72MSH" && (so.BufSize != 4096 || so.Features[0] != 0x14) {
			t.Fatalf("J %+v", so)
		}
	}
	for _, raw := range []string{"10 02 82 8F CF F0 C2", "10 02 82 8F CF F0 C2 00"} {
		if b, err := Decode(unhex(t, raw)); err != nil || len(b.Records) != 1 || b.Records[0].SRCB != 0xC2 {
			t.Fatalf("signoff %s: %+v %v", raw, b, err)
		}
	}
	if b, err := Decode(unhex(t, snaKeepalive)); err != nil || !b.IsNull() || b.BCB != 0x81 {
		t.Fatalf("keepalive %+v %v", b, err)
	}
}
