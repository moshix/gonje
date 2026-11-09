package tcpip

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// Framing sizes (figs 61, 63, 64).
const (
	TTBLen = 8 // block header: F(1) U(1) LN(2) reserved(4)
	TTRLen = 4 // record header: F(1) U(1) LN(2)
	// MaxBlock is the largest TTB we build; the manual gives 8192 as the
	// TCP send/receive buffer size. Inbound blocks up to 64K are accepted.
	MaxBlock = 8192
)

// EncodeBlock wraps records in one TTB block:
//
//	TTB | TTR rec | TTR rec | ... | TTR(EOB)
//
// TTB.LN is the total block length including the TTB itself and the
// final 4-byte all-zero EOB TTR. TTR.LN is the length of the record that
// follows, excluding the TTR (spec 01 section 3.4, ICF for the TTR).
func EncodeBlock(recs ...[]byte) ([]byte, error) {
	total := TTBLen + TTRLen
	for _, r := range recs {
		if len(r) == 0 || len(r) > 0xFFFF {
			return nil, fmt.Errorf("tcpip: bad record length %d", len(r))
		}
		total += TTRLen + len(r)
	}
	if total > 0xFFFF {
		return nil, fmt.Errorf("tcpip: block too large (%d)", total)
	}
	b := make([]byte, 0, total)
	b = append(b, 0, 0, byte(total>>8), byte(total), 0, 0, 0, 0)
	for _, r := range recs {
		b = append(b, 0, 0, byte(len(r)>>8), byte(len(r)))
		b = append(b, r...)
	}
	b = append(b, 0, 0, 0, 0) // EOB TTR
	return b, nil
}

// WriteBlock encodes and writes one block, returning the raw bytes sent.
func WriteBlock(w io.Writer, recs ...[]byte) ([]byte, error) {
	b, err := EncodeBlock(recs...)
	if err != nil {
		return nil, err
	}
	_, err = w.Write(b)
	return b, err
}

// ReadBlock reads one TTB block from br and returns its records plus the
// raw block bytes (for logging).
//
// TCP is a byte stream, so a block may arrive in many pieces; bufio and
// io.ReadFull hide the short reads. The reader is tolerant of senders
// whose TTB.LN is slightly off: records are read TTR by TTR until the EOB
// TTR (LN=0). If TTB.LN is exhausted without an EOB TTR, we peek at
// already-buffered input: four zero bytes are consumed as the EOB TTR (a real TTB always has a non-zero
// LN, so this cannot swallow the next block). If an EOB TTR arrives before
// TTB.LN is exhausted, the remainder is read and discarded.
// A block with no records at all (VSE/POWER "empty buffer" keepalive) is
// returned with zero records.
func ReadBlock(br *bufio.Reader) (recs [][]byte, raw []byte, err error) {
	hdr := make([]byte, TTBLen)
	if _, err = io.ReadFull(br, hdr); err != nil {
		return nil, nil, err
	}
	raw = append(raw, hdr...)
	ln := int(binary.BigEndian.Uint16(hdr[2:4]))
	if ln < TTBLen {
		return nil, raw, fmt.Errorf("tcpip: TTB length %d too small", ln)
	}
	remaining := ln - TTBLen
	for {
		if remaining <= 0 {
			// TTB.LN used up; swallow a trailing EOB TTR if one follows.
			// Only look at bytes that already arrived: blocking here would
			// hold this block until the peer sends again (and a peer that
			// waits for our answer never would).
			if br.Buffered() < TTRLen {
				return recs, raw, nil
			}
			if p, perr := br.Peek(TTRLen); perr == nil && isZero(p) {
				_, _ = br.Discard(TTRLen)
				raw = append(raw, p...)
			}
			return recs, raw, nil
		}
		ttr := make([]byte, TTRLen)
		if remaining < TTRLen {
			// Fragment shorter than a TTR: discard it.
			junk := make([]byte, remaining)
			if _, err = io.ReadFull(br, junk); err != nil {
				return recs, raw, err
			}
			raw = append(raw, junk...)
			return recs, raw, nil
		}
		if _, err = io.ReadFull(br, ttr); err != nil {
			return recs, raw, err
		}
		raw = append(raw, ttr...)
		remaining -= TTRLen
		rl := int(binary.BigEndian.Uint16(ttr[2:4]))
		if rl == 0 {
			// EOB TTR. Discard anything TTB.LN still claims.
			if remaining > 0 {
				junk := make([]byte, remaining)
				if _, err = io.ReadFull(br, junk); err != nil {
					return recs, raw, err
				}
				raw = append(raw, junk...)
			}
			return recs, raw, nil
		}
		rec := make([]byte, rl)
		if _, err = io.ReadFull(br, rec); err != nil {
			return recs, raw, err
		}
		raw = append(raw, rec...)
		remaining -= rl
		recs = append(recs, rec)
	}
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
