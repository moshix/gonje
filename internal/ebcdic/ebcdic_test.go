// Copyright 2026 by moshix

package ebcdic

import (
	"bytes"
	"testing"
)

func TestKnownVectors(t *testing.T) {
	cases := []struct {
		s string
		e []byte
	}{
		{"TEST MESSAGE", []byte{0xE3, 0xC5, 0xE2, 0xE3, 0x40, 0xD4, 0xC5, 0xE2, 0xE2, 0xC1, 0xC7, 0xC5}},
		{"OPEN", []byte{0xD6, 0xD7, 0xC5, 0xD5}},
		{"ACK", []byte{0xC1, 0xC3, 0xD2}},
		{"NAK", []byte{0xD5, 0xC1, 0xD2}},
		{"az09", []byte{0x81, 0xA9, 0xF0, 0xF9}},
		{"@#$", []byte{0x7C, 0x7B, 0x5B}},
	}
	for _, c := range cases {
		if got := Encode(c.s); !bytes.Equal(got, c.e) {
			t.Errorf("Encode(%q) = % X, want % X", c.s, got, c.e)
		}
		if got := Decode(c.e); got != c.s {
			t.Errorf("Decode(% X) = %q, want %q", c.e, got, c.s)
		}
	}
}

func TestRoundTripAllBytes(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	if got := EncodeBytes(DecodeBytes(all)); !bytes.Equal(got, all) {
		t.Fatal("EBCDIC->Latin1->EBCDIC not identity")
	}
	if got := DecodeBytes(EncodeBytes(all)); !bytes.Equal(got, all) {
		t.Fatal("Latin1->EBCDIC->Latin1 not identity")
	}
	if got := Encode(Decode(all)); !bytes.Equal(got, all) {
		t.Fatal("string round trip failed")
	}
}

func TestField(t *testing.T) {
	f := Field("abc", 8)
	want := []byte{0x81, 0x82, 0x83, 0x40, 0x40, 0x40, 0x40, 0x40}
	if !bytes.Equal(f, want) {
		t.Fatalf("Field = % X", f)
	}
	if got := Field("TOOLONGNAME", 8); len(got) != 8 || Decode(got) != "TOOLONGN" {
		t.Fatalf("truncation: %q", Decode(got))
	}
	if s := FieldString(f); s != "abc" {
		t.Fatalf("FieldString = %q", s)
	}
	if s := FieldString(make([]byte, 8)); s != "" {
		t.Fatalf("zeros FieldString = %q", s)
	}
}

func TestHexDump(t *testing.T) {
	data := append(Encode("TEST MESSAGE"), 0, 0, 0, 0)
	lines := HexDump(data)
	want := "0000  E3 C5 E2 E3 40 D4 C5 E2  E2 C1 C7 C5 00 00 00 00  TEST MESSAGE...."
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("got %q\nwant %q", lines, want)
	}
	if len(want) != DumpWidth {
		t.Fatalf("DumpWidth %d != %d", DumpWidth, len(want))
	}
	// Partial line keeps alignment.
	lines = HexDump(append(data, Encode("AB")...))
	if len(lines) != 2 {
		t.Fatalf("lines=%d", len(lines))
	}
	if lines[1] != "0010  C1 C2                                             AB" {
		t.Fatalf("partial line %q", lines[1])
	}
	for _, l := range lines {
		if len(l) > DumpWidth {
			t.Fatalf("line too long: %d", len(l))
		}
	}
	if HexDump(nil) != nil {
		t.Fatal("empty dump should be nil")
	}
}

func TestHexDumpMasked(t *testing.T) {
	data := Encode("USERPASSWORD")
	mask := make([]bool, len(data))
	for i := 4; i < 12; i++ {
		mask[i] = true
	}
	got := HexDumpMasked(data, mask)
	want := "0000  E4 E2 C5 D9 XX XX XX XX  XX XX XX XX              USER********"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if HexDumpMasked(data, mask[:2])[0] != HexDump(data)[0] {
		t.Fatal("short mask must only hide its own bytes")
	}
}
