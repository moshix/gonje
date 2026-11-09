// Copyright 2026 by moshix

package scb

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestKnownVectors(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"empty", nil, []byte{0x00}},
		{"literal", []byte{0xC1, 0xC2}, []byte{0xC2, 0xC1, 0xC2, 0x00}},
		{"blanks", []byte{0x40, 0x40, 0x40, 0x40, 0x40}, []byte{0x85, 0x00}},
		{"single blank literal", []byte{0xC1, 0x40, 0xC2}, []byte{0xC3, 0xC1, 0x40, 0xC2, 0x00}},
		{"dup", []byte{0xF0, 0xF0, 0xF0, 0xF0}, []byte{0xA4, 0xF0, 0x00}},
		{"two same literal", []byte{0xF0, 0xF0}, []byte{0xC2, 0xF0, 0xF0, 0x00}},
		{"mixed", []byte{0xC1, 0x40, 0x40, 0x40, 0xC2, 0xC2, 0xC2},
			[]byte{0xC1, 0xC1, 0x83, 0xA3, 0xC2, 0x00}},
	}
	for _, c := range cases {
		got := Compress(c.in)
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s: Compress = % X want % X", c.name, got, c.want)
		}
		dec, n, ab, err := Decompress(got)
		if err != nil || ab || n != len(got) || !bytes.Equal(dec, c.in) {
			t.Errorf("%s: Decompress = % X n=%d ab=%v err=%v", c.name, dec, n, ab, err)
		}
	}
}

func TestLongRuns(t *testing.T) {
	// 40 blanks -> 31 + 9; 100 literal bytes -> 63 + 37.
	rec := bytes.Repeat([]byte{0x40}, 40)
	got := Compress(rec)
	if !bytes.Equal(got, []byte{0x9F, 0x89, 0x00}) {
		t.Fatalf("blank run: % X", got)
	}
	lit := make([]byte, 100)
	for i := range lit {
		lit[i] = byte(i%2 + 0xC1)
	}
	got = Compress(lit)
	if got[0] != 0xFF || got[64] != 0xC0|37 {
		t.Fatalf("literal split wrong: %X %X", got[0], got[64])
	}
}

func TestRoundTripRandom(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		n := r.Intn(300)
		rec := make([]byte, n)
		for j := range rec {
			switch r.Intn(4) {
			case 0:
				rec[j] = 0x40
			case 1:
				rec[j] = 0xF0
			default:
				rec[j] = byte(r.Intn(256))
			}
		}
		enc := Compress(rec)
		// Every SCB must appear at least every 64 bytes.
		dec, n2, ab, err := Decompress(enc)
		if err != nil || ab || n2 != len(enc) || !bytes.Equal(dec, rec) {
			t.Fatalf("round trip %d failed: err=%v", i, err)
		}
	}
}

func TestDecompressTolerance(t *testing.T) {
	// Abort SCB.
	_, n, ab, err := Decompress([]byte{0xC1, 0xC1, 0x40, 0x99})
	if err != nil || !ab || n != 3 {
		t.Fatalf("abort: n=%d ab=%v err=%v", n, ab, err)
	}
	// Missing EOR.
	if _, _, _, err := Decompress([]byte{0xC1, 0xC1}); err == nil {
		t.Fatal("expected missing EOR error")
	}
	// Undefined SCB.
	if _, _, _, err := Decompress([]byte{0x41, 0x00}); err == nil {
		t.Fatal("expected bad SCB error")
	}
	// Literal overflow.
	if _, _, _, err := Decompress([]byte{0xC5, 0x01}); err == nil {
		t.Fatal("expected overflow error")
	}
	// Zero counts are no-ops.
	rec, _, _, err := Decompress([]byte{0x80, 0xA0, 0x11, 0xC0, 0xC1, 0xC1, 0x00})
	if err != nil || !bytes.Equal(rec, []byte{0xC1}) {
		t.Fatalf("zero counts: % X %v", rec, err)
	}
	// Trailing data after EOR is not consumed.
	_, n, _, _ = Decompress([]byte{0x00, 0x9A})
	if n != 1 {
		t.Fatalf("n=%d", n)
	}
}
