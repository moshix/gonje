// Copyright 2026 by moshix

package records

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"gonje/internal/ebcdic"
)

func TestStreamRCBs(t *testing.T) {
	if SysoutRCB(1) != 0x99 || SysoutRCB(7) != 0xF9 || SysinRCB(1) != 0x98 || SysinRCB(7) != 0xF8 {
		t.Fatal("stream RCB formula")
	}
	if !IsSysout(0xA9) || IsSysout(0xA8) || !IsSysin(0xA8) || IsSysin(0x9A) || IsStream(0x90) {
		t.Fatal("stream classification")
	}
	if StreamNumber(0xC9) != 4 || StreamName(0x99) != "SYSOUT1" || StreamName(0x9A) != "NMR" {
		t.Fatal("stream naming")
	}
}

func TestFCS(t *testing.T) {
	f := FCSNormal
	if f.WaitABit() || !f.Console() || !f.StreamEnabled(0x99) || !f.StreamEnabled(0x98) {
		t.Fatal("normal FCS")
	}
	f = FCS{0xC0, 0x80}
	if !f.WaitABit() || f.Console() || f.StreamEnabled(0x99) {
		t.Fatal("wait-a-bit FCS")
	}
	// Only SYSOUT 1 (FCS2 X'01') enabled.
	f = FCS{0x80, 0x81}
	if !f.StreamEnabled(0x99) || f.StreamEnabled(0xA9) || f.StreamEnabled(0x98) {
		t.Fatal("SYSOUT1 bit")
	}
	// SYSIN 1 = FCS1 X'08'.
	f = FCS{0x88, 0x80}
	if !f.StreamEnabled(0x98) || f.StreamEnabled(0xF9) {
		t.Fatal("SYSIN1 bit")
	}
	// Invalid FCS (high bits missing) = everything enabled.
	f = FCS{0x00, 0x00}
	if f.WaitABit() || !f.Console() || !f.StreamEnabled(0x99) {
		t.Fatal("invalid FCS tolerance")
	}
}

func TestBufferEncodeDecode(t *testing.T) {
	b := &Buffer{BCB: 0x81, FCS: FCSNormal, Records: []Record{
		{RCB: 0x99, SRCB: SRCBData, Data: ebcdic.Encode("HELLO     WORLD")},
		{RCB: 0x99, SRCB: SRCBData, Data: nil},
	}}
	raw := b.Encode()
	if raw[0] != DLE || raw[1] != STX || raw[2] != 0x81 || raw[len(raw)-1] != 0 {
		t.Fatalf("framing % X", raw)
	}
	got, err := Decode(raw)
	if err != nil || len(got.Records) != 2 || got.BCB != 0x81 || got.FCS != FCSNormal {
		t.Fatalf("decode %v %+v", err, got)
	}
	if ebcdic.Decode(got.Records[0].Data) != "HELLO     WORLD" || len(got.Records[1].Data) != 0 {
		t.Fatal("data mismatch")
	}
	// Null buffer.
	n := (&Buffer{BCB: 0x80, FCS: FCSNormal}).Encode()
	if !bytes.Equal(n, []byte{0x10, 0x02, 0x80, 0x8F, 0xCF, 0x00}) {
		t.Fatalf("null % X", n)
	}
	if nb, err := Decode(n); err != nil || !nb.IsNull() {
		t.Fatal("null decode")
	}
}

