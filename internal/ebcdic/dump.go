// Copyright 2026 by moshix

package ebcdic

import "strings"

// DumpWidth is the width of every line produced by HexDump:
// 4 offset + 2 + 23 + 2 + 23 + 2 + 16 = 72 columns.
const DumpWidth = 72

const hexDigits = "0123456789ABCDEF"

// HexDump formats data 16 bytes per line in the gonje dump format:
//
//	0000  E3 C5 E2 E3 40 D4 C5 E2  E2 C1 C7 C5 00 00 00 00  TEST MESSAGE....
//
// The right column shows the EBCDIC-decoded printable character for each
// byte, or '.' when there is none. A short final line is padded so the
// character column stays aligned. Offsets wrap at 0xFFFF (records are
// never that large).
func HexDump(data []byte) []string { return HexDumpMasked(data, nil) }

// HexDumpMasked is HexDump with secrets hidden: every byte whose index
// has mask[i] set is shown as "XX" in the hex part and '*' in the
// character column (used for signon passwords). mask may be shorter than
// data or nil.
func HexDumpMasked(data []byte, mask []bool) []string {
	var lines []string
	for off := 0; off < len(data); off += 16 {
		end := off + 16
		if end > len(data) {
			end = len(data)
		}
		var m []bool
		if off < len(mask) {
			m = mask[off:min(end, len(mask))]
		}
		lines = append(lines, dumpLine(off, data[off:end], m))
	}
	return lines
}

func dumpLine(off int, chunk []byte, mask []bool) string {
	hidden := func(i int) bool { return i < len(mask) && mask[i] }
	var sb strings.Builder
	sb.Grow(DumpWidth)
	sb.WriteByte(hexDigits[(off>>12)&0xF])
	sb.WriteByte(hexDigits[(off>>8)&0xF])
	sb.WriteByte(hexDigits[(off>>4)&0xF])
	sb.WriteByte(hexDigits[off&0xF])
	sb.WriteString("  ")
	for i := 0; i < 16; i++ {
		// Column layout: two groups of 8 bytes separated by an extra blank.
		if i == 8 {
			sb.WriteByte(' ')
		}
		if i < len(chunk) && hidden(i) {
			sb.WriteString("XX")
		} else if i < len(chunk) {
			sb.WriteByte(hexDigits[chunk[i]>>4])
			sb.WriteByte(hexDigits[chunk[i]&0xF])
		} else {
			sb.WriteString("  ")
		}
		if i != 15 {
			sb.WriteByte(' ')
		}
	}
	sb.WriteString("  ")
	for i, b := range chunk {
		if hidden(i) {
			sb.WriteByte('*')
			continue
		}
		sb.WriteByte(Printable(b))
	}
	return sb.String()
}
