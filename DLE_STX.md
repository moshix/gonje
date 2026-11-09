# NJE over TCP/IP signon framing: DLE STX and the end-of-buffer byte

A correction and porting guide for `../tsu/` (Go). Everything here
comes from gonje 0.9 (`/Users/moshe/gonje`); paths are relative to that
tree.

## 1. Summary

- **All known TCPNJE peers use DLE STX.** Hercules TCPNJE (NJE38 /
  HOUCICS), z/VM RSCS TCPNJE (ZVM72MSH) and Sine Nomine's NJE all start
  every transmission buffer inside a TTR record with `10 02` (DLE STX),
  then BCB, FCS. Keep sending it.
- **A buffer that holds only an F0 control record ends at that record.**
  The signon I and J (and the other NCC records, and the B signoff) are
  *not* followed by the end-of-buffer RCB `00`. Data buffers and null
  buffers do end with RCB `00`.
- z/VM RSCS enforces this. An I with a trailing `00` (47-byte TTR
  instead of 46) gets
  `DMTTNE945E Protocol error detected by link <link>, code 1818 -- data being received exceeds expected length`
  and an immediate reset. NJE38 only tolerated the extra byte.
- Receive both forms (with and without the trailing `00`, with and
  without DLE STX).

## 2. Evidence

### 2.1 Sine Nomine NJE signing on to z/VM RSCS (accepted)

TTB/TTR are the usual TCPNJE wrappers (TTB: flags, 00, length(2),
reserved(4); TTR: flags, 00, length(2), data; a TTR of length 0 ends
the TTB). RSCS's TTB/TTR conventions are identical to Hercules' and
gonje's.

Sine Nomine's I (TTB length X'3E', TTR length X'2E' = 46):

    10 02 | 80 | 8F CF | F0 C9 | 29 | D9 C5 D3 C1 E8 40 40 40 | 01 | 00 00 00 00 | 00 00 | 20 00
    DLE STX BCB  FCS     RCB SRCB NCCIDL  NCCINODE "RELAY"      QUAL  NCCIEVNT      REST    BUFSZ 8192
    40 x 16 (line and node password, blank) | 00 | 15 00 00 00
                                              FLG  NCCIFEAT
    -- end of TTR: no 00 after the record

RSCS's J back (TTR X'2E' = 46):

    10 02 80 8F CF F0 D1 29 E9 E5 D4 F7 F2 D4 E2 C8 01 FF FF FF FF 00 00 10 00
    40 x 16 00 14 00 00 00
    -- no trailing 00; NCCIEVNT X'FFFFFFFF', buffer size 4096, NCCIFEAT X'14'

Then RSCS's first data buffer, BCB X'81', carries an NMR:
`DMTNCR905I Signon of link RELAY complete, buffer size=4096`.

Sine Nomine's keepalive, a null buffer, ends with the RCB `00`:

    10 02 81 8F CF 00

Byte count: NCCIDL X'29' = 41 counts the record from its RCB (RCB,
SRCB, NCCIDL itself and 38 more bytes). 41 + 5 bytes of framing
(DLE STX, BCB, FCS(2)) = 46 = the TTR length. Nothing follows.

NJE38's own I and J (HOUCICS traces) are 46-byte TTRs as well.

### 2.2 gonje's rejected I (before the fix)

    TTR X'2F' = 47:  10 02 80 8F CF F0 C9 29 ... 00 00 00 00 00 | 00
                                                      features   ^ extra RCB 00

RSCS reads the 41-byte record, then finds one more byte where the TTR
should have ended: "data being received exceeds expected length", code
1818, reset 40 ms later.

### 2.3 Why the attempt without DLE STX gave 916E instead

RSCS expects DLE STX in front of the BCB. Sent without `10 02`, every
byte of our I sits two places earlier than RSCS expects, so RSCS does
not find a valid signon record where it looks for one and reports
`DMTTNE916E Invalid NJE signon connection record`. The different
message comes from the shifted bytes, not from a different rule.

### 2.4 What we got wrong first

We first read the 1818 as "RSCS does not use DLE STX": parsed without
it, `10 02 80 8F CF F0 ...` gives BCB=10 FCS=0280 RCB=8F SRCB=CF and an
SCB F0 that promises 48 bytes, which also "exceeds expected length".
That theory was plausible but wrong. The Sine Nomine trace shows RSCS
sending and accepting DLE STX. gonje 0.8 shipped a heuristic that sent
no DLE STX to RSCS-like peers (SYN NAK, OPEN with RIP 0.0.0.0); 0.9
removed it.

## 3. What to implement

