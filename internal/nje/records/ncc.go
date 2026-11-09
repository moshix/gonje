package records

import (
	"encoding/binary"
	"fmt"
	"strings"

	"gonje/internal/ebcdic"
)

// SignonLen is NCCIDL for a full I/J record (X'29' = 41 bytes, counted
// from the RCB).
const SignonLen = 0x29

// CESPredefined is the NCCIEVNT value a non-path-manager J record carries.
const CESPredefined = 0xFFFFFFFF

// Signon is an initial (I) or response (J) signon record, Tables 39/40
// (docs/spec/02 section 3.1/3.2):
//
//	00 RCB F0 | 01 SRCB 'I'/'J' | 02 NCCIDL | 03 NCCINODE(8) | 0B NCCIQUAL
//	0C NCCIEVNT(4) | 10 NCCIREST(2) | 12 NCCIBFSZ(2) | 14 NCCILPAS(8)
//	1C NCCINPAS(8) | 24 NCCIFLG | 25 NCCIFEAT(4)
//
// The manual prints NCCINODE at offset 4 in Table 39; that is a typo
// (3+8 = X'0B' = NCCIQUAL), see spec 02 AMBIG-9.
type Signon struct {
	SRCB     byte // SRCBSignonI or SRCBSignonJ
	Node     string
	Qual     byte
	Event    uint32
	Resist   uint16
	BufSize  uint16
	LinePW   string
	NodePW   string
	Flags    byte
	Features [4]byte
	Length   int // NCCIDL as received
}

// Record encodes the signon as an F0 record.
func (s *Signon) Record() Record {
	// Data starts at the NCCIDL byte (offset 2), so offsets below are
	// manual offsets minus 2.
	d := make([]byte, SignonLen-2)
	d[0] = SignonLen
	ebcdic.PutField(d[1:9], strings.ToUpper(s.Node))
	d[9] = s.Qual
	binary.BigEndian.PutUint32(d[10:14], s.Event)
	binary.BigEndian.PutUint16(d[14:16], s.Resist)
	binary.BigEndian.PutUint16(d[16:18], s.BufSize)
	ebcdic.PutField(d[18:26], s.LinePW)
	ebcdic.PutField(d[26:34], s.NodePW)
	d[34] = s.Flags
	copy(d[35:39], s.Features[:])
	return Record{RCB: RCBControl, SRCB: s.SRCB, Data: d}
}

// ParseSignon decodes an I or J record. Older systems send a shorter
// NCCIDL (no NCCIFEAT): missing fields default to zero/blank.
func ParseSignon(r Record) (*Signon, error) {
	if r.RCB != RCBControl || (r.SRCB != SRCBSignonI && r.SRCB != SRCBSignonJ) {
		return nil, fmt.Errorf("records: not a signon record (%02X %02X)", r.RCB, r.SRCB)
	}
	if len(r.Data) < 9 {
		return nil, fmt.Errorf("records: signon record too short (%d)", len(r.Data))
	}
	d := make([]byte, SignonLen-2)
	copy(d, r.Data)
	s := &Signon{
		SRCB:    r.SRCB,
		Length:  int(r.Data[0]),
		Node:    ebcdic.FieldString(d[1:9]),
		Qual:    d[9],
		Event:   binary.BigEndian.Uint32(d[10:14]),
		Resist:  binary.BigEndian.Uint16(d[14:16]),
		BufSize: binary.BigEndian.Uint16(d[16:18]),
		LinePW:  ebcdic.FieldString(d[18:26]),
		NodePW:  ebcdic.FieldString(d[26:34]),
		Flags:   d[34],
	}
	copy(s.Features[:], d[35:39])
	return s, nil
}

// String describes the signon for panels and logs.
func (s *Signon) String() string {
	t := "I"
	if s.SRCB == SRCBSignonJ {
		t = "J"
	}
	return fmt.Sprintf("signon %s node=%s qual=%d ces=%08X bufsz=%d feat=%02X",
		t, s.Node, s.Qual, s.Event, s.BufSize, s.Features[0])
}

// SignoffRecord returns the signoff record: just F0 C2 (Table 44 defines
// no length byte).
func SignoffRecord() Record { return Record{RCB: RCBControl, SRCB: SRCBSignoff} }
