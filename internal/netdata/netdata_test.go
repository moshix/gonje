// Copyright 2026 by moshix

package netdata

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"gonje/internal/ebcdic"
)

func golden(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/sendfile.netdata")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestGolden decodes the live NJE38 TRANSMIT of HERC01.SENDFILE.
func TestGolden(t *testing.T) {
	f, err := Parse(golden(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.DSName != "HERC01.SENDFILE" || f.FromNode != "HOUCICS" || f.FromUser != "HERC01" ||
		f.ToNode != "MOSHIX" || f.ToUser != "MOSHE" || f.Utility != "INMCOPY" {
		t.Fatalf("%+v", f)
	}
	if f.LRECL != 80 || !f.Fixed() || DSOrgName(f.DSOrg) != "PS" || f.PDS() || !strings.HasPrefix(f.Time, "2026") {
		t.Fatalf("attrs lrecl=%d recfm=%04X dsorg=%04X time=%q", f.LRECL, f.RECFM, f.DSOrg, f.Time)
	}
	if len(f.Records) != 1 {
		t.Fatalf("%d records", len(f.Records))
	}
	recs := StripSequenceNumbers(f.Records)
	if got := strings.TrimRight(ebcdic.Decode(recs[0]), " "); got != "THIS IS A TEXT FILE" {
		t.Fatalf("%q", got)
	}
	ids := []string{}
	for _, c := range f.Controls {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "INMR01,INMR02,INMR03,INMR06" {
		t.Fatalf("controls %v", ids)
	}
}

// --- a tiny NETDATA builder for synthetic cases ---

type tu struct {
	key  uint16
	vals [][]byte
}

func unit(key uint16, vals ...[]byte) tu { return tu{key, vals} }

func num(n, size int) []byte {
	b := make([]byte, size)
	for i := size - 1; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
	return b
}

func control(id string, prefix []byte, units ...tu) []byte {
	b := append(ebcdic.Encode(id), prefix...)
	for _, u := range units {
		b = binary.BigEndian.AppendUint16(b, u.key)
		b = binary.BigEndian.AppendUint16(b, uint16(len(u.vals)))
		for _, v := range u.vals {
			b = binary.BigEndian.AppendUint16(b, uint16(len(v)))
			b = append(b, v...)
		}
	}
	return b
}

// segments splits one logical record into segments of at most max data
// bytes and appends them to the stream.
func segments(stream, rec []byte, ctl bool, max int) []byte {
	for first := true; first || len(rec) > 0; first = false {
		n := min(len(rec), max)
		flags := byte(0)
		if first {
			flags |= flagFirst
		}
		if n == len(rec) {
			flags |= flagLast
		}
		if ctl {
			flags |= flagControl
		}
		stream = append(stream, byte(n+2), flags)
		stream = append(stream, rec[:n]...)
		rec = rec[n:]
	}
	return stream
}

// build makes a NETDATA stream (padded to 80-byte cards).
func build(dsorg, util string, recfm uint16, lrecl int, data [][]byte, maxSeg int) []byte {
	org := DSOrgPS
	if dsorg == "PO" {
		org = DSOrgPO
	}
	s := segments(nil, control("INMR01", nil, unit(KeyFNode, ebcdic.Encode("NODEA")), unit(KeyFUID, ebcdic.Encode("ALICE")),
		unit(KeyTNode, ebcdic.Encode("NODEB")), unit(KeyTUID, ebcdic.Encode("BOB"))), true, 253)
	s = segments(s, control("INMR02", num(1, 4), unit(KeyUtilN, ebcdic.Encode(util)),
		unit(KeyDSName, ebcdic.Encode("ALICE"), ebcdic.Encode("DATA")), unit(KeyDSOrg, num(org, 2)),
		unit(KeyRECFM, num(int(recfm), 2)), unit(KeyLRECL, num(lrecl, 4))), true, 253)
	s = segments(s, control("INMR03", nil), true, 253)
	for _, d := range data {
		s = segments(s, d, false, maxSeg)
	}
	s = segments(s, control("INMR06", nil), true, 253)
	for len(s)%80 != 0 {
		s = append(s, 0)
	}
	return s
}

func TestSpanningAndMultiRecord(t *testing.T) {
	lines := [][]byte{ebcdic.Field("LINE ONE", 80), ebcdic.Field("LINE TWO", 80), ebcdic.Field("LINE THREE", 80)}
	// Data records split into 30-byte segments: every record spans
	// several segments and the segments cross card boundaries.
	f, err := Parse(build("PS", "INMCOPY", 0x9000, 80, lines, 30))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Records) != 3 || !bytes.Equal(f.Records[2], lines[2]) || f.DSName != "ALICE.DATA" || RecfmName(f.RECFM) != "FB" {
		t.Fatalf("%d records %q", len(f.Records), f.DSName)
	}
	// No sequence numbers here: nothing is stripped.
	if len(StripSequenceNumbers(f.Records)[0]) != 80 {
		t.Fatal("stripped columns that were not numbers")
	}
}

func TestBinaryAndPDS(t *testing.T) {
	bin := [][]byte{{0, 1, 2, 3, 0xFF, 0xFE}, {9, 9}}
	f, err := Parse(build("PS", "INMCOPY", 0xC000, 6, bin, 200))
	if err != nil || len(f.Records) != 2 || !bytes.Equal(f.Records[0], bin[0]) || RecfmName(f.RECFM) != "U" {
		t.Fatalf("%+v %v", f, err)
	}
	f, err = Parse(build("PO", "IEBCOPY", 0x8000, 80, bin, 200))
	if err != nil || !f.PDS() || DSOrgName(f.DSOrg) != "PO" {
		t.Fatalf("PDS: %+v %v", f, err)
	}
}

func TestMalformed(t *testing.T) {
	g := golden(t)
	cases := map[string][]byte{
		"empty":         nil,
		"not netdata":   ebcdic.Encode(strings.Repeat("HELLO ", 20)),
		"truncated":     g[:120],
		"bad length":    append(append([]byte(nil), g[:95]...), 1, 0x80),
		"orphan cont":   append(append([]byte(nil), g[:95]...), 4, 0x00, 1, 2),
		"short control": {8, 0xE0, 0xC9, 0xD5, 0xD4, 0xD9, 0xF0, 0xF1, 3, 0xE0, 0},
	}
	for name, b := range cases {
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if IsNetdata(nil) || !IsNetdata(g) {
		t.Fatal("IsNetdata")
	}
}

func FuzzParse(f *testing.F) {
	g, _ := os.ReadFile("testdata/sendfile.netdata")
	f.Add(g)
	f.Add(build("PS", "INMCOPY", 0x9000, 80, [][]byte{ebcdic.Field("X", 80)}, 30))
	f.Fuzz(func(t *testing.T, b []byte) {
		if nf, err := Parse(b); err == nil {
			_ = StripSequenceNumbers(nf.Records)
			_ = RecfmName(nf.RECFM)
			_ = nf.PDS()
		}
	})
}
