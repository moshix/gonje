// Copyright 2026 by moshix

package netdata

import (
	"bytes"
	"os"
	"testing"

	"gonje/internal/ebcdic"
)

// twoINMR02 builds what TSO and NJE38 TRANSMIT send for a PDS: INMR02 #1
// = IEBCOPY (DSORG PO, the PDS itself), INMR02 #2 = INMCOPY (DSORG PS,
// RECFM VS, the IEBCOPY unload data set). From the second review (M2).
func twoINMR02() []byte {
	s := segments(nil, control("INMR01", nil, unit(KeyFNode, ebcdic.Encode("NODEA")), unit(KeyFUID, ebcdic.Encode("ALICE"))), true, 253)
	s = segments(s, control("INMR02", num(1, 4), unit(KeyUtilN, ebcdic.Encode("IEBCOPY")),
		unit(KeyDSName, ebcdic.Encode("ALICE"), ebcdic.Encode("PDS")), unit(KeyDSOrg, num(DSOrgPO, 2)),
		unit(KeyRECFM, num(0x9000, 2)), unit(KeyLRECL, num(80, 4))), true, 253)
	s = segments(s, control("INMR02", num(1, 4), unit(KeyUtilN, ebcdic.Encode("INMCOPY")),
		unit(KeyDSOrg, num(DSOrgPS, 2)), unit(KeyRECFM, num(0x4802, 2)), unit(KeyLRECL, num(32760, 4))), true, 253)
	s = segments(s, control("INMR03", nil), true, 253)
	s = segments(s, []byte{0x00, 0xCA, 0x6D, 0x0F, 1, 2, 3}, false, 253)
	s = segments(s, control("INMR06", nil), true, 253)
	return s
}

// TestTwoINMR02PDS: two INMR02 records mean a PDS, and the attributes
// are those of the original data set (the first INMR02).
func TestTwoINMR02PDS(t *testing.T) {
	f, err := Parse(twoINMR02())
	if err != nil {
		t.Fatal(err)
	}
	if !f.PDS() {
		t.Fatalf("PDS transmission (two INMR02) not recognised: DSOrg=%04X Utility=%s", f.DSOrg, f.Utility)
	}
	if f.Utility != "IEBCOPY" || DSOrgName(f.DSOrg) != "PO" || f.LRECL != 80 || RecfmName(f.RECFM) != "FB" || f.DSName != "ALICE.PDS" {
		t.Fatalf("attributes from the second INMR02: %+v", f)
	}
	// Only the PO flag of the second INMR02's sibling set: an INMCOPY PS
	// first INMR02 followed by an IEBCOPY one is a PDS too.
	g := segments(nil, control("INMR01", nil, unit(KeyFNode, ebcdic.Encode("NODEA"))), true, 253)
	g = segments(g, control("INMR02", num(1, 4), unit(KeyUtilN, ebcdic.Encode("INMCOPY")), unit(KeyDSOrg, num(DSOrgPS, 2))), true, 253)
	g = segments(g, control("INMR06", nil), true, 253)
	if f, err := Parse(g); err != nil || f.PDS() {
		t.Fatalf("single PS INMR02 taken for a PDS: %v", err)
	}
	// The golden file used by the node tests is this stream.
	if b, err := os.ReadFile("testdata/pds2.netdata"); err != nil || !bytes.Equal(b, twoINMR02()) {
		if os.Getenv("GONJE_UPDATE_GOLDEN") != "" {
			if err := os.WriteFile("testdata/pds2.netdata", twoINMR02(), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatalf("testdata/pds2.netdata out of date (GONJE_UPDATE_GOLDEN=1 to rewrite): %v", err)
	}
}

// TestSequenceNumbersMustRise: columns 73-80 are only stripped when they
// hold strictly rising numbers in at least two records.
func TestSequenceNumbersMustRise(t *testing.T) {
	card := func(text, seq string) []byte {
		b := bytes.Repeat(ebcdic.Encode(" "), 72)
		copy(b, ebcdic.Encode(text))
		return append(b, ebcdic.Encode(seq)...)
	}
	rising := [][]byte{card("A", "00000100"), card("B", "00000200")}
	if recs, ok := StripSequenceNumbers(rising); !ok || len(recs[0]) != 72 {
		t.Fatal("rising numbers not stripped")
	}
	if _, ok := StripSequenceNumbers([][]byte{card("A", "00000100")}); !ok {
		t.Fatal("one ISPF line not stripped")
	}
	for name, recs := range map[string][][]byte{
		"one record": {card("A", "12345678")},
		"equal":      {card("A", "12345678"), card("B", "12345678")},
		"falling":    {card("A", "00000200"), card("B", "00000100")},
		"not digits": {card("A", "00000100"), card("B", "0000010X")},
	} {
		if out, ok := StripSequenceNumbers(recs); ok || len(out[0]) != 80 {
			t.Fatalf("%s: stripped", name)
		}
	}
}