func TestDecodeTolerance(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		recs int
	}{
		{"no DLE STX", []byte{0x80, 0x8F, 0xCF, 0x00}, 0},
		{"DLE ETB", []byte{0x10, 0x02, 0x80, 0x8F, 0xCF, 0x00, 0x10, 0x26}, 0},
		{"no EOB", []byte{0x80, 0x8F, 0xCF}, 0},
		{"control with EOR", []byte{0x80, 0x8F, 0xCF, 0x90, 0x99, 0x00, 0x00}, 1},
		{"control without EOR", []byte{0x80, 0x8F, 0xCF, 0xA0, 0x99, 0x00}, 1},
		{"two controls no EOR", []byte{0x80, 0x8F, 0xCF, 0x90, 0x99, 0x90, 0xA9}, 2},
		{"EOF with EOR", []byte{0x80, 0x8F, 0xCF, 0x99, 0x00, 0x00, 0x00}, 1},
		{"EOF without EOR", []byte{0x80, 0x8F, 0xCF, 0x99, 0x00}, 1},
		{"signoff", []byte{0x80, 0x8F, 0xCF, 0xF0, 0xC2, 0x00}, 1},
	}
	for _, c := range cases {
		b, err := Decode(c.raw)
		if err != nil || len(b.Records) != c.recs {
			t.Errorf("%s: err=%v recs=%d", c.name, err, len(b.Records))
		}
	}
	// B0 with reason code.
	b, err := Decode([]byte{0x80, 0x8F, 0xCF, 0xB0, 0x99, 0xC2, 0x18, 0x10, 0x00, 0x00})
	if err != nil || len(b.Records) != 1 || !bytes.Equal(b.Records[0].Data, []byte{0x18, 0x10}) {
		t.Fatalf("B0 reason: %v %+v", err, b)
	}
	// Transmitter cancel via SCB 40.
	b, _ = Decode([]byte{0x80, 0x8F, 0xCF, 0x99, 0x80, 0xC1, 0xC1, 0x40, 0x00})
	if len(b.Records) != 1 || !b.Records[0].Aborted {
		t.Fatal("SCB abort")
	}
	// Unknown RCB.
	if _, err := Decode([]byte{0x80, 0x8F, 0xCF, 0x91, 0x00, 0x00}); err == nil {
		t.Fatal("expected undefined RCB error")
	}
	if _, err := Decode([]byte{0x80}); err != ErrShortBuffer {
		t.Fatal("short buffer")
	}
}

func TestClassify(t *testing.T) {
	if Classify(SOHENQ) != KindSOHENQ || Classify(DLEACK0) != KindDLEACK0 ||
		Classify(SYNNAK) != KindSYNNAK || Classify([]byte{NAK}) != KindNAK ||
		Classify([]byte{0x10, 0x02, 0x80, 0x8F, 0xCF, 0}) != KindBuffer ||
		Classify([]byte{0x80, 0x8F, 0xCF}) != KindBuffer || Classify([]byte{0x05}) != KindUnknown {
		t.Fatal("classify")
	}
	if KindSOHENQ.String() != "SOH ENQ" {
		t.Fatal("kind string")
	}
}

func TestSignon(t *testing.T) {
	s := &Signon{SRCB: SRCBSignonI, Node: "nodea", Qual: 1, BufSize: 8192,
		NodePW: "SECRET"}
	r := s.Record()
	full := r.Bytes()
	if len(full) != 0x29 || full[0] != 0xF0 || full[1] != 0xC9 || full[2] != 0x29 {
		t.Fatalf("I record % X", full)
	}
	if !bytes.Equal(full[3:11], ebcdic.Field("NODEA", 8)) || full[11] != 1 {
		t.Fatal("node/qual offsets")
	}
	if binary.BigEndian.Uint16(full[0x12:]) != 8192 {
		t.Fatal("bufsize offset")
	}
	// Through a buffer and back.
	raw := (&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{r}}).Encode()
	b, err := Decode(raw)
	if err != nil || len(b.Records) != 1 {
		t.Fatal(err)
	}
	p, err := ParseSignon(b.Records[0])
	if err != nil || p.Node != "NODEA" || p.BufSize != 8192 || p.NodePW != "SECRET" || p.Length != 0x29 {
		t.Fatalf("%+v %v", p, err)
	}
	// J with CES predefined.
	j := (&Signon{SRCB: SRCBSignonJ, Node: "B", Event: CESPredefined}).Record()
	pj, _ := ParseSignon(j)
	if pj.Event != 0xFFFFFFFF || !strings.Contains(pj.String(), "signon J") {
		t.Fatal("J")
	}
	// Short (old-style) record: features default to zero.
	short := Record{RCB: 0xF0, SRCB: 0xC9, Data: append([]byte{0x14}, ebcdic.Field("OLD", 8)...)}
	ps, err := ParseSignon(short)
	if err != nil || ps.Node != "OLD" || ps.Features[0] != 0 {
		t.Fatal("short signon")
	}
	if _, err := ParseSignon(SignoffRecord()); err == nil {
		t.Fatal("signoff is not a signon")
	}
	if so := (&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{SignoffRecord()}}).Encode(); !bytes.Equal(so[5:], []byte{0xF0, 0xC2}) {
		// An F0 record alone: no end-of-buffer RCB 00 (see EncodeFramed).
		t.Fatalf("signoff % X", so)
	}
}

