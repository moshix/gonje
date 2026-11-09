package records

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"gonje/internal/ebcdic"
)

// Header sizes for the minimal records gonje sends
// (docs/spec/04 sections 7-8, docs/spec/05 section 8.d).
const (
	NJHGeneralLen = 212 // X'D4'
	NJHLen        = 4 + NJHGeneralLen
	NDHGeneralLen = 120 // X'78'
	NDHLen        = 4 + NDHGeneralLen
	NJTGeneralLen = 48 // X'30'
	NJTLen        = 4 + NJTGeneralLen
)

// RECFM bits for NDHGRCFM (spec 05 section 3.2).
const (
	RecFmFixed     = 0x80
	RecFmVariable  = 0x40
	RecFmUndefined = 0xC0
	RecFmASA       = 0x04
	RecFmMachine   = 0x02
)

// NDHGFLG2 bits.
const (
	NDHFlag2Print = 0x80
	NDHFlag2Punch = 0x40
	NDHFlag2RSCS  = 0x20 // take FILENAME/FILETYPE from NDHGPROC/NDHGSTEP
)

// todEpochOffset is the number of seconds between 1900-01-01 (S/370 TOD
// epoch) and 1970-01-01 (Unix epoch).
const todEpochOffset = 2208988800

// TOD converts a time to an S/370 STCK value: microseconds since 1900
// shifted left 12 bits (bit 51 = 1 microsecond).
func TOD(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	us := uint64(t.UnixMicro()) + todEpochOffset*1_000_000
	return us << 12
}

// FromTOD converts an STCK value back to a time (zero for 0).
func FromTOD(v uint64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	us := int64(v>>12) - todEpochOffset*1_000_000
	return time.UnixMicro(us).UTC()
}

// section is one LEN/TYPE/MOD section inside a header record.
type section struct {
	typ, mod byte
	data     []byte // whole section including its 4-byte LEN/TYPE/MOD
}

// walkSections splits a header (prefix included) into its sections.
// Sections may be longer or shorter than defined; we just follow LEN and
// stop at a LEN < 4 or at the end of the record (spec 04 section 2).
func walkSections(rec []byte) []section {
	total := int(binary.BigEndian.Uint16(rec[0:2]))
	if total > len(rec) || total < 4 {
		total = len(rec)
	}
	var out []section
	for off := 4; off+4 <= total; {
		l := int(binary.BigEndian.Uint16(rec[off : off+2]))
		if l < 4 {
			break
		}
		end := off + l
		if end > total {
			end = total
		}
		out = append(out, section{typ: rec[off+2], mod: rec[off+3], data: rec[off:end]})
		off += l
	}
	return out
}

// fixed copies a section into a zeroed buffer of the defined size so that
// fields beyond a short section read as zero/blank defaults.
func fixed(s []byte, n int) []byte {
	b := make([]byte, n)
	copy(b, s)
	return b
}

// ErrNoGeneral is returned when a header has no general section.
var ErrNoGeneral = errors.New("records: header has no general section")

func checkPrefix(rec []byte, what string) error {
	if len(rec) < 8 {
		return fmt.Errorf("records: %s too short (%d)", what, len(rec))
	}
	return nil
}

// JobHeader is the NJH general section plus the fields gonje cares about.
type JobHeader struct {
	JobID      uint16
	JobClass   byte // EBCDIC class letter
	MsgClass   byte
	Priority   byte
	Copies     byte
	JobName    string
	NotifyUser string
	Entered    time.Time
	OrigNode   string
	OrigUser   string
	ExecNode   string
	ExecUser   string
	PrintNode  string
	PrintUser  string
	PunchNode  string
	PunchUser  string
	Programmer string
	NRec       uint32
	JobNo      uint32
	NotifyNode string
	Raw        []byte // full received header (all sections)
}

