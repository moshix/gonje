// Copyright 2026 by moshix

// Package records builds and parses NJE transmission buffers and the
// records they carry: signon/signoff (NCCR I/J/B), job header (NJH),
// data set header (NDH), job trailer (NJT) and nodal messages (NMR).
//
// Over TCP/IP each TTR record holds either a 2-byte BSC line-control
// sequence (SOH ENQ, DLE ACK0, ...) or one BSC/CTC transmission buffer:
//
//	[DLE STX] BCB FCS1 FCS2 { RCB SRCB <SCB-compressed data> 00 } ... 00
//
// See docs/spec/02_presentation_stream_conn.md section 1 and
// docs/spec/03_link_protocols.md sections 2-6.
package records

import (
	"errors"
	"fmt"

	"gonje/internal/nje/scb"
)

// BSC line-control characters (EBCDIC).
const (
	DLE  = 0x10
	STX  = 0x02
	ETB  = 0x26
	ETX  = 0x03
	SOH  = 0x01
	ENQ  = 0x2D
	SYN  = 0x32
	NAK  = 0x3D
	ACK0 = 0x70
)

// Two-byte line-control sequences, each sent as its own TTR record.
var (
	SOHENQ  = []byte{SOH, ENQ}  // "I want to be primary"
	DLEACK0 = []byte{DLE, ACK0} // positive response
	SYNNAK  = []byte{SYN, NAK}  // "either role is fine"
)

// RCB values (Table 49).
const (
	RCBEOB     = 0x00 // end of block
	RCBRequest = 0x90 // request to initiate stream; SRCB = stream RCB
	RCBPermit  = 0xA0 // permission to initiate stream
	RCBDeny    = 0xB0 // negative permission / receiver cancel
	RCBAckEOF  = 0xC0 // acknowledge transmission complete
	RCBReady   = 0xD0 // ready to receive stream
	RCBBCBErr  = 0xE0 // BCB sequence error; SRCB = expected count
	RCBControl = 0xF0 // NCCR (signon etc.); SRCB = EBCDIC letter
	RCBNMR     = 0x9A // operator command / console message
)

// F0 SRCB letters (EBCDIC).
const (
	SRCBSignoff = 0xC2 // 'B'
	SRCBSignonI = 0xC9 // 'I'
	SRCBSignonJ = 0xD1 // 'J'
	SRCBResetK  = 0xD2 // 'K'
	SRCBConcurL = 0xD3 // 'L'
	SRCBAddM    = 0xD4 // 'M'
	SRCBDelN    = 0xD5 // 'N'
)

// Stream SRCB values (Tables 50, 51).
const (
	SRCBEOF        = 0x00 // end of file on a stream
	SRCBCancel     = 0x40 // transmitter cancel
	SRCBData       = 0x80 // standard record, no carriage control
	SRCBMachineCC  = 0x90 // SYSOUT data, machine carriage control
	SRCBASACC      = 0xA0 // SYSOUT data, ASA carriage control
	SRCBJobHeader  = 0xC0
	SRCBDSHeader   = 0xE0
	SRCBJobTrailer = 0xD0
	SRCBNMR        = 0x80 // JES2 actually sends X'00'; accept both
)

// SysoutRCB returns the RCB for SYSOUT stream n (1-7): 1nnn1001.
func SysoutRCB(n int) byte { return byte(0x89 + n*0x10) }

// SysinRCB returns the RCB for SYSIN stream n (1-7): 1nnn1000.
func SysinRCB(n int) byte { return byte(0x88 + n*0x10) }

// IsSysout reports whether rcb is a SYSOUT stream RCB (99..F9).
func IsSysout(rcb byte) bool { return rcb&0x0F == 0x09 && rcb>>4 >= 9 }

// IsSysin reports whether rcb is a SYSIN stream RCB (98..F8).
func IsSysin(rcb byte) bool { return rcb&0x0F == 0x08 && rcb>>4 >= 9 }

// IsStream reports whether rcb is any SYSIN/SYSOUT stream.
func IsStream(rcb byte) bool { return IsSysout(rcb) || IsSysin(rcb) }

// StreamNumber returns 1-7 for a stream RCB.
func StreamNumber(rcb byte) int { return int(rcb>>4) - 8 }

// IsStreamControl reports whether rcb is one of the stream control RCBs
// (90, A0, B0, C0, D0, E0), whose SRCB names a stream.
func IsStreamControl(rcb byte) bool {
	switch rcb {
	case RCBRequest, RCBPermit, RCBDeny, RCBAckEOF, RCBReady, RCBBCBErr:
		return true
	}
	return false
}