func TestTOD(t *testing.T) {
	tm := time.Date(2026, 11, 9, 12, 0, 0, 123456000, time.UTC)
	if got := FromTOD(TOD(tm)); !got.Equal(tm) {
		t.Fatalf("TOD round trip %v", got)
	}
	// 1970-01-01 is X'7D91048BCA000000' in TOD format.
	if TOD(time.Unix(0, 0)) != 0x7D91048BCA000000 {
		t.Fatalf("epoch %X", TOD(time.Unix(0, 0)))
	}
	if TOD(time.Time{}) != 0 || !FromTOD(0).IsZero() {
		t.Fatal("zero")
	}
}

func TestJobHeader(t *testing.T) {
	h := &JobHeader{JobID: 42, JobName: "MOSHE", NotifyUser: "MOSHE", OrigNode: "NODEA",
		OrigUser: "MOSHE", ExecNode: "NODEA", ExecUser: "MOSHE", PrintNode: "NODEB",
		PrintUser: "BOB", PunchNode: "NODEB", PunchUser: "BOB", NRec: 7, JobNo: 42,
		NotifyNode: "NODEA", Entered: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	b := h.Encode()
	if len(b) != 216 || binary.BigEndian.Uint16(b) != 216 || binary.BigEndian.Uint16(b[4:]) != 212 {
		t.Fatal("lengths")
	}
	if b[15] != 1 || b[10] != 0xC1 || ebcdic.FieldString(b[68:76]) != "NODEA" || ebcdic.FieldString(b[28:36]) != "MOSHE" {
		t.Fatal("field offsets")
	}
	p, err := ParseJobHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.JobID != 42 || p.JobName != "MOSHE" || p.OrigNode != "NODEA" || p.PrintUser != "BOB" ||
		p.NRec != 7 || p.NotifyNode != "NODEA" || !p.Entered.Equal(h.Entered) {
		t.Fatalf("%+v", p)
	}
	// Optional section before the general one, and a short general section.
	opt := []byte{0, 12, 0x8A, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	short := append([]byte{0, 0, 0, 0}, opt...)
	gen := make([]byte, 40)
	binary.BigEndian.PutUint16(gen, 40)
	copy(gen[24:32], ebcdic.Field("SHORTJ", 8))
	short = append(short, gen...)
	binary.BigEndian.PutUint16(short, uint16(len(short)))
	p, err = ParseJobHeader(short)
	if err != nil || p.JobName != "SHORTJ" || p.OrigNode != "" {
		t.Fatalf("short header %+v %v", p, err)
	}
	if _, err := ParseJobHeader([]byte{0, 8, 0, 0, 0, 4, 0x8A, 0}); err != ErrNoGeneral {
		t.Fatal("no general")
	}
	if _, err := ParseJobHeader([]byte{1}); err == nil {
		t.Fatal("too short")
	}
}

func TestDatasetHeader(t *testing.T) {
	h := &DatasetHeader{Node: "NODEB", Remote: "BOB", Proc: "README", Step: "TXT",
		DD: "SYSPRINT", DSNo: 1, NRec: 10, RecFM: RecFmVariable, LRECL: 80,
		Name: "README", Flag2: NDHFlag2Print | NDHFlag2RSCS, PMode: "LINE"}
	b := h.Encode()
	if len(b) != 124 || binary.BigEndian.Uint16(b) != 124 || b[51] != 0xC1 || b[60] != 1 {
		t.Fatal("layout")
	}
	if ebcdic.FieldString(b[96:104]) != "README" || ebcdic.FieldString(b[108:116]) != "LINE" {
		t.Fatal("name/pmode offsets")
	}
	p, err := ParseDatasetHeader(b)
	if err != nil || p.Node != "NODEB" || p.Remote != "BOB" || p.Name != "README" ||
		p.LRECL != 80 || p.RecFM != RecFmVariable || p.NRec != 10 || p.Step != "TXT" || p.RCCS {
		t.Fatalf("%+v %v", p, err)
	}
	rccs := []byte{0, 12, 0, 0, 0, 8, 0, 0x40, 0, 0x80, 0, 80}
	p, err = ParseDatasetHeader(rccs)
	if err != nil || !p.RCCS || p.LRECL != 80 || p.RecFM != 0x80 {
		t.Fatalf("rccs %+v %v", p, err)
	}
}

func TestJobTrailer(t *testing.T) {
	tr := &JobTrailer{Lines: 99}
	b := tr.Encode()
	if len(b) != 52 || binary.BigEndian.Uint16(b) != 52 || b[9] != 0xC1 {
		t.Fatal("layout")
	}
	p, err := ParseJobTrailer(b)
	if err != nil || p.Lines != 99 || p.XClass != 0xC1 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestReassembler(t *testing.T) {
	var r Reassembler
	// Unsegmented header passes straight through.
	full := (&JobTrailer{}).Encode()
	out, err := r.Add(full)
	if err != nil || !bytes.Equal(out, full) {
		t.Fatal("unsegmented")
	}
	// Split a job header into 3 segments, each with its own prefix.
	hdr := (&JobHeader{JobName: "SEGJOB", OrigNode: "X"}).Encode()
	seg0 := append([]byte(nil), hdr[:100]...)
	seg0[3] = 0x80
	seg1 := append([]byte{0, 216, 0, 0x81}, hdr[100:180]...)
	seg2 := append([]byte{0, 216, 0, 0x02}, hdr[180:]...)
	if out, _ := r.Add(seg0); out != nil || !r.Pending() {
		t.Fatal("seg0")
	}
	if out, _ := r.Add(seg1); out != nil {
		t.Fatal("seg1")
	}
	out, err = r.Add(seg2)
	if err != nil || !bytes.Equal(out, hdr) {
		t.Fatalf("reassembled differs: %v", err)
	}
	p, err := ParseJobHeader(out)
	if err != nil || p.JobName != "SEGJOB" {
		t.Fatal("parse reassembled")
	}
	// Out of order.
	if _, err := r.Add(seg2); err == nil {
		t.Fatal("expected out of order error")
	}
	if _, err := r.Add([]byte{1}); err == nil {
		t.Fatal("expected short error")
	}
}

func TestNMRMessage(t *testing.T) {
	ms := NewMessages("NODEA", "MOSHE", "NODEB", "BOB", "TEST MESSAGE")
	if len(ms) != 1 {
		t.Fatal("count")
	}
	b := ms[0].Encode()
	if b[0] != 0x20 || b[1] != 0x77 || b[2] != 0x0C || int(b[3]) != 8+12 || len(b) != 30+20 {
		t.Fatalf("header % X", b[:4])
	}
	if !bytes.Equal(b[13:21], ebcdic.Field("BOB", 8)) || !bytes.Equal(b[30:38], ebcdic.Field("MOSHE", 8)) {
		t.Fatal("out/sender")
	}
	p, err := ParseNMR(b)
	if err != nil || p.IsCommand() || p.Text() != "TEST MESSAGE" || p.Sender() != "MOSHE" ||
		p.OutUser() != "BOB" || p.ToNode != "NODEB" || p.FromNode != "NODEA" || p.IsResponse() {
		t.Fatalf("%+v %q", p, p.Text())
	}
	long := strings.Repeat("x", 300)
	ms = NewMessages("A", "U", "B", "V", long)
	total := ""
	for _, m := range ms {
		if len(m.Encode()) > NMRMaxRecord {
			t.Fatal("NMR too big")
		}
		total += m.Text()
	}
	if total != long {
		t.Fatal("split text mismatch")
	}
	op := NewMessages("A", "U", "B", "*", "hi")[0]
	if op.Flag != 0 || op.Out[2] != 0x41 {
		t.Fatal("operator message")
	}
}

func TestNMRMessageVariants(t *testing.T) {
	// Timestamp + no sender (JES2 style).
	msg := append(ebcdic.Encode("12.00.00"), ebcdic.Encode("HELLO")...)
	m := &NMR{Flag: 0x20, Level: 0x37, Type: 0x00, ML: 5, Msg: msg}
	if m.Text() != "HELLO" || m.Sender() != "" || !m.IsResponse() {
		t.Fatalf("ts variant %q", m.Text())
	}
	// Timestamp + sender.
	msg = append(append(ebcdic.Encode("12.00.00"), ebcdic.Field("OPER", 8)...), ebcdic.Encode("HI")...)
	m = &NMR{Type: 0x08, ML: 10, Msg: msg}
	if m.Text() != "HI" || m.Sender() != "OPER" {
		t.Fatalf("ts+sender %q %q", m.Text(), m.Sender())
	}
	// ML zero: take the rest.
	m = &NMR{Type: 0x04, ML: 0, Msg: ebcdic.Encode("REST")}
	if m.Text() != "REST" {
		t.Fatal("ML 0")
	}
	// ML too large: clamp.
	m = &NMR{Type: 0x04, ML: 200, Msg: ebcdic.Encode("CLAMP")}
	if m.Text() != "CLAMP" {
		t.Fatal("clamp")
	}
	if _, err := ParseNMR([]byte{1, 2}); err == nil {
		t.Fatal("short NMR")
	}
}

func TestNMRCommandAndResponse(t *testing.T) {
	c, err := NewCommand("NODEA", "MOSHE", "NODEB", "Q SYS")
	if err != nil {
		t.Fatal(err)
	}
	b := c.Encode()
	if b[0] != 0xA0 || b[2] != 0 || b[3] != 5 {
		t.Fatalf("cmd header % X", b[:4])
	}
	p, _ := ParseNMR(b)
	if !p.IsCommand() || p.IsFormatted() || p.Text() != "Q SYS" || p.Sender() != "MOSHE" {
		t.Fatal("cmd parse")
	}
	r := NewResponse(p, "NODEB", "NODEB IS UP")
	if r.ToNode != "NODEA" || r.Flag != 0x20 || r.Level != 0x37 || r.OutUser() != "MOSHE" {
		t.Fatalf("response %+v", r)
	}
	pr, _ := ParseNMR(r.Encode())
	if pr.Text() != "NODEB IS UP" || !pr.IsResponse() {
		t.Fatal("response parse")
	}
	if _, err := NewCommand("A", "U", "B", ""); err == nil {
		t.Fatal("empty command")
	}
	if _, err := NewCommand("A", "U", "B", strings.Repeat("Q", 133)); err == nil {
		t.Fatal("long command")
	}
	// NMR inside a buffer with SRCB 00 (JES2) still decodes.
	raw := (&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{{RCB: 0x9A, SRCB: 0x00, Data: b}}}).Encode()
	buf, err := Decode(raw)
	if err != nil || len(buf.Records) != 1 || !bytes.Equal(buf.Records[0].Data, b) {
		t.Fatal("NMR in buffer")
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]Record{
		"signon I":               {RCB: 0xF0, SRCB: 0xC9},
		"NMR":                    {RCB: 0x9A},
		"request SYSOUT1":        {RCB: 0x90, SRCB: 0x99},
		"permit SYSOUT1":         {RCB: 0xA0, SRCB: 0x99},
		"EOF-ack SYSIN2":         {RCB: 0xC0, SRCB: 0xA8},
		"SYSOUT1 EOF":            {RCB: 0x99, SRCB: 0},
		"SYSOUT1 job header":     {RCB: 0x99, SRCB: 0xC0},
		"SYSOUT1 dataset header": {RCB: 0x99, SRCB: 0xE0},
		"SYSOUT1 data":           {RCB: 0x99, SRCB: 0xA0},
		"SYSOUT1 job trailer":    {RCB: 0x99, SRCB: 0xD0},
		"signoff B":              {RCB: 0xF0, SRCB: 0xC2},
		"deny SYSOUT1":           {RCB: 0xB0, SRCB: 0x99},
		"BCB-error":              {RCB: 0xE0},
		"SYSOUT1 cancel":         {RCB: 0x99, SRCB: 0x40},
	}
	for want, r := range cases {
		if got := r.Describe(); got != want {
			t.Errorf("Describe(%02X %02X) = %q want %q", r.RCB, r.SRCB, got, want)
		}
	}
}

// TestHeaderOffsets fixes every field gonje writes to the manual's
// offsets (spec 04 Table 5 / section 7, spec 05 Table 14 / section 8.d,
// spec 04 Table 30). Offsets are absolute within the record.
func TestHeaderOffsets(t *testing.T) {
	type char struct {
		off, n int
		want   string
	}
	type bin struct {
		off, n int
		want   uint64
	}
	check := func(what string, b []byte, chars []char, bins []bin) {
		t.Helper()
		for _, c := range chars {
			if got := ebcdic.Decode(b[c.off : c.off+c.n]); got != string(ebcdic.Decode(ebcdic.Field(c.want, c.n))) {
				t.Errorf("%s char at X'%02X': %q want %q", what, c.off, got, c.want)
			}
		}
		for _, x := range bins {
			var v uint64
			for _, by := range b[x.off : x.off+x.n] {
				v = v<<8 | uint64(by)
			}
			if v != x.want {
				t.Errorf("%s binary at X'%02X': %X want %X", what, x.off, v, x.want)
			}
		}
	}
	njh := (&JobHeader{JobID: 0x1234, JobName: "JNAM", NotifyUser: "USID", OrigNode: "ORGN",
		OrigUser: "ORGR", ExecNode: "XEQN", ExecUser: "XEQU", PrintNode: "PRTN", PrintUser: "PRTR",
		PunchNode: "PUNN", PunchUser: "PUNR", Programmer: "PROGRAMMER NAME", NRec: 0x01020304,
		JobNo: 0x05060708, NotifyNode: "NTYN", Priority: 9, Copies: 3}).Encode()
	check("NJH", njh, []char{
		{0x14, 8, ""}, {0x1C, 8, "JNAM"}, {0x24, 8, "USID"}, {0x44, 8, "ORGN"}, {0x4C, 8, "ORGR"},
		{0x54, 8, "XEQN"}, {0x5C, 8, "XEQU"}, {0x64, 8, "PRTN"}, {0x6C, 8, "PRTR"}, {0x74, 8, "PUNN"},
		{0x7C, 8, "PUNR"}, {0x84, 8, ""}, {0x9C, 20, "PROGRAMMER NAME"}, {0xB0, 8, ""}, {0xB8, 8, ""},
		{0xC0, 8, ""}, {0xD0, 8, "NTYN"},
	}, []bin{
		{0x00, 2, 216}, {0x02, 2, 0}, {0x04, 2, 0xD4}, {0x06, 2, 0}, {0x08, 2, 0x1234},
		{0x0A, 1, 0xC1}, {0x0B, 1, 0xC1}, {0x0C, 1, 0}, {0x0D, 1, 9}, {0x0E, 1, 0}, {0x0F, 1, 3},
		{0x10, 1, 0}, {0x12, 2, 0}, {0x2C, 8, 0}, {0x34, 8, 0}, {0x8C, 4, 0}, {0x90, 4, 0},
		{0x94, 4, 0}, {0x98, 4, 0}, {0xC8, 4, 0x01020304}, {0xCC, 4, 0x05060708},
	})
	ndh := (&DatasetHeader{Node: "DNODE", Remote: "DUSER", Proc: "FNAME", Step: "FTYPE", DD: "SYSPRINT",
		DSNo: 0x0102, Class: 0xC2, NRec: 0x0A0B0C0D, Flag1: 0x04, RecFM: 0x80, LRECL: 80, Copies: 2,
		Form: "STD", XWriter: "XW", Name: "DSNAME", Flag2: 0x40, PMode: "LINE"}).Encode()
	check("NDH", ndh, []char{
		{0x08, 8, "DNODE"}, {0x10, 8, "DUSER"}, {0x18, 8, "FNAME"}, {0x20, 8, "FTYPE"},
		{0x28, 8, "SYSPRINT"}, {0x40, 8, "STD"}, {0x48, 8, ""}, {0x50, 8, ""}, {0x58, 8, "XW"},
		{0x60, 8, "DSNAME"}, {0x6C, 8, "LINE"},
	}, []bin{
		{0x00, 2, 124}, {0x04, 2, 0x78}, {0x06, 2, 0}, {0x30, 2, 0x0102}, {0x32, 1, 0}, {0x33, 1, 0xC2},
		{0x34, 4, 0x0A0B0C0D}, {0x38, 1, 0x04}, {0x39, 1, 0x80}, {0x3A, 2, 80}, {0x3C, 1, 2},
		{0x3D, 1, 0}, {0x3E, 1, 0}, {0x68, 1, 0x40}, {0x69, 1, 0}, {0x74, 4, 0}, {0x78, 4, 0},
	})
	njt := (&JobTrailer{Lines: 0x11223344, Cards: 0x55, Priority: 6, Comp: 0x41}).Encode()
	check("NJT", njt, nil, []bin{
		{0x00, 2, 52}, {0x04, 2, 0x30}, {0x06, 2, 0}, {0x08, 1, 0}, {0x09, 1, 0xC1}, {0x0C, 8, 0},
		{0x14, 8, 0}, {0x20, 4, 0x11223344}, {0x24, 4, 0x55}, {0x2C, 1, 6}, {0x2D, 1, 6},
		{0x2E, 1, 6}, {0x2F, 1, 6}, {0x30, 1, 0x41},
	})
}

// TestResponseToRSCSCommand is the M2 byte-level check: an RSCS command
// NMR has NMRFLAG X'80' (no routing bit, Table 35) and the user in NMROUT.
// The reply must carry NMRFLAGT so NMROUT is read as a userid (Table 34).
func TestResponseToRSCSCommand(t *testing.T) {
	cmd := []byte{0x80, 0x77, 0x00, 0x05}
	cmd = append(cmd, ebcdic.Field("GONJE", 8)...)
	cmd = append(cmd, 0x00)
	cmd = append(cmd, ebcdic.Field("MAINT", 8)...)
	cmd = append(cmd, ebcdic.Field("VMNODE", 8)...)
	cmd = append(cmd, 0x00)
	cmd = append(cmd, ebcdic.Encode("Q SYS")...)
	req, err := ParseNMR(cmd)
	if err != nil {
		t.Fatal(err)
	}
	b := NewResponse(req, "GONJE", "OK").Encode()
	want := []byte{0x20, 0x37, 0x04, 0x02}
	want = append(want, ebcdic.Field("VMNODE", 8)...)
	want = append(want, 0x00)
	want = append(want, ebcdic.Field("MAINT", 8)...)
	want = append(want, ebcdic.Field("GONJE", 8)...)
	want = append(want, 0x00)
	want = append(want, ebcdic.Encode("OK")...)
	if !bytes.Equal(b, want) {
		t.Fatalf("response\n got % X\nwant % X", b, want)
	}
	// MCS routing codes in NMROUT (not a name): no T bit is invented.
	req.Out = [8]byte{0, 0, 0x41, 0, 0, 0, 0, 0}
	if NewResponse(req, "GONJE", "OK").Flag != 0 {
		t.Fatal("T bit set for MCS routing NMROUT")
	}
	// JES2 console origin (U bit) is copied as is.
	req.Flag = 0x90
	if NewResponse(req, "GONJE", "OK").Flag != 0x10 {
		t.Fatal("U bit not copied")
	}
}

func TestStreamControlWithoutEOR(t *testing.T) {
	// N1: two C0 records without EOR must not be misread as C0 + SCB.
	b, err := Decode([]byte{0x80, 0x8F, 0xCF, 0xC0, 0x99, 0xC0, 0xA9, 0x00})
	if err != nil || len(b.Records) != 2 || b.Records[1].SRCB != 0xA9 {
		t.Fatalf("%v %+v", err, b)
	}
}

func TestSignonMasks(t *testing.T) {
	r := (&Signon{SRCB: SRCBSignonJ, Node: "A", NodePW: "SECRET", LinePW: "LINEPW"}).Record()
	full := r.Bytes()
	m := SignonMask(full)
	for i := range full {
		if m[i] != (i >= 0x14 && i < 0x24) {
			t.Fatalf("mask at %d = %v", i, m[i])
		}
	}
	raw := (&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{r}}).Encode()
	bm := BufferMask(raw)
	if !bm[5+0x14] || !bm[5+0x23] || bm[5+0x13] || bm[5+0x24] {
		t.Fatal("buffer mask offsets")
	}
	if SignonMask((&NMR{}).Record().Bytes()) != nil || BufferMask([]byte{0x10, 0x70}) != nil {
		t.Fatal("mask for non-signon")
	}
}

func TestReassemblerCap(t *testing.T) {
	var r Reassembler
	seg := make([]byte, 300)
	seg[3] = 0x80
	r.Add(seg)
	var err error
	for i := 1; i < 127 && err == nil; i++ {
		s := make([]byte, 300)
		s[3] = 0x80 | byte(i)
		_, err = r.Add(s)
	}
	if err == nil {
		t.Fatal("oversized header accepted")
	}
}