// Encode builds a 216-byte job header (prefix + general section only).
// Offsets below are absolute within the record (spec 04 section 7).
func (h *JobHeader) Encode() []byte {
	b := make([]byte, NJHLen)
	binary.BigEndian.PutUint16(b[0:2], NJHLen) // NJHLEN; FLAGS=SEQ=0
	binary.BigEndian.PutUint16(b[4:6], NJHGeneralLen)
	// b[6]=TYPE 00, b[7]=MOD 00
	binary.BigEndian.PutUint16(b[8:10], h.JobID)
	b[10] = orDefault(h.JobClass, 0xC1) // 'A'
	b[11] = orDefault(h.MsgClass, 0xC1)
	b[13] = h.Priority
	b[15] = orDefault(h.Copies, 1)
	ebcdic.PutField(b[20:28], "")
	ebcdic.PutField(b[28:36], h.JobName)
	ebcdic.PutField(b[36:44], h.NotifyUser)
	// 44..59 job passwords: binary zeros
	binary.BigEndian.PutUint64(b[60:68], TOD(h.Entered))
	ebcdic.PutField(b[68:76], h.OrigNode)
	ebcdic.PutField(b[76:84], h.OrigUser)
	ebcdic.PutField(b[84:92], h.ExecNode)
	ebcdic.PutField(b[92:100], h.ExecUser)
	ebcdic.PutField(b[100:108], h.PrintNode)
	ebcdic.PutField(b[108:116], h.PrintUser)
	ebcdic.PutField(b[116:124], h.PunchNode)
	ebcdic.PutField(b[124:132], h.PunchUser)
	ebcdic.PutField(b[132:140], "") // forms
	ebcdic.PutField(b[156:176], h.Programmer)
	ebcdic.PutField(b[176:184], "") // room
	ebcdic.PutField(b[184:192], "") // dept
	ebcdic.PutField(b[192:200], "") // building
	binary.BigEndian.PutUint32(b[200:204], h.NRec)
	binary.BigEndian.PutUint32(b[204:208], h.JobNo)
	ebcdic.PutField(b[208:216], h.NotifyNode)
	return b
}

func orDefault(v, d byte) byte {
	if v == 0 {
		return d
	}
	return v
}

// ParseJobHeader decodes a (reassembled) job header.
func ParseJobHeader(rec []byte) (*JobHeader, error) {
	if err := checkPrefix(rec, "job header"); err != nil {
		return nil, err
	}
	for _, s := range walkSections(rec) {
		if s.typ != 0 || s.mod != 0 {
			continue // optional sections: kept in Raw, otherwise ignored
		}
		g := fixed(s.data, NJHGeneralLen)
		// g offsets are section offsets (= record offset - 4).
		return &JobHeader{
			JobID:      binary.BigEndian.Uint16(g[4:6]),
			JobClass:   g[6],
			MsgClass:   g[7],
			Priority:   g[9],
			Copies:     g[11],
			JobName:    ebcdic.FieldString(g[24:32]),
			NotifyUser: ebcdic.FieldString(g[32:40]),
			Entered:    FromTOD(binary.BigEndian.Uint64(g[56:64])),
			OrigNode:   ebcdic.FieldString(g[64:72]),
			OrigUser:   ebcdic.FieldString(g[72:80]),
			ExecNode:   ebcdic.FieldString(g[80:88]),
			ExecUser:   ebcdic.FieldString(g[88:96]),
			PrintNode:  ebcdic.FieldString(g[96:104]),
			PrintUser:  ebcdic.FieldString(g[104:112]),
			PunchNode:  ebcdic.FieldString(g[112:120]),
			PunchUser:  ebcdic.FieldString(g[120:128]),
			Programmer: ebcdic.FieldString(g[152:172]),
			NRec:       binary.BigEndian.Uint32(g[196:200]),
			JobNo:      binary.BigEndian.Uint32(g[200:204]),
			NotifyNode: ebcdic.FieldString(g[204:212]),
			Raw:        append([]byte(nil), rec...),
		}, nil
	}
	return nil, ErrNoGeneral
}

