package records

import (
	"fmt"
	"strings"

	"gonje/internal/ebcdic"
)

// NMR header size and limits (docs/spec/05 section 6).
const (
	NMRHeaderLen  = 30  // text starts at NMRMSG = X'1E'
	NMRMaxText    = 132 // max message/command text
	NMRMaxRecord  = 256
	nmrTimestamp  = 8
	nmrSenderUser = 8
)

// NMRFLAG bits.
const (
	NMRFlagCommand = 0x80 // 1 = command, 0 = message
	NMRFlagRemote  = 0x40 // NMROUT = remote workstation
	NMRFlagUser    = 0x20 // NMROUT = userid
	NMRFlagConsole = 0x10 // NMROUT = console id
	nmrRouteMask   = NMRFlagRemote | NMRFlagUser | NMRFlagConsole
)

// NMRTYPE bits.
const (
	NMRTypeSender    = 0x08 // message: sending userid included in NMRMSG
	NMRTypeNoTime    = 0x04 // message: NO timestamp in NMRMSG
	NMRTypeFormatted = 0x02 // command: formatted
)

// NMR levels: high nibble = importance, low nibble = priority (always 7).
const (
	NMRLevelCommand  = 0x77
	NMRLevelMessage  = 0x77 // essential (notify)
	NMRLevelResponse = 0x37 // normal: what RSCS uses for command responses
)

// NMR is a nodal message record:
//
//	00 NMRFLAG | 01 NMRLEVEL/PRIO | 02 NMRTYPE | 03 NMRML
//	04 NMRTONOD(8) | 0C NMRTOQUL | 0D NMROUT(8) | 15 NMRFMNOD(8)
//	1D NMRFMQUL | 1E NMRMSG...
type NMR struct {
	Flag     byte
	Level    byte
	Type     byte
	ML       byte
	ToNode   string
	ToQual   byte
	Out      [8]byte
	FromNode string
	FromQual byte
	Msg      []byte // raw NMRMSG bytes
}

// Encode builds the NMR record body (sent with RCB 9A, SRCB 80).
func (m *NMR) Encode() []byte {
	b := make([]byte, NMRHeaderLen, NMRHeaderLen+len(m.Msg))
	b[0], b[1], b[2], b[3] = m.Flag, m.Level, m.Type, m.ML
	ebcdic.PutField(b[4:12], m.ToNode)
	b[12] = m.ToQual
	copy(b[13:21], m.Out[:])
	ebcdic.PutField(b[21:29], m.FromNode)
	b[29] = m.FromQual
	return append(b, m.Msg...)
}

// Record wraps the NMR as a buffer record.
func (m *NMR) Record() Record { return Record{RCB: RCBNMR, SRCB: SRCBNMR, Data: m.Encode()} }

// ParseNMR decodes an NMR record body.
func ParseNMR(b []byte) (*NMR, error) {
	if len(b) < NMRHeaderLen {
		return nil, fmt.Errorf("records: NMR too short (%d)", len(b))
	}
	m := &NMR{
		Flag: b[0], Level: b[1], Type: b[2], ML: b[3],
		ToNode:   ebcdic.FieldString(b[4:12]),
		ToQual:   b[12],
		FromNode: ebcdic.FieldString(b[21:29]),
		FromQual: b[29],
		Msg:      append([]byte(nil), b[NMRHeaderLen:]...),
	}
	copy(m.Out[:], b[13:21])
	return m, nil
}

// IsCommand reports whether the NMR carries a command.
func (m *NMR) IsCommand() bool { return m.Flag&NMRFlagCommand != 0 }

// IsFormatted reports a formatted (JES2 $G) command.
func (m *NMR) IsFormatted() bool { return m.IsCommand() && m.Type&NMRTypeFormatted != 0 }

// IsResponse guesses whether a message is a command response: RSCS and
// gonje send responses at importance level 3 (X'3x').
func (m *NMR) IsResponse() bool { return !m.IsCommand() && m.Level>>4 == 3 }

// OutUser returns NMROUT as a user/remote name when the W or T bit says it
// holds one, else "".
func (m *NMR) OutUser() string {
	if m.Flag&(NMRFlagUser|NMRFlagRemote) != 0 {
		return ebcdic.FieldString(m.Out[:])
	}
	return ""
}

// clamp returns b[from:to] limited to the slice bounds.
func clamp(b []byte, from, to int) []byte {
	if from > len(b) {
		from = len(b)
	}
	if to > len(b) || to < from {
		to = len(b)
	}
	return b[from:to]
}

