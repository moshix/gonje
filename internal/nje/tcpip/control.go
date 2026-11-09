// Package tcpip implements the NJE-over-TCP/IP transport layer
// (SA32-0988 Chapter 12, docs/spec/01_intro_tcpip.md section 3):
// the 33-byte OPEN/ACK/NAK control records exchanged right after the TCP
// connect, and the TTB/TTR block framing used for everything afterwards.
package tcpip

import (
	"errors"
	"fmt"
	"io"
	"net"

	"gonje/internal/ebcdic"
)

// ControlLen is the size of an IPv4 control record (fig 59).
const ControlLen = 33

// Control record types (8 byte EBCDIC, blank padded).
const (
	TypeOpen = "OPEN"
	TypeACK  = "ACK"
	TypeNAK  = "NAK"
)

// NAK reason codes (spec 01 section 3.1).
const (
	NAKNoSuchLink  byte = 0x01 // no such link / not expected: do not retry
	NAKActive      byte = 0x02 // link already active: retry later
	NAKCollision   byte = 0x03 // link is itself doing an active open: retry
	NAKTemporary   byte = 0x04 // temporary condition: retry
	controlTypeLen      = 8
)

// Control is an OPEN, ACK or NAK record.
//
// Layout (all EBCDIC / big endian):
//
//	off 0  len 8  TYPE   'OPEN    ' / 'ACK     ' / 'NAK     '
//	off 8  len 8  RHOST  node name of the record's SENDER
//	off 16 len 4  RIP    sender IPv4 address
//	off 20 len 8  OHOST  node name of the intended RECEIVER
//	off 28 len 4  OIP    receiver IPv4 address
//	off 32 len 1  R      NAK reason (0 for OPEN/ACK)
//
// For ACK/NAK we follow the IPv6 description (spec 01 section 3.2, ICF):
// RHOST/RIP describe the ACK sender and OHOST/OIP the OPEN sender.
type Control struct {
	Type   string
	RHost  string
	RIP    net.IP
	OHost  string
	OIP    net.IP
	Reason byte
}

// ErrBadControl is returned for a malformed control record.
var ErrBadControl = errors.New("tcpip: malformed control record")

// Marshal encodes the record into its 33-byte wire form.
func (c *Control) Marshal() []byte {
	b := make([]byte, ControlLen)
	ebcdic.PutField(b[0:8], c.Type)
	ebcdic.PutField(b[8:16], c.RHost)
	copy(b[16:20], ipv4(c.RIP))
	ebcdic.PutField(b[20:28], c.OHost)
	copy(b[28:32], ipv4(c.OIP))
	b[32] = c.Reason
	return b
}

func ipv4(ip net.IP) []byte {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return make([]byte, 4)
}

// UnmarshalControl decodes a 33-byte control record. Unknown TYPE values
// (including the IPv6 'OPENNJE ' form and 'OPEN SSL', neither of which
// gonje supports) are reported as ErrBadControl with the type text.
func UnmarshalControl(b []byte) (*Control, error) {
	if len(b) != ControlLen {
		return nil, ErrBadControl
	}
	typ := ebcdic.FieldString(b[0:controlTypeLen])
	switch typ {
	case TypeOpen, TypeACK, TypeNAK:
	default:
		return nil, fmt.Errorf("%w: type %q", ErrBadControl, typ)
	}
	return &Control{
		Type:   typ,
		RHost:  ebcdic.FieldString(b[8:16]),
		RIP:    net.IP(append([]byte(nil), b[16:20]...)),
		OHost:  ebcdic.FieldString(b[20:28]),
		OIP:    net.IP(append([]byte(nil), b[28:32]...)),
		Reason: b[32],
	}, nil
}

// ReadControl reads exactly one control record from r (short reads are
// handled by io.ReadFull). It also returns the raw bytes for logging.
func ReadControl(r io.Reader) (*Control, []byte, error) {
	b := make([]byte, ControlLen)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, nil, err
	}
	c, err := UnmarshalControl(b)
	return c, b, err
}

// String returns a one-line description for logs and panels.
func (c *Control) String() string {
	s := fmt.Sprintf("%s RHOST=%s RIP=%s OHOST=%s OIP=%s", c.Type, c.RHost, c.RIP, c.OHost, c.OIP)
	if c.Type == TypeNAK {
		s += fmt.Sprintf(" R=%02X (%s)", c.Reason, NAKReasonText(c.Reason))
	}
	return s
}

// NAKReasonText describes a NAK reason code.
func NAKReasonText(r byte) string {
	switch r {
	case NAKNoSuchLink:
		return "no such link"
	case NAKActive:
		return "link already active"
	case NAKCollision:
		return "connect collision"
	case NAKTemporary:
		return "temporary condition"
	}
	return "unknown reason"
}

// NAKRetryable reports whether a NAK reason allows a retry after a random
// delay (X'02'-X'04'); X'01' means stop.
func NAKRetryable(r byte) bool { return r != NAKNoSuchLink }