// DatasetHeader is the NDH general section (or, for SYSIN, a record
// characteristics change section).
type DatasetHeader struct {
	Node    string // NDHGNODE destination node
	Remote  string // NDHGRMT destination user / remote
	Proc    string // NDHGPROC (RSCS: file name)
	Step    string // NDHGSTEP (RSCS: file type)
	DD      string
	DSNo    uint16
	Class   byte
	NRec    uint32
	Flag1   byte
	RecFM   byte
	LRECL   uint16
	Copies  byte
	Form    string
	XWriter string
	Name    string // NDHGNAME data set name qualifier
	Flag2   byte
	PMode   string
	RCCS    bool // header held only a record characteristics change section
	Raw     []byte
}

// Encode builds a 124-byte data set header (prefix + general section).
// Offsets are absolute within the record (spec 05 section 8.d).
func (h *DatasetHeader) Encode() []byte {
	b := make([]byte, NDHLen)
	binary.BigEndian.PutUint16(b[0:2], NDHLen)
	binary.BigEndian.PutUint16(b[4:6], NDHGeneralLen)
	ebcdic.PutField(b[8:16], h.Node)
	ebcdic.PutField(b[16:24], h.Remote)
	ebcdic.PutField(b[24:32], h.Proc)
	ebcdic.PutField(b[32:40], h.Step)
	ebcdic.PutField(b[40:48], h.DD)
	binary.BigEndian.PutUint16(b[48:50], h.DSNo)
	b[51] = orDefault(h.Class, 0xC1)
	binary.BigEndian.PutUint32(b[52:56], h.NRec)
	b[56] = h.Flag1
	b[57] = h.RecFM
	binary.BigEndian.PutUint16(b[58:60], h.LRECL)
	b[60] = orDefault(h.Copies, 1)
	ebcdic.PutField(b[64:72], h.Form)
	ebcdic.PutField(b[72:80], "") // FCB
	ebcdic.PutField(b[80:88], "") // UCS
	ebcdic.PutField(b[88:96], h.XWriter)
	ebcdic.PutField(b[96:104], h.Name)
	b[104] = h.Flag2
	ebcdic.PutField(b[108:116], h.PMode)
	return b
}

// ParseDatasetHeader decodes a (reassembled) data set header.
func ParseDatasetHeader(rec []byte) (*DatasetHeader, error) {
	if err := checkPrefix(rec, "dataset header"); err != nil {
		return nil, err
	}
	for _, s := range walkSections(rec) {
		if s.typ == 0 && s.mod == 0x40 {
			// Record characteristics change (SYSIN only, Table 15).
			c := fixed(s.data, 8)
			return &DatasetHeader{RCCS: true, RecFM: c[5],
				LRECL: binary.BigEndian.Uint16(c[6:8]), Raw: append([]byte(nil), rec...)}, nil
		}
		if s.typ != 0 || s.mod != 0 {
			continue
		}
		g := fixed(s.data, NDHGeneralLen)
		return &DatasetHeader{
			Node:    ebcdic.FieldString(g[4:12]),
			Remote:  ebcdic.FieldString(g[12:20]),
			Proc:    ebcdic.FieldString(g[20:28]),
			Step:    ebcdic.FieldString(g[28:36]),
			DD:      ebcdic.FieldString(g[36:44]),
			DSNo:    binary.BigEndian.Uint16(g[44:46]),
			Class:   g[47],
			NRec:    binary.BigEndian.Uint32(g[48:52]),
			Flag1:   g[52],
			RecFM:   g[53],
			LRECL:   binary.BigEndian.Uint16(g[54:56]),
			Copies:  g[56],
			Form:    ebcdic.FieldString(g[60:68]),
			XWriter: ebcdic.FieldString(g[84:92]),
			Name:    ebcdic.FieldString(g[92:100]),
			Flag2:   g[100],
			PMode:   ebcdic.FieldString(g[104:112]),
			Raw:     append([]byte(nil), rec...),
		}, nil
	}
	return nil, ErrNoGeneral
}

// JobTrailer is the NJT general section.
type JobTrailer struct {
	XClass   byte
	Start    time.Time
	Stop     time.Time
	Lines    uint32
	Cards    uint32
	Priority byte
	Comp     byte
	Raw      []byte
}

