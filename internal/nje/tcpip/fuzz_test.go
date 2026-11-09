package tcpip

import (
	"bufio"
	"bytes"
	"testing"
)

// FuzzReadBlock reads up to 10 blocks from arbitrary bytes (adopted from
// the code review). `go test` runs the seeds; add -fuzz to fuzz.
func FuzzReadBlock(f *testing.F) {
	f.Add([]byte{0, 0, 0, 18, 0, 0, 0, 0, 0, 0, 0, 2, 1, 0x2d, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 8, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 14, 0, 0, 0, 0, 0, 0, 0, 2, 0x10, 0x70, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		br := bufio.NewReader(bytes.NewReader(b))
		for i := 0; i < 10; i++ {
			if _, _, err := ReadBlock(br); err != nil {
				return
			}
		}
	})
}

func FuzzUnmarshalControl(f *testing.F) {
	f.Add((&Control{Type: TypeOpen, RHost: "A", OHost: "B"}).Marshal())
	f.Fuzz(func(t *testing.T, b []byte) {
		if c, err := UnmarshalControl(b); err == nil {
			_ = c.String()
			_ = c.Marshal()
		}
	})
}