// StreamName describes a stream RCB, e.g. "SYSOUT1" or "NMR".
func StreamName(rcb byte) string {
	switch {
	case IsSysout(rcb):
		return fmt.Sprintf("SYSOUT%d", StreamNumber(rcb))
	case IsSysin(rcb):
		return fmt.Sprintf("SYSIN%d", StreamNumber(rcb))
	case rcb == RCBNMR:
		return "NMR"
	}
	return fmt.Sprintf("RCB%02X", rcb)
}

// FCS is the 2-byte function control sequence (Table 47). A 1 bit means
// the stream is enabled, i.e. the SENDER of the FCS will accept it.
type FCS [2]byte

// FCSNormal enables every stream and the console, no wait-a-bit.
var FCSNormal = FCS{0x8F, 0xCF}

// valid reports whether both "must be 1" bits are set. An invalid FCS is
// treated as all-enabled (tolerant receive).
func (f FCS) valid() bool { return f[0]&0x80 != 0 && f[1]&0x80 != 0 }

// WaitABit reports the "suspend all streams" bit.
func (f FCS) WaitABit() bool { return f.valid() && f[0]&0x40 != 0 }

// Console reports whether the NMR (console) stream is enabled.
func (f FCS) Console() bool { return !f.valid() || f[1]&0x40 != 0 }

// StreamEnabled reports whether stream rcb may be sent. SYSIN n uses bit
// index n-1 and SYSOUT m uses bit index 8-m; index k<4 lives in byte 0
// (mask 0x08>>k), k>=4 in byte 1 (mask 0x08>>(k-4)).
func (f FCS) StreamEnabled(rcb byte) bool {
	if !f.valid() {
		return true
	}
	var k int
	switch {
	case IsSysin(rcb):
		k = StreamNumber(rcb) - 1
	case IsSysout(rcb):
		k = 8 - StreamNumber(rcb)
	case rcb == RCBNMR:
		return f.Console()
	default:
		return true
	}
	if k < 4 {
		return f[0]&(0x08>>k) != 0
	}
	return f[1]&(0x08>>(k-4)) != 0
}

// Record is one logical record in a buffer. For non-F0 records Data is
// the DEcompressed content; for F0 records it is the raw bytes after the
// SRCB (length byte first), which are never compressed.
type Record struct {
	RCB     byte
	SRCB    byte
	Data    []byte
	Aborted bool // record ended with SCB X'40' (stream terminate)
}

// Bytes returns RCB, SRCB and the (decompressed) data, for dumps.
func (r Record) Bytes() []byte {
	return append([]byte{r.RCB, r.SRCB}, r.Data...)
}

// Describe returns a short human-readable name for the record type.
func (r Record) Describe() string {
	switch {
	case r.RCB == RCBControl:
		switch r.SRCB {
		case SRCBSignonI:
			return "signon I"
		case SRCBSignonJ:
			return "signon J"
		case SRCBSignoff:
			return "signoff B"
		}
		return fmt.Sprintf("NCCR %02X", r.SRCB)
	case r.RCB == RCBNMR:
		return "NMR"
	case r.RCB == RCBRequest:
		return "request " + StreamName(r.SRCB)
	case r.RCB == RCBPermit:
		return "permit " + StreamName(r.SRCB)
	case r.RCB == RCBDeny:
		return "deny " + StreamName(r.SRCB)
	case r.RCB == RCBAckEOF:
		return "EOF-ack " + StreamName(r.SRCB)
	case r.RCB == RCBReady:
		return "ready " + StreamName(r.SRCB)
	case r.RCB == RCBBCBErr:
		return "BCB-error"
	case IsStream(r.RCB):
		s := StreamName(r.RCB)
		switch {
		case r.SRCB == SRCBEOF:
			return s + " EOF"
		case r.SRCB == SRCBCancel || r.Aborted:
			return s + " cancel"
		case r.SRCB == SRCBJobHeader:
			return s + " job header"
		case r.SRCB == SRCBDSHeader:
			return s + " dataset header"
		case r.SRCB == SRCBJobTrailer:
			return s + " job trailer"
		}
		return s + " data"
	}
	return fmt.Sprintf("RCB %02X", r.RCB)
}

// Buffer is one transmission buffer.
type Buffer struct {
	BCB     byte
	FCS     FCS
	Records []Record
}

// IsNull reports whether the buffer carries no records (an ACK/keepalive).
func (b *Buffer) IsNull() bool { return len(b.Records) == 0 }

