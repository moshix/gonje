// Copyright 2026 by moshix

// Package netdata decodes NETDATA, the format TSO TRANSMIT (and NJE38's
// TRANSMIT) uses to ship a data set as 80-byte card images.
//
// The 80-byte records are just a carrier: concatenated, they form a byte
// stream of segments. Each segment is
//
//	len(1)   segment length, including the length and flags bytes
//	flags(1) X'80' first segment of a logical record
//	         X'40' last segment of a logical record
//	         X'20' control record (INMR01, INMR02, ...)
//	         X'10' record-number record
//	data     len-2 bytes
//
// Segments may cross record boundaries. Control records start with a
// 6-byte EBCDIC id ("INMR01") followed by text units: key(2) count(2)
// then count x [len(2) data]. Everything after INMR06 (and zero padding)
// is ignored.
package netdata

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"gonje/internal/ebcdic"
)

// Segment flags.
const (
	flagFirst   = 0x80
	flagLast    = 0x40
	flagControl = 0x20
	flagRecNum  = 0x10
)

// Text unit keys (TSO/E Customization, "Format of NETDATA").
const (
	KeyDSName  = 0x0002 // INMDSNAM: data set name qualifiers
	KeyTNode   = 0x1001 // INMTNODE: target node
	KeyTUID    = 0x1002 // INMTUID: target user
	KeyFNode   = 0x1011 // INMFNODE: origin node
	KeyFUID    = 0x1012 // INMFUID: origin user
	KeyFTime   = 0x1024 // INMFTIME: origin timestamp
	KeyUtilN   = 0x1028 // INMUTILN: utility (INMCOPY, IEBCOPY)
	KeyDSOrg   = 0x003C // INMDSORG: X'4000' PS, X'0200' PO
	KeyLRECL   = 0x0042 // INMLRECL
	KeyRECFM   = 0x0049 // INMRECFM
	KeySize    = 0x102C // INMSIZE: approximate size in bytes
	KeyNumFile = 0x102F // INMNUMF: number of files
)

// DSORG values.
const (
	DSOrgPS = 0x4000
	DSOrgPO = 0x0200
)

// Control is one control record: id plus its text units.
type Control struct {
	ID    string
	Units map[uint16][][]byte
}

// Str returns a text unit as decoded EBCDIC (values joined by sep).
func (c *Control) Str(key uint16, sep string) string {
	var parts []string
	for _, v := range c.Units[key] {
		parts = append(parts, ebcdic.Decode(v))
	}
	return strings.Join(parts, sep)
}

// Num returns a numeric text unit (big endian, any length up to 8).
func (c *Control) Num(key uint16) uint64 {
	vs := c.Units[key]
	if len(vs) == 0 || len(vs[0]) > 8 {
		return 0
	}
	var n uint64
	for _, b := range vs[0] {
		n = n<<8 | uint64(b)
	}
	return n
}

// File is a decoded NETDATA transmission of one data set.
type File struct {
	Controls []*Control
	DSName   string // dotted data set name
	FromNode string
	FromUser string
	ToNode   string
	ToUser   string
	Time     string // INMFTIME as sent (yyyymmddhhmmss...)
	Utility  string // last INMUTILN (INMCOPY, IEBCOPY ...)
	DSOrg    uint16
	RECFM    uint16
	LRECL    int
	Size     uint64
	Records  [][]byte // logical data records (EBCDIC)
}

// PDS reports a partitioned data set (IEBCOPY unload).
func (f *File) PDS() bool { return f.DSOrg&DSOrgPO != 0 || f.Utility == "IEBCOPY" }

// Fixed reports RECFM F/FB.
func (f *File) Fixed() bool { return f.RECFM&0xC000 == 0x8000 }

// IsNetdata reports whether stream starts with an INMR01 control segment.
func IsNetdata(stream []byte) bool {
	return len(stream) >= 8 && int(stream[0]) >= 8 && stream[1]&flagControl != 0 &&
		ebcdic.Decode(stream[2:8]) == "INMR01"
}

// ErrNotNetdata is returned for a stream without an INMR01 header.
var ErrNotNetdata = errors.New("netdata: no INMR01 header")