### 3.1 Encoding (required)

    buffer = DLE STX, BCB, FCS1, FCS2
    for each record:
        F0:  RCB SRCB <data as is>          (NCCIDL counts from the RCB)
        else RCB SRCB <SCB-compressed data> (ends with SCB 00)
    if the buffer holds exactly one F0 record: stop here
    else: append RCB 00 (end of buffer; a null buffer is BCB FCS 00)

### 3.2 Decoding (tolerant)

- Strip a leading `10 02` only at offset 0 of a TTR record classified as
  a buffer. Never scan for it inside the data.
- Accept a missing final RCB `00` (end of record = end of buffer).
- Ignore a trailing DLE ETB / DLE ETX (BSC habit; never send them).
- Classify line control first, by exact content: `01 2D` SOH ENQ,
  `10 70` DLE ACK0, `32 3D` SYN NAK, `3D` NAK. These are never framed.
  A buffer starts with `10 02` or with a BCB (high bit on).

### 3.3 Adaptive framing: the safety net that remains

All known peers use DLE STX, so gonje's default is DLE STX. For unknown
peers:

1. **Mirror.** Every data buffer received shows the peer's form (starts
   with `10 02` or not). Send in that form from then on and log it.
2. **Blind start** (our I goes first: we dialled, or the peer sent SYN
   NAK): the form confirmed earlier for this peer, else DLE STX.
   No other heuristics.
3. **Bounded fallback.** If the peer resets, closes, NAKs or ignores our
   I before sending any data buffer, try the other form **once** in
   this connect window. If that fails too, go back to DLE STX and stay
   there. Wait at least 5 s before redialling after a rejected signon,
   which prevents a tight reconnect loop.
4. **Remember** the confirmed form per peer (gonje: `gonje.state` next to
   the pid file). Store "no DLE STX" only if the peer itself sent buffers
   without it (mirrored); never store a blind guess.

State per peer:

    learned  : form confirmed by a signon (persisted), or unknown
    next     : form for the next attempt after a rejection, or unset
    triedOther: the fallback was used in this connect window

    attempt:   form = next ?: learned ?: DLE STX
    rejected:  if !triedOther: next = !form, triedOther = true
               else: next = unset (DLE STX)
               if learned == form: forget learned
               retry after max(backoff, 5 s)
    signed on: next = unset, triedOther = false
               if form == DLE STX or mirrored: learned = form (store)
    operator "connect" or "link reset-framing": triedOther = false
                                               (reset also forgets learned)

## 4. Other observations (not changed in gonje)

- **NCCIFEAT.** Sine Nomine offers X'15' and RSCS answers X'14'. gonje
  sends X'00' in I and J and supports none of the optional features
  (its J therefore answers "I features AND ours" = 0). Whether RSCS
  accepts X'00' from gonje is not yet confirmed by a live trace; if it
  objects, offering X'15' like Sine Nomine is the next thing to try.