// Encode builds a 52-byte job trailer (spec 04 section 8).
func (t *JobTrailer) Encode() []byte {
	b := make([]byte, NJTLen)
	binary.BigEndian.PutUint16(b[0:2], NJTLen)
	binary.BigEndian.PutUint16(b[4:6], NJTGeneralLen)
	b[9] = orDefault(t.XClass, 0xC1)
	binary.BigEndian.PutUint64(b[12:20], TOD(t.Start))
	binary.BigEndian.PutUint64(b[20:28], TOD(t.Stop))
	binary.BigEndian.PutUint32(b[32:36], t.Lines)
	binary.BigEndian.PutUint32(b[36:40], t.Cards)
	b[44], b[45], b[46], b[47] = t.Priority, t.Priority, t.Priority, t.Priority
	b[48] = t.Comp
	return b
}

// ParseJobTrailer decodes a (reassembled) job trailer.
func ParseJobTrailer(rec []byte) (*JobTrailer, error) {
	if err := checkPrefix(rec, "job trailer"); err != nil {
		return nil, err
	}
	for _, s := range walkSections(rec) {
		if s.typ != 0 || s.mod != 0 {
			continue
		}
		g := fixed(s.data, NJTGeneralLen)
		return &JobTrailer{
			XClass:   g[5],
			Start:    FromTOD(binary.BigEndian.Uint64(g[8:16])),
			Stop:     FromTOD(binary.BigEndian.Uint64(g[16:24])),
			Lines:    binary.BigEndian.Uint32(g[28:32]),
			Cards:    binary.BigEndian.Uint32(g[32:36]),
			Priority: g[40],
			Comp:     g[44],
			Raw:      append([]byte(nil), rec...),
		}, nil
	}
	return nil, ErrNoGeneral
}

// MaxHeader is the largest header we reassemble (NJHLEN range 8-32764).
const MaxHeader = 32764

// Reassembler rebuilds a header that the sender split into segments.
//
// Each segment is its own record (same SRCB) starting with a 4-byte
// prefix whose SEQ byte has X'80' = "more segments follow" and X'7F' =
// segment number from 0. Segment 0 is taken whole; later segments add
// their bytes after the prefix. The manual does not say what LEN holds in
// later segments, so it is ignored there (spec 04 section 3.1).
// We never segment outbound headers.
type Reassembler struct {
	buf  []byte
	next byte
}

// Add feeds one segment. It returns the complete header when the last
// segment arrives, or nil while more are expected.
func (r *Reassembler) Add(seg []byte) ([]byte, error) {
	if len(seg) < 4 {
		r.Reset()
		return nil, fmt.Errorf("records: header segment too short (%d)", len(seg))
	}
	seq := seg[3]
	num := seq & 0x7F
	if num == 0 {
		r.buf = append([]byte(nil), seg...)
	} else {
		if r.buf == nil || num != r.next {
			r.Reset()
			return nil, fmt.Errorf("records: header segment %d out of order", num)
		}
		r.buf = append(r.buf, seg[4:]...)
	}
	r.next = num + 1
	if len(r.buf) > MaxHeader {
		r.Reset()
		return nil, fmt.Errorf("records: reassembled header exceeds %d bytes", MaxHeader)
	}
	if seq&0x80 != 0 {
		return nil, nil
	}
	out := r.buf
	// Clear the SEQ byte and trust the bytes we actually have over a
	// segment-0 NJHLEN that may describe the full header differently.
	out[3] = 0
	if l := int(binary.BigEndian.Uint16(out[0:2])); l > len(out) || l < 4 {
		binary.BigEndian.PutUint16(out[0:2], uint16(len(out)))
	}
	r.Reset()
	return out, nil
}

// Pending reports whether a partial header is buffered.
func (r *Reassembler) Pending() bool { return r.buf != nil }

// Reset discards any partial header.
func (r *Reassembler) Reset() { r.buf, r.next = nil, 0 }