// Encode builds the wire form: DLE STX, BCB, FCS, the records, RCB 00.
// It never sends DLE ETB and never doubles DLEs (PLAN protocol
// decisions; spec 03 section 2.1). EncodeFramed can leave out the DLE
// STX (only as a fallback: every known TCPNJE peer uses it).
//
// A buffer holding a single F0 control record (I/J signon, the other
// NCC records, B signoff) ends at the record: no end-of-buffer RCB 00.
// z/VM RSCS rejects the extra byte ("DMTTNE945E Protocol error ... code
// 1818 -- data being received exceeds expected length"): the accepted I
// in the Sine Nomine NJE / RSCS trace of 2026-11-09 is a 46-byte TTR
// (5 bytes DLE STX BCB FCS + NCCIDL X'29' = 41), RSCS's J is the same,
// and so are NJE38's own I and J. Ours had a 47th byte, X'00'. Data
// and null buffers still end with RCB 00 (Sine Nomine's keepalive is
// "10 02 81 8F CF 00").
//
// Non-F0 records are RCB SRCB followed by SCB-compressed data ending in
// SCB 00. Stream control records with no data therefore become
// "RCB SRCB 00" and EOF becomes "RCB 00 00" (spec 02 AMBIG-1).
func (b *Buffer) Encode() []byte { return b.EncodeFramed(true) }

// EncodeFramed is Encode with (dleSTX) or without the leading DLE STX.
func (b *Buffer) EncodeFramed(dleSTX bool) []byte {
	out := []byte{b.BCB, b.FCS[0], b.FCS[1]}
	if dleSTX {
		out = []byte{DLE, STX, b.BCB, b.FCS[0], b.FCS[1]}
	}
	for _, r := range b.Records {
		out = append(out, r.RCB, r.SRCB)
		if r.RCB == RCBControl {
			out = append(out, r.Data...)
			continue
		}
		if r.Aborted {
			out = append(out, scb.Abort)
			continue
		}
		out = append(out, scb.Compress(r.Data)...)
	}
	if len(b.Records) == 1 && b.Records[0].RCB == RCBControl {
		return out // F0 record alone: the buffer ends at NCCIDL
	}
	return append(out, RCBEOB)
}

// EncodedSize returns how many bytes rec adds to an encoded buffer.
func EncodedSize(r Record) int {
	if r.RCB == RCBControl {
		return 2 + len(r.Data)
	}
	return 2 + len(scb.Compress(r.Data))
}

// ErrShortBuffer is returned for a buffer too short to hold BCB+FCS.
var ErrShortBuffer = errors.New("records: buffer too short")

// Decode parses one transmission buffer. It is tolerant: a leading DLE STX
// is optional, a trailing DLE ETB / DLE ETX is ignored, a missing final
// RCB 00 is accepted. On a parse error the records decoded so far are
// returned together with the error.
func Decode(raw []byte) (*Buffer, error) {
	p := raw
	if len(p) >= 2 && p[0] == DLE && p[1] == STX {
		p = p[2:]
	}
	if len(p) >= 2 && p[len(p)-2] == DLE && (p[len(p)-1] == ETB || p[len(p)-1] == ETX) {
		p = p[:len(p)-2]
	}
	if len(p) < 3 {
		return nil, ErrShortBuffer
	}
	b := &Buffer{BCB: p[0], FCS: FCS{p[1], p[2]}}
	i := 3
	for i < len(p) {
		rcb := p[i]
		if rcb == RCBEOB {
			break
		}
		if i+1 >= len(p) {
			return b, fmt.Errorf("records: RCB %02X without SRCB", rcb)
		}
		r := Record{RCB: rcb, SRCB: p[i+1]}
		i += 2
		switch {
		case rcb == RCBControl:
			r.Data, i = decodeF0(p, i, r.SRCB)
		case IsStreamControl(rcb):
			// Stream control: "RCB SRCB 00", "RCB SRCB" or, for B0 with
			// a reason, "RCB SRCB C2 rc1 rc2 00" (spec 02 AMBIG-1). Only B0
			// carries data, so only B0 may be followed by a literal SCB;
			// for the others a following byte >= X'C0' is the next record's
			// RCB (e.g. "C0 99 C0 A9" without EORs).
			if rcb == RCBDeny && i < len(p) && p[i] >= 0xC0 {
				data, n, _, err := scb.Decompress(p[i:])
				if err != nil {
					return b, fmt.Errorf("records: %s: %v", r.Describe(), err)
				}
				r.Data, i = data, i+n
			} else if i < len(p) && p[i] == scb.EOR {
				i++
			}
		case rcb == RCBNMR || IsStream(rcb):
			if i >= len(p) {
				break // record without SCBs at end of buffer: empty
			}
			if IsStream(rcb) && (r.SRCB == SRCBEOF || r.SRCB == SRCBCancel) {
				// EOF / transmitter cancel: optional EOR (spec 03 11.9).
				if p[i] == scb.EOR || p[i] == scb.Abort {
					i++
				}
				r.Aborted = r.SRCB == SRCBCancel
				break
			}
			data, n, ab, err := scb.Decompress(p[i:])
			if err != nil {
				return b, fmt.Errorf("records: %s: %v", r.Describe(), err)
			}
			r.Data, r.Aborted, i = data, ab, i+n
		default:
			return b, fmt.Errorf("records: undefined RCB %02X", rcb)
		}
		b.Records = append(b.Records, r)
	}
	return b, nil
}

