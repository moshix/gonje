// Copyright 2026 by moshix

// Package scb implements NJE non-SNA String Control Byte (SCB) compression
// as used on BSC, CTC and TCP/IP links (SA32-0988 Ch.8 Table 48; see
// docs/spec/03_link_protocols.md section 6).
//
// Every non-F0 record in a transmission buffer is a sequence of SCBs, each
// optionally followed by data, ending with the end-of-record SCB X'00':
//
//	00          end of record (EOR)
//	40          terminate stream (transmitter cancel / abort)
//	100bbbbb    insert bbbbb (1-31) EBCDIC blanks; no data byte follows
//	101ddddd    the next byte is repeated ddddd (1-31) times
//	11cccccc    cccccc (1-63) literal bytes follow
//
// An SCB must appear at least every 64 bytes, so literal runs are <= 63.
package scb

import (
	"errors"
	"fmt"
)

// SCB values and masks.
const (
	EOR       = 0x00 // end of record
	Abort     = 0x40 // terminate stream (transmitter cancel)
	blankMask = 0x80 // 100bbbbb
	dupMask   = 0xA0 // 101ddddd
	litMask   = 0xC0 // 11cccccc
	maxRun    = 31   // max count for blank/dup SCBs
	maxLit    = 63   // max literal run
	blank     = 0x40 // EBCDIC blank
)

// MaxRecord is a sanity limit on a decompressed record. NJE segments are at
// most 256 bytes, but we accept larger records to stay tolerant.
const MaxRecord = 32768

// ErrNoEOR is returned when input ends before the end-of-record SCB.
var ErrNoEOR = errors.New("scb: missing end-of-record SCB")

// Compress encodes one record and appends the EOR SCB.
//
// Greedy encoder: runs of >= 2 blanks become a blank SCB (1 byte replaces
// >= 2), runs of >= 3 identical non-blank bytes become a dup SCB (2 bytes
// replace >= 3), everything else is emitted as literal runs of <= 63.
func Compress(rec []byte) []byte {
	out := make([]byte, 0, len(rec)+len(rec)/63+2)
	var lit []byte // pending literal bytes not yet emitted
	flush := func() {
		for len(lit) > 0 {
			n := len(lit)
			if n > maxLit {
				n = maxLit
			}
			out = append(out, litMask|byte(n))
			out = append(out, lit[:n]...)
			lit = lit[n:]
		}
	}
	for i := 0; i < len(rec); {
		c := rec[i]
		run := 1
		for i+run < len(rec) && rec[i+run] == c && run < maxRun {
			run++
		}
		switch {
		case c == blank && run >= 2:
			flush()
			out = append(out, blankMask|byte(run))
			i += run
		case c != blank && run >= 3:
			flush()
			out = append(out, dupMask|byte(run), c)
			i += run
		default:
			lit = append(lit, c)
			i++
			if len(lit) == maxLit {
				flush()
			}
		}
	}
	flush()
	return append(out, EOR)
}

// Decompress decodes one record starting at in[0] (the first SCB, i.e. the
// byte after RCB/SRCB). It returns the record, the number of input bytes
// consumed (including the terminating SCB) and whether the record ended
// with the abort SCB X'40' instead of EOR.
//
// Zero counts (X'80', X'A0', X'C0') are not defined by the manual; they are
// tolerated as no-ops (X'A0' still consumes its data byte).
func Decompress(in []byte) (rec []byte, n int, aborted bool, err error) {
	i := 0
	for {
		if i >= len(in) {
			return rec, i, false, ErrNoEOR
		}
		s := in[i]
		i++
		switch {
		case s == EOR:
			return rec, i, false, nil
		case s == Abort:
			return rec, i, true, nil
		case s&0xC0 == 0x40:
			return rec, i, false, fmt.Errorf("scb: undefined SCB X'%02X' at %d", s, i-1)
		case s&0xE0 == blankMask:
			for k := 0; k < int(s&0x1F); k++ {
				rec = append(rec, blank)
			}
		case s&0xE0 == dupMask:
			if i >= len(in) {
				return rec, i, false, ErrNoEOR
			}
			c := in[i]
			i++
			for k := 0; k < int(s&0x1F); k++ {
				rec = append(rec, c)
			}
		case s&0xC0 == litMask:
			cnt := int(s & 0x3F)
			if i+cnt > len(in) {
				return rec, len(in), false, fmt.Errorf("scb: literal run of %d overflows input", cnt)
			}
			rec = append(rec, in[i:i+cnt]...)
			i += cnt
		default:
			// s in 0x01..0x3F: bits 00xxxxxx other than EOR are undefined.
			return rec, i, false, fmt.Errorf("scb: undefined SCB X'%02X' at %d", s, i-1)
		}
		if len(rec) > MaxRecord {
			return rec, i, false, fmt.Errorf("scb: record exceeds %d bytes", MaxRecord)
		}
	}
}
