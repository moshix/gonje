// Copyright 2026 by moshix

package shell

import "unicode/utf8"

// KeyCode identifies a decoded key.
type KeyCode int

// Keys produced by the decoder. KeyRune carries a printable character;
// KeyCtrl carries a control letter (Ctrl-A = 'a').
const (
	KeyNone KeyCode = iota
	KeyRune
	KeyCtrl
	KeyEnter
	KeyBackspace
	KeyDelete
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyWordLeft  // Alt-B, Ctrl-Left
	KeyWordRight // Alt-F, Ctrl-Right
	KeyEscape
	KeyCtrlUp   // Ctrl-Up: grow the events pane
	KeyCtrlDown // Ctrl-Down: shrink the events pane
)

// Key is one decoded key press.
type Key struct {
	Code KeyCode
	Rune rune
}

// KeyDecoder turns raw terminal bytes into keys. It is a small state
// machine because one key may arrive as several bytes:
//
//	ESC [ A..D / H / F          arrows, Home, End
//	ESC [ n ~                   1/7 Home, 4/8 End, 3 Delete, 5 PgUp, 6 PgDn
//	ESC [ 1 ; 5 C / D           Ctrl-Right / Ctrl-Left
//	ESC [ 1 ; 5 A / B           Ctrl-Up / Ctrl-Down
//	ESC O A..D / H / F          application-mode arrows, Home, End
//	ESC b / ESC f               Alt-B / Alt-F (word left / right)
//	UTF-8 multi-byte sequences  one KeyRune
type KeyDecoder struct {
	seq    []byte
	refeed bool // the last byte ended a lone ESC and must be decoded again
}

// Feed adds one byte and returns the keys it completes (usually zero or
// one; two when a lone ESC is followed by an ordinary key, which is then
// decoded on its own instead of being swallowed).
func (d *KeyDecoder) Feed(b byte) []Key {
	var out []Key
	if k, ok := d.feed1(b); ok {
		out = append(out, k)
	}
	if d.refeed {
		d.refeed = false
		if k, ok := d.feed1(b); ok {
			out = append(out, k)
		}
	}
	return out
}

func (d *KeyDecoder) feed1(b byte) (Key, bool) {
	if len(d.seq) == 0 {
		switch {
		case b == 0x1b:
			d.seq = append(d.seq, b)
			return Key{}, false
		case b == '\r' || b == '\n':
			return Key{Code: KeyEnter}, true
		case b == 0x7f || b == 0x08:
			return Key{Code: KeyBackspace}, true
		case b < 0x20:
			return Key{Code: KeyCtrl, Rune: rune('a' + b - 1)}, true
		case b < 0x80:
			return Key{Code: KeyRune, Rune: rune(b)}, true
		}
		// Start of a UTF-8 sequence.
		d.seq = append(d.seq, b)
		return Key{}, false
	}
	d.seq = append(d.seq, b)
	if d.seq[0] != 0x1b {
		// Collecting UTF-8.
		if utf8.FullRune(d.seq) {
			r, _ := utf8.DecodeRune(d.seq)
			d.seq = d.seq[:0]
			if r == utf8.RuneError {
				return Key{}, false
			}
			return Key{Code: KeyRune, Rune: r}, true
		}
		if len(d.seq) >= utf8.UTFMax {
			d.seq = d.seq[:0]
		}
		return Key{}, false
	}
	// Escape sequences.
	s := d.seq
	if len(s) == 2 {
		switch s[1] {
		case '[', 'O':
			return Key{}, false
		case 'b', 'B':
			d.seq = d.seq[:0]
			return Key{Code: KeyWordLeft}, true
		case 'f', 'F':
			d.seq = d.seq[:0]
			return Key{Code: KeyWordRight}, true
		case 0x1b:
			d.seq = d.seq[:1]
			return Key{Code: KeyEscape}, true
		}
		// ESC followed by an ordinary byte: report the ESC and decode the
		// byte again as a key of its own.
		d.seq = d.seq[:0]
		d.refeed = true
		return Key{Code: KeyEscape}, true
	}
	last := s[len(s)-1]
	// CSI / SS3 sequences end with a byte in 0x40..0x7E.
	if last < 0x40 || last > 0x7e {
		if len(s) > 8 {
			d.seq = d.seq[:0] // runaway sequence: drop it
		}
		return Key{}, false
	}
	params := string(s[2 : len(s)-1])
	d.seq = d.seq[:0]
	switch last {
	case 'A':
		if params == "1;5" || params == "5" {
			return Key{Code: KeyCtrlUp}, true
		}
		return Key{Code: KeyUp}, true
	case 'B':
		if params == "1;5" || params == "5" {
			return Key{Code: KeyCtrlDown}, true
		}
		return Key{Code: KeyDown}, true
	case 'C':
		if params == "1;5" || params == "5" || params == "1;3" {
			return Key{Code: KeyWordRight}, true
		}
		return Key{Code: KeyRight}, true
	case 'D':
		if params == "1;5" || params == "5" || params == "1;3" {
			return Key{Code: KeyWordLeft}, true
		}
		return Key{Code: KeyLeft}, true
	case 'H':
		return Key{Code: KeyHome}, true
	case 'F':
		return Key{Code: KeyEnd}, true
	case '~':
		switch params {
		case "1", "7":
			return Key{Code: KeyHome}, true
		case "4", "8":
			return Key{Code: KeyEnd}, true
		case "3":
			return Key{Code: KeyDelete}, true
		case "5":
			return Key{Code: KeyPgUp}, true
		case "6":
			return Key{Code: KeyPgDn}, true
		}
	}
	return Key{Code: KeyNone}, true
}