// Parse decodes a NETDATA byte stream (the concatenated 80-byte records).
// It never panics on malformed input; it returns an error instead.
func Parse(stream []byte) (*File, error) {
	if !IsNetdata(stream) {
		return nil, ErrNotNetdata
	}
	f := &File{}
	var cur []byte // logical record being assembled
	open := false  // a logical record is in progress
	done := false
	for i := 0; i < len(stream) && !done; {
		l := int(stream[i])
		if l == 0 {
			break // zero padding after the last record
		}
		if l < 2 || i+l > len(stream) {
			return f, fmt.Errorf("netdata: bad segment length %d at offset %d", l, i)
		}
		flags := stream[i+1]
		data := stream[i+2 : i+l]
		i += l
		if flags&flagFirst != 0 {
			cur, open = nil, true
		} else if !open {
			return f, fmt.Errorf("netdata: continuation segment without a first segment at offset %d", i-l)
		}
		cur = append(cur, data...)
		if flags&flagLast == 0 {
			continue
		}
		open = false
		switch {
		case flags&flagControl != 0:
			c, err := parseControl(cur)
			if err != nil {
				return f, err
			}
			f.Controls = append(f.Controls, c)
			f.apply(c)
			done = c.ID == "INMR06"
		case flags&flagRecNum != 0:
			// Record-number records carry no data.
		default:
			f.Records = append(f.Records, append([]byte(nil), cur...))
		}
	}
	if open {
		return f, errors.New("netdata: truncated (logical record not finished)")
	}
	if len(f.Controls) == 0 || f.Controls[0].ID != "INMR01" {
		return f, ErrNotNetdata
	}
	return f, nil
}

// parseControl splits a control record into its id and text units.
func parseControl(rec []byte) (*Control, error) {
	if len(rec) < 6 {
		return nil, fmt.Errorf("netdata: control record too short (%d)", len(rec))
	}
	c := &Control{ID: ebcdic.Decode(rec[:6]), Units: map[uint16][][]byte{}}
	if !strings.HasPrefix(c.ID, "INMR") {
		return nil, fmt.Errorf("netdata: unknown control record %q", c.ID)
	}
	p := rec[6:]
	if c.ID == "INMR02" && len(p) >= 4 {
		p = p[4:] // INMR02 starts with a 4-byte file number
	}
	for len(p) >= 4 {
		key := binary.BigEndian.Uint16(p[0:2])
		count := int(binary.BigEndian.Uint16(p[2:4]))
		p = p[4:]
		var vals [][]byte
		for k := 0; k < count; k++ {
			if len(p) < 2 {
				return nil, fmt.Errorf("netdata: %s: text unit %04X truncated", c.ID, key)
			}
			n := int(binary.BigEndian.Uint16(p[0:2]))
			if 2+n > len(p) {
				return nil, fmt.Errorf("netdata: %s: text unit %04X value overflows", c.ID, key)
			}
			vals = append(vals, append([]byte(nil), p[2:2+n]...))
			p = p[2+n:]
		}
		c.Units[key] = vals
	}
	return c, nil
}

// apply takes the fields gonje uses from a control record.
func (f *File) apply(c *Control) {
	switch c.ID {
	case "INMR01":
		f.FromNode, f.FromUser = c.Str(KeyFNode, ""), c.Str(KeyFUID, "")
		f.ToNode, f.ToUser = c.Str(KeyTNode, ""), c.Str(KeyTUID, "")
		f.Time = c.Str(KeyFTime, "")
	case "INMR02":
		// Data set attributes come from INMR02. INMR03 describes the
		// transmission records themselves (e.g. RECFM X'0001'), not the
		// data set, so it is not used here.
		if v := c.Str(KeyUtilN, ""); v != "" {
			f.Utility = v
		}
		if v := c.Str(KeyDSName, "."); v != "" {
			f.DSName = v
		}
		if v := c.Num(KeyDSOrg); v != 0 {
			f.DSOrg = uint16(v)
		}
		if v := c.Num(KeyRECFM); v != 0 {
			f.RECFM = uint16(v)
		}
		if v := c.Num(KeyLRECL); v != 0 {
			f.LRECL = int(v)
		}
		if v := c.Num(KeySize); v != 0 {
			f.Size = v
		}
	}
}

// StripSequenceNumbers removes columns 73-80 from fixed 80-byte card
// images when every record has 8 digits there (ISPF/RFE line numbers).
func StripSequenceNumbers(recs [][]byte) [][]byte {
	if len(recs) == 0 {
		return recs
	}
	for _, r := range recs {
		if len(r) != 80 || !isDigits(r[72:80]) {
			return recs
		}
	}
	out := make([][]byte, len(recs))
	for i, r := range recs {
		out[i] = r[:72]
	}
	return out
}

func isDigits(e []byte) bool {
	for _, b := range e {
		if b < 0xF0 || b > 0xF9 { // EBCDIC '0'..'9'
			return false
		}
	}
	return true
}

// RecfmName describes INMRECFM: F/V/U plus B (blocked).
func RecfmName(r uint16) string {
	var s string
	switch r & 0xC000 {
	case 0x8000:
		s = "F"
	case 0x4000:
		s = "V"
	case 0xC000:
		s = "U"
	default:
		s = "?"
	}
	if r&0x1000 != 0 {
		s += "B"
	}
	return s
}

// DSOrgName names INMDSORG.
func DSOrgName(d uint16) string {
	switch {
	case d&DSOrgPO != 0:
		return "PO"
	case d&DSOrgPS != 0:
		return "PS"
	}
	return fmt.Sprintf("%04X", d)
}

// Join concatenates records (helper for callers holding [][]byte).
func Join(recs [][]byte) []byte { return bytes.Join(recs, nil) }
