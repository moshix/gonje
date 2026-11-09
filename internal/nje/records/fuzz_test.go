// Copyright 2026 by moshix

package records

import "testing"

// FuzzDecode feeds arbitrary buffers to Decode and every parser it feeds
// (adopted from the code review). `go test` runs the seed corpus; use
// `go test -fuzz=FuzzDecode ./internal/nje/records` to fuzz for real.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{0x10, 0x02, 0x80, 0x8F, 0xCF, 0x99, 0x80, 0xC1, 0xC1, 0x00, 0x00})
	f.Add([]byte{0x80, 0x8F, 0xCF, 0xF0, 0xC9, 0x29})
	f.Add([]byte{0x80, 0x8F, 0xCF, 0xC0, 0x99, 0xC0, 0xA9})
	f.Add([]byte{0x80, 0x8F, 0xCF, 0xB0, 0x99, 0xC2, 0x10, 0x04, 0x00, 0x00})
	f.Add((&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{(&Signon{SRCB: SRCBSignonI, Node: "A"}).Record()}}).Encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		_ = BufferMask(b)
		buf, _ := Decode(b)
		if buf == nil {
			return
		}
		for _, r := range buf.Records {
			_ = SignonMask(r.Bytes())
			if r.RCB == RCBControl {
				_, _ = ParseSignon(r)
			}
			_, _ = ParseJobHeader(r.Data)
			_, _ = ParseDatasetHeader(r.Data)
			_, _ = ParseJobTrailer(r.Data)
			var ra Reassembler
			_, _ = ra.Add(r.Data)
			if m, err := ParseNMR(r.Data); err == nil {
				_ = m.Text()
				_ = m.Sender()
				_ = NewResponse(m, "X", m.Text())
			}
		}
	})
}