- **Buffer size.** RSCS answered 4096 to an 8192 offer. gonje already
  sends at most min(ours, peer's).

## 5. Log messages (gonje)

    framing: DLE STX (guessed: default)                    blind start
    framing: DLE STX (learned: confirmed earlier)          from gonje.state
    framing: none (guessed: the other form was rejected)   fallback (once)
    framing: DLE STX (mirrored peer)                       first peer buffer
    framing: DLE STX (mirrored peer, was none)             switched
    framing: DLE STX rejected by PEER (no J: EOF)          session
    framing: DLE STX rejected, next attempt uses none      node
    framing: none rejected too, back to DLE STX            node (bounded)
    framing: DLE STX confirmed (guessed)                   stored

## 6. Tests to replicate

| Test | Expected |
|---|---|
| I/J golden (`internal/nje/records/signon_frame_test.go`) | our I with node RELAY, buffer 8192, features X'15' equals Sine Nomine's 46 bytes; our J equals RSCS's J; B signoff `10 02 .. F0 C2`; null buffer `10 02 81 8F CF 00` |
| decode both | I/J with and without trailing `00`, J without DLE STX, B with and without `00` |
| RSCS signon (`internal/node/framing_test.go`) | fake RSCS: SYN NAK, wants DLE STX, resets unless the I TTR is exactly 46 bytes; signon completes first time (with and without OPEN RIP 0.0.0.0) |
| bounded fallback | peer rejects everything: forms DLE STX, none, DLE STX, DLE STX...; attempts at least RejectRetry apart |
| mirror | Hercules-style primary peer; our J copies its form |
| fallback + restart | a (hypothetical) no-DLE-STX peer: second attempt works, stored, restart uses it |
| relearn | stored form fails: fallback, new form stored, other entries kept |

Golden bytes as used in the test:

```go
// From the Sine Nomine NJE / z/VM RSCS (ZVM72MSH) trace of 2026-11-09:
// both TTR records are 46 bytes, no RCB 00 after the F0 record.
const (
	snaI = "10 02 80 8F CF F0 C9 29 D9C5D3C1E8404040 01 00000000 0000 2000" +
		" 40404040404040404040404040404040 00 15 00 00 00"
	rscsJ = "10 02 80 8F CF F0 D1 29 E9E5D4F7F2D4E2C8 01 FFFFFFFF 0000 1000" +
		" 40404040404040404040404040404040 00 14 00 00 00"
	snaKeepalive = "10 02 81 8F CF 00"
)

// TestSignonBufferHasNoEOB: our I and J encode to exactly the bytes
// RSCS accepts (golden from the trace, our encoder, same node name).
func TestSignonBufferHasNoEOB(t *testing.T) {
	i := &Signon{SRCB: SRCBSignonI, Node: "RELAY", Qual: 1, BufSize: 8192, Features: [4]byte{0x15}}
	got := (&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{i.Record()}}).Encode()
	if want := unhex(t, snaI); !bytes.Equal(got, want) || len(got) != 46 {
		t.Fatalf("I\n got % X\nwant % X", got, want)
	}
	j := &Signon{SRCB: SRCBSignonJ, Node: "ZVM72MSH", Qual: 1, BufSize: 4096, Event: CESPredefined, Features: [4]byte{0x14}}
	got = (&Buffer{BCB: 0x80, FCS: FCSNormal, Records: []Record{j.Record()}}).Encode()
	if want := unhex(t, rscsJ); !bytes.Equal(got, want) {
		t.Fatalf("J\n got % X\nwant % X", got, want)
	}
	// A signoff (B) is an F0 record alone too: no RCB 00 either.
	b := (&Buffer{BCB: 0x82, FCS: FCSNormal, Records: []Record{{RCB: RCBControl, SRCB: 0xC2}}}).Encode()
	if !bytes.Equal(b, unhex(t, "10 02 82 8F CF F0 C2")) {
		t.Fatalf("B % X", b)
	}
	// Data and null buffers keep their end-of-buffer RCB 00.
	if k := (&Buffer{BCB: 0x81, FCS: FCSNormal}).Encode(); !bytes.Equal(k, unhex(t, snaKeepalive)) {
		t.Fatalf("null buffer % X", k)
	}
	nmr := (&Buffer{BCB: 0x81, FCS: FCSNormal, Records: []Record{{RCB: RCBNMR, SRCB: 0x80, Data: []byte{1, 2, 3}}}}).Encode()
	if nmr[len(nmr)-1] != RCBEOB || nmr[len(nmr)-2] != 0x00 {
		t.Fatalf("data buffer % X", nmr)
	}
}

```

## 7. Code (gonje)

### 7.1 `internal/nje/records/buffer.go`

```go
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

// Classify tells line-control sequences apart from data buffers. A data
// buffer starts with DLE STX or directly with a BCB (high bit set).
func Classify(rec []byte) LineKind {
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
```

### 7.2 `internal/nje/records/ncc.go`: the signon record

```go
// Record encodes the signon as an F0 record.
func (s *Signon) Record() Record {
	// Data starts at the NCCIDL byte (offset 2), so offsets below are
	// manual offsets minus 2.
	d := make([]byte, SignonLen-2)
	d[0] = SignonLen
	ebcdic.PutField(d[1:9], strings.ToUpper(s.Node))
	d[9] = s.Qual
	binary.BigEndian.PutUint32(d[10:14], s.Event)
	binary.BigEndian.PutUint16(d[14:16], s.Resist)
	binary.BigEndian.PutUint16(d[16:18], s.BufSize)
	ebcdic.PutField(d[18:26], s.LinePW)
	ebcdic.PutField(d[26:34], s.NodePW)
	d[34] = s.Flags
	copy(d[35:39], s.Features[:])
	return Record{RCB: RCBControl, SRCB: s.SRCB, Data: d}
}
```

### 7.3 `internal/nje/link/framing.go` (whole file)

```go
// Adaptive DLE STX framing.
//
// Every known TCPNJE peer (Hercules TCPNJE / NJE38, z/VM RSCS TCPNJE,
// Sine Nomine NJE) starts its buffers with DLE STX, and so does gonje.
// (An earlier theory blamed DLE STX for RSCS's DMTTNE945E code 1818; the
// real cause was the RCB 00 after the F0 signon record, see
// records.Buffer.EncodeFramed and DLE_STX.md.) What remains is a safety
// net for an unknown peer:
//
//   - Mirror: every data buffer from the peer shows its form (starts
//     with 10 02 or not); we send in that form from then on.
//   - Blind start (our I goes first): the form the node learned or
//     mirrored earlier, else DLE STX. No other guessing.
//   - Fallback: an I that is answered by a reset, a close, a NAK or
//     nothing is reported (FramingRejected); the node tries the other
//     form once, then returns to DLE STX (bounded, with backoff).
//
// Receiving always accepts both forms (records.Decode strips a leading
// 10 02 at the start of the record only). Line control sequences (SOH
// ENQ, DLE ACK0, SYN NAK, NAK) never carry DLE STX.

// Framing selects the form of our buffers.
type Framing int

// Framing forms.
const (
	FramingAuto   Framing = iota // DLE STX (the default)
	FramingDLESTX                // 10 02 BCB FCS ...
	FramingNone                  // BCB FCS ...
)

// Framing sources (how the form was chosen), shown by "stats <node>".
const (
	FramingLearned  = "learned"  // remembered from an earlier signon
	FramingGuessed  = "guessed"  // default or fallback
	FramingMirrored = "mirrored" // copied from the peer's own buffers
)

// FormName names a form for logs and the shell.
func FormName(dleSTX bool) string {
	if dleSTX {
		return "DLE STX"
	}
	return "none"
}

// initFraming sets the initial form from the parameters.
func (s *Session) initFraming() {
	s.dleSTX = s.p.Framing != FramingNone
	s.framingSrc = s.p.FramingSrc
	if s.framingSrc == "" {
		s.framingSrc = FramingGuessed
	}
	s.framingWhy = s.p.FramingWhy
	if s.framingWhy == "" {
		s.framingWhy = "default"
	}
	s.statFraming()
}

// Framing returns the form we send, how it was chosen and why.
func (s *Session) Framing() (dleSTX bool, src, why string) {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	return s.dleSTX, s.framingSrc, s.framingWhy
}

// FramingRejected reports that our signon I went unanswered (reset,
// close, NAK, E0 or timeout) in the form Framing returns.
func (s *Session) FramingRejected() bool {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	return s.framingRejected
}

// statFraming copies the form into the stats (for "stats <node>").
func (s *Session) statFraming() {
	dle, src, _ := s.dleSTX, s.framingSrc, s.framingWhy
	s.count(func(st *Stats) { st.Framing = FormName(dle) + " (" + src + ")" })
}

// announceFraming logs the blind choice before our I goes out.
func (s *Session) announceFraming() {
	dle, src, why := s.Framing()
	s.framingEvent(fmt.Sprintf("framing: %s (%s: %s)", FormName(dle), src, why))
}

func (s *Session) framingEvent(text string) {
	s.log.Info(text)
	s.emit(EvLink, L2, text)
}

// mirrorFraming adopts the form of a data buffer the peer sent (rec is
// one TTR record classified as a buffer). Logged when first seen and on
// every change.
func (s *Session) mirrorFraming(rec []byte) {
	peer := len(rec) >= 2 && rec[0] == records.DLE && rec[1] == records.STX
	s.fmu.Lock()
	first, changed := !s.mirrored, peer != s.dleSTX
	old := s.dleSTX
	s.mirrored, s.dleSTX = true, peer
	if first || changed {
		s.framingSrc, s.framingWhy = FramingMirrored, "peer's buffers"
		s.statFraming()
	}
	s.fmu.Unlock()
	switch {
	case changed:
		s.framingEvent(fmt.Sprintf("framing: %s (mirrored peer, was %s)", FormName(peer), FormName(old)))
	case first:
		s.framingEvent(fmt.Sprintf("framing: %s (mirrored peer)", FormName(peer)))
	}
}

// rejectFraming notes that the peer did not answer our I.
func (s *Session) rejectFraming(err error) {
	s.fmu.Lock()
	if !s.iSent || s.mirrored {
		// The peer did send data: its form is known, the failure is
		// something else.
		s.fmu.Unlock()
		return
	}
	s.framingRejected = true
	dle := s.dleSTX
	s.fmu.Unlock()
	s.framingEvent(fmt.Sprintf("framing: %s rejected by %s (no J: %v)", FormName(dle), s.p.Remote, err))
}
```

### 7.4 `internal/nje/link/session.go`: blind I and rejection

```go
// primaryIJ sends I and waits for J.
//
// We send first, so the framing is a choice made blind (framing.go). If
// the peer resets, closes, refuses or stays silent instead of answering
// with J, that form is reported as rejected (FramingRejected) so the
// node tries the other form next time.
func (s *Session) primaryIJ() error {
	s.announceFraming()
	if err := s.sendBuffer([]records.Record{s.signonRecord(records.SRCBSignonI, nil)}); err != nil {
		return err
	}
	s.fmu.Lock()
	s.iSent = true
	s.fmu.Unlock()
	s.emit(EvSignon, L2, fmt.Sprintf("signon I sent (we are primary, bufsz=%d)", s.p.BufSize))
	j, err := s.waitSignon(records.SRCBSignonJ)
	if err != nil {
		s.rejectFraming(err)
		return err
	}
	s.finishSignon(j, true)
	// J answered our I, so it is our turn: Run answers J right away.
	s.takeTurn(time.Now(), false, false)
	return nil
}
```

`sendBuffer` encodes with the current form: `s.lastBlock =
b.EncodeFramed(dle)`. Every received data buffer goes through
`s.mirrorFraming(rec)` before decoding (in `waitSignon` and
`handleInbound`).

### 7.5 `internal/node/framing.go`: decisions, bounded fallback

```go
// framingLocked fills the framing parameters of an attempt (n.mu held).
func (n *Node) framingLocked(r *remote, p *link.Params) {
	set := func(dle bool, src, why string) {
		p.Framing = link.FramingNone
		if dle {
			p.Framing = link.FramingDLESTX
		}
		p.FramingSrc, p.FramingWhy = src, why
	}
	switch {
	case r.next != nil:
		set(*r.next, link.FramingGuessed, "the other form was rejected")
	case r.learned != nil:
		set(*r.learned, link.FramingLearned, "confirmed earlier")
	default:
		p.Framing, p.FramingSrc, p.FramingWhy = link.FramingAuto, link.FramingGuessed, "default"
	}
}

// framingFailed handles a signon that failed. If the peer rejected our
// I, the other form is tried once per connect window (then DLE STX
// again), a remembered form that failed is forgotten, and the retry
// waits at least RejectRetry: the returned delay for attemptFailed
// (0 = normal backoff).
func (n *Node) framingFailed(r *remote, s *link.Session) time.Duration {
	if !s.FramingRejected() {
		return 0
	}
	dle, _, _ := s.Framing()
	n.mu.Lock()
	var text string
	switch {
	case !r.triedOther:
		other := !dle
		r.next, r.triedOther = &other, true
		text = fmt.Sprintf("framing: %s rejected, next attempt uses %s", link.FormName(dle), link.FormName(other))
	default:
		// Both forms failed: the problem is not the framing. Stay with
		// DLE STX (the form every known peer uses).
		r.next = nil
		text = fmt.Sprintf("framing: %s rejected too, back to DLE STX", link.FormName(dle))
	}
	forget := r.learned != nil && *r.learned == dle
	if forget {
		r.learned = nil
	}
	retry := max(n.backoffLocked(r), n.opts.RejectRetry)
	n.mu.Unlock()
	n.log.Warn(r.cfg.Name + ": " + text)
	n.emit(link.EvLink, link.L2, r.cfg.Name, text)
	if forget {
		n.saveFraming("")
	}
	return retry
}

// framingConfirmed records the form of a session that signed on. A
// guessed "none" is not stored: only a form the peer used itself.
func (n *Node) framingConfirmed(r *remote, s *link.Session) {
	dle, src, _ := s.Framing()
	n.mu.Lock()
	r.next, r.triedOther = nil, false
	if !dle && src != link.FramingMirrored {
		n.mu.Unlock()
		return
	}
	changed := r.learned == nil || *r.learned != dle
	r.learned = &dle
	n.mu.Unlock()
	if changed {
		text := fmt.Sprintf("framing: %s confirmed (%s)", link.FormName(dle), src)
		n.log.Info(r.cfg.Name + ": " + text)
		n.emit(link.EvLink, link.L2, r.cfg.Name, text)
	}
	n.saveFraming(r.cfg.Name) // also refreshes the confirmed time
}
```

## 8. Porting checklist for ../tsu/

1. Encoder: no RCB `00` after a buffer that holds only an F0 record;
   keep DLE STX. Golden-test the 46-byte I against the trace above.
2. Decoder: tolerate a missing trailing `00` and an optional leading
   `10 02` (offset 0 only).
3. Optional safety net: mirror the peer's form; try the other form once
   per connect window after a rejected I; back off 5 s or more; persist
   only confirmed forms.