// decodeF0 extracts an uncompressed F0 record body starting at p[i]
// (the length byte, which counts from the RCB). Signoff 'B' has no length
// byte in Table 44, so a following 00 (EOB) or end of buffer yields an
// empty body. A bad length takes the rest of the buffer.
func decodeF0(p []byte, i int, srcb byte) ([]byte, int) {
	if i >= len(p) {
		return nil, i
	}
	n := int(p[i])
	if srcb == SRCBSignoff && n == 0 {
		return nil, i
	}
	if n >= 3 && i-2+n <= len(p) {
		end := i - 2 + n
		return append([]byte(nil), p[i:end]...), end
	}
	end := len(p)
	if end > i && p[end-1] == RCBEOB {
		end--
	}
	return append([]byte(nil), p[i:end]...), len(p)
}

// LineKind classifies a TTR record.
type LineKind int

// TTR record kinds.
const (
	KindBuffer LineKind = iota
	KindSOHENQ
	KindDLEACK0
	KindSYNNAK
	KindNAK
	KindUnknown
)

func (k LineKind) String() string {
	switch k {
	case KindBuffer:
		return "buffer"
	case KindSOHENQ:
		return "SOH ENQ"
	case KindDLEACK0:
		return "DLE ACK0"
	case KindSYNNAK:
		return "SYN NAK"
	case KindNAK:
		return "NAK"
	}
	return "unknown"
}

// Classify tells line-control sequences apart from data buffers. A data
// buffer starts with DLE STX or directly with a BCB (high bit set).
func Classify(rec []byte) LineKind {
	// Some peers (PUBNET, a VM/370 node) end a line-control sequence with
	// BSC pad bytes X'FF': DLE ACK0 arrives as 10 70 FF. Pad bytes are not
	// part of the sequence, so they are ignored here. The trimmed form is
	// used only when it IS one of those sequences, so a data buffer that
	// happens to end in X'FF' is still a buffer.
	if ctl := trimPad(rec); len(ctl) < len(rec) {
		if k := classifyExact(ctl); k == KindSOHENQ || k == KindDLEACK0 ||
			k == KindSYNNAK || k == KindNAK {
			return k
		}
	}
	return classifyExact(rec)
}

// trimPad returns rec without its trailing BSC pad bytes (X'FF').
func trimPad(rec []byte) []byte {
	n := len(rec)
	for n > 0 && rec[n-1] == 0xFF {
		n--
	}
	return rec[:n]
}

// classifyExact classifies rec as it is, pad bytes included.
func classifyExact(rec []byte) LineKind {
	switch {
	case len(rec) == 2 && rec[0] == SOH && rec[1] == ENQ:
		return KindSOHENQ
	case len(rec) == 2 && rec[0] == DLE && rec[1] == ACK0:
		return KindDLEACK0
	case len(rec) == 2 && rec[0] == SYN && rec[1] == NAK:
		return KindSYNNAK
	case len(rec) == 1 && rec[0] == NAK:
		return KindNAK
	case len(rec) >= 2 && rec[0] == DLE && rec[1] == STX:
		return KindBuffer
	case len(rec) >= 3 && rec[0]&0x80 != 0:
		return KindBuffer
	}
	return KindUnknown
}

// Offsets of the two 8-byte password fields (NCCILPAS X'14', NCCINPAS
// X'1C') in an I/J signon record, counted from its RCB.
const (
	signonPwFrom = 0x14
	signonPwTo   = 0x24
)

// SignonMask returns a mask for HexDumpMasked that hides the password
// fields if rec (RCB, SRCB, data...) is an I or J signon, else nil.
func SignonMask(rec []byte) []bool {
	if len(rec) < 2 || rec[0] != RCBControl || (rec[1] != SRCBSignonI && rec[1] != SRCBSignonJ) {
		return nil
	}
	m := make([]bool, len(rec))
	for i := signonPwFrom; i < signonPwTo && i < len(rec); i++ {
		m[i] = true
	}
	return m
}

// BufferMask returns a password mask for a raw transmission buffer (as
// carried in a TTR record): an I/J signon is the only record in its
// buffer, so it sits right after [DLE STX] BCB FCS.
func BufferMask(raw []byte) []bool {
	start := 3
	if len(raw) >= 2 && raw[0] == DLE && raw[1] == STX {
		start = 5
	}
	if len(raw) <= start {
		return nil
	}
	inner := SignonMask(raw[start:])
	if inner == nil {
		return nil
	}
	return append(make([]bool, start), inner...)
}