// Text returns the decoded command or message text.
//
// Message NMRMSG layout depends on NMRTYPE (Table 37):
//
//	TYPE4=0 TYPET=0: timestamp(8) text
//	TYPE4=0 TYPET=1: text
//	TYPE4=1 TYPET=0: timestamp(8) userid(8) text
//	TYPE4=1 TYPET=1: userid(8) text
//
// NMRML counts the userid but not the timestamp. We clamp to the record
// length and, if NMRML is zero, take the rest of the record.
func (m *NMR) Text() string {
	if m.IsCommand() {
		n := int(m.ML)
		if n == 0 {
			n = len(m.Msg)
		}
		return strings.TrimRight(ebcdic.Decode(clamp(m.Msg, 0, n)), " \x00")
	}
	p := 0
	if m.Type&NMRTypeNoTime == 0 {
		p = nmrTimestamp
	}
	n := int(m.ML)
	if m.Type&NMRTypeSender != 0 {
		p += nmrSenderUser
		n -= nmrSenderUser
	}
	end := p + n
	if n <= 0 {
		end = len(m.Msg)
	}
	return strings.TrimRight(ebcdic.Decode(clamp(m.Msg, p, end)), " \x00")
}

// Sender returns the originating userid carried in a message NMRMSG
// (NMRECSID, present when NMRTYPE4 is set), or for a command the
// originating user in NMROUT.
func (m *NMR) Sender() string {
	if m.IsCommand() {
		return m.OutUser()
	}
	if m.Type&NMRTypeSender == 0 {
		return ""
	}
	p := 0
	if m.Type&NMRTypeNoTime == 0 {
		p = nmrTimestamp
	}
	return ebcdic.FieldString(clamp(m.Msg, p, p+nmrSenderUser))
}

// splitText cuts text into pieces of at most NMRMaxText bytes.
func splitText(text string, max int) []string {
	enc := []rune(text)
	var out []string
	for len(enc) > max {
		out = append(out, string(enc[:max]))
		enc = enc[max:]
	}
	return append(out, string(enc))
}

// NewMessages builds message NMRs from user sender at node from to user
// toUser at node toNode. Text longer than 132 bytes is split over several
// NMRs. toUser "" or "*" addresses the operator console instead of a user.
//
// For user messages we set NMRFLAGT (NMROUT = destination userid) and
// NMRTYPE = X'0C' (sender userid included, no timestamp), per spec 05
// section 8.a.
func NewMessages(from, sender, toNode, toUser, text string) []*NMR {
	var out []*NMR
	for _, piece := range splitText(text, NMRMaxText-nmrSenderUser) {
		m := &NMR{Level: NMRLevelMessage, ToNode: toNode, FromNode: from}
		if toUser == "" || toUser == "*" {
			// Operator console: NMRDESC=0, NMRROUT=X'4100', NMRCONID=0.
			m.Out = [8]byte{0, 0, 0x41, 0, 0, 0, 0, 0}
		} else {
			m.Flag = NMRFlagUser
			copy(m.Out[:], ebcdic.Field(toUser, 8))
		}
		m.Type = NMRTypeSender | NMRTypeNoTime
		m.Msg = append(ebcdic.Field(sender, 8), ebcdic.Encode(piece)...)
		m.ML = byte(len(m.Msg))
		out = append(out, m)
	}
	return out
}

// NewCommand builds an unformatted command NMR (spec 05 section 8.b):
// NMRFLAG = command + NMROUT holds the originating userid.
func NewCommand(from, origUser, toNode, text string) (*NMR, error) {
	enc := ebcdic.Encode(text)
	if len(enc) == 0 || len(enc) > NMRMaxText {
		return nil, fmt.Errorf("command must be 1-%d characters", NMRMaxText)
	}
	m := &NMR{Flag: NMRFlagCommand | NMRFlagUser, Level: NMRLevelCommand,
		ML: byte(len(enc)), ToNode: toNode, FromNode: from, Msg: enc}
	copy(m.Out[:], ebcdic.Field(origUser, 8))
	return m, nil
}

// NewResponse builds one response message NMR for a received command:
// it goes back to the command's origin node with the same NMROUT and
// routing bits (W/T/U), so the origin can route it to the requester.
//
// RSCS does not set NMRFLAGT on commands but still puts the originating
// userid in NMROUT (Table 35). A reply with W/T/U all zero would make
// the receiver read NMROUT as MCS routing codes and send the reply to the
// console (Table 34), so when the command had no routing bit but NMROUT
// holds a name, the reply gets NMRFLAGT (NMROUT = destination userid).
func NewResponse(req *NMR, from, text string) *NMR {
	enc := ebcdic.Encode(text)
	if len(enc) > NMRMaxText {
		enc = enc[:NMRMaxText]
	}
	flag := req.Flag & nmrRouteMask
	if flag == 0 && isNameField(req.Out[:]) {
		flag = NMRFlagUser
	}
	return &NMR{
		Flag:     flag,
		Level:    NMRLevelResponse,
		Type:     NMRTypeNoTime,
		ML:       byte(len(enc)),
		ToNode:   req.FromNode,
		ToQual:   req.FromQual,
		Out:      req.Out,
		FromNode: from,
		Msg:      enc,
	}
}

// isNameField reports whether f holds an EBCDIC name: A-Z, 0-9, @ # $
// followed by blank padding, with at least one character.
func isNameField(f []byte) bool {
	name := ebcdic.Decode(f)
	trimmed := strings.TrimRight(name, " ")
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '@' || r == '#' || r == '$'
		if !ok {
			return false
		}
	}
	return true
}
