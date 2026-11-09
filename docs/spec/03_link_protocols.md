# 03 - NJE Link Protocols (BSC framing, BCB/FCS/RCB/SRCB/SCB, ACK/NAK, signon)

Source: IBM SA32-0988-02 "z/OS NJE Formats and Protocols".
Primary scope: Chapter 11 (txt lines 8692-10488). Chapter 11 only describes the
*line* procedures (SOH ENQ / DLE ACK0 / NAK / PREPARE / CTC / SNA). The byte
layouts of BCB, FCS, RCB, SRCB, SCB are in Chapter 8 (txt 7380-8095) and
Chapter 1 (txt 880-1060); they are included here because the link layer cannot
be implemented without them. Chapter 12 (TCP/IP, txt 10488+) is touched only
where it changes the BSC rules. Appendix A deviations (txt ~11100-11910) noted.

Tags used below:
- **[MANUAL]** stated in the manual (page numbers = printed page).
- **[EXT]** not in the manual; from standard EBCDIC BSC or from known open
  implementations (funetnje/HUJI-NJE, HNET). Verify against a live JES2.
- **[AMBIG]** manual is unclear / contradictory.

---

## 1. BSC control characters (EBCDIC)

| Name | Hex | Source |
|---|---|---|
| SOH | `01` | [MANUAL] via SOH ENQ = X'012D' (p.183) |
| STX | `02` | [MANUAL] via DLE STX = X'1002' (p.180) |
| ETX | `03` | [EXT] standard EBCDIC BSC |
| DLE | `10` | [MANUAL] |
| ENQ | `2D` | [MANUAL] |
| ETB | `26` | [EXT] standard EBCDIC BSC |
| SYN | `32` | [MANUAL] via SYN NAK = X'323D' (p.183) |
| NAK | `3D` | [MANUAL] (p.180) |
| ACK0 (2nd char) | `70` | [MANUAL] DLE ACK0 = X'1070' (p.180) |

Two-byte sequences used by NJE:

| Sequence | Bytes | Meaning |
|---|---|---|
| SOH ENQ | `01 2D` | "I want to be primary" / bid for line (init) |
| DLE ACK0 | `10 70` | positive ack (init response; or ack w/o data) |
| NAK | `3D` | negative ack (single byte) |
| SYN NAK | `32 3D` | CTC/TCP: "either role OK, you be primary" |
| DLE STX | `10 02` | start of transmission block |
| DLE ETB | `10 26` | end of transmission block (normal) |
| DLE ETX | `10 03` | end of block, PREPARE-mode suspend request only |
| DLE ENQ | `10 2D` | PREPARE-mode resume (wake-up) only |

---

## 2. Transmission block (BSC/CTC "non-SNA buffer") layout

[MANUAL p.143-144, Fig 8 & 15]

```
DLE STX | BCB | FCS1 FCS2 | rec1 | rec2 | ... | RCB=00 | DLE ETB
 10 02    1B     2B                               1B      10 26

rec (compressed, normal data/stream-control record):
  RCB | SRCB | SCB [data] | SCB [data] | ... | SCB=00 (EOR)

rec (F0 control record, NOT compressed: signon I/J/K/L/M/N, signoff B):
  RCB=F0 | SRCB=C'x' | NCCR body (begins with its own length byte, see notes 02)
```

- "Null buffer" (= ACK with no data, keep-alive) =
  `10 02 | BCB | FCS1 FCS2 | 00 | 10 26` [MANUAL: null block contains a BCB,
  Fig 16/17; trailing RCB 00 is the normal buffer terminator].
- RCB `00` = end-of-block. Placed after last record; buffer truncated there.
  [MANUAL p.149]. A receiver stops parsing at RCB 00.
- Each buffer holds records of ONE stream only (one RCB value) unless the
  NCCIMRCB signon flag was negotiated [MANUAL p.147-148]. Violation =>
  terminate connection (all streams).
- Job header, data set header, job trailer must each be the FIRST record in
  its buffer and alone, unless signon flag NCCIPACK negotiated [MANUAL p.143].
- I and J signon records must be the ONLY record in the buffer. Other F0
  records may be blocked together but never with non-F0 records [MANUAL p.150-151].
- Record segments never span buffers (compressed records are always whole in
  a buffer); logical records >255 use the spanned-record format [MANUAL p.149,158].

### 2.1 Over TCP/IP (Chapter 12) - what changes

```
TTB(8) | TTR(4) | record | TTR(4) | record | ... | TTR EOB(4 x 00)
TTB = F(1)=00 | U(1)=00 | LN(2, big-endian, total length incl. TTB+EOB TTR) | 4 x 00
TTR = F(1)=00 | U(1)=00 | LN(2, big-endian, length of following record)
```
- Each "record" inside a TTR is one BSC transmission (e.g. `01 2D`, `10 70`,
  or a DLE STX ... block). [MANUAL: Fig 61; layout details are chapter 12 notes]
- Max TCP send/receive buffer 8192 [MANUAL p.216]. TCP reads are partial;
  reassemble by TTB.LN then TTR.LN.
- Chapter 12 says TCP NJE carries "CTC NJE data buffers" [MANUAL p.211] ->
  CTC rules apply: no NAK retransmission scheme is needed (see 6.4).
- **DLE transparency** [AMBIG/EXT]: the manual never describes DLE doubling
  for NJE at all (neither BSC chapter nor TCP chapter). BSC hardware used
  transparent mode; over TCP, framing is by TTB/TTR lengths, so **do NOT
  double DLE bytes** and do not scan for DLE ETB. [EXT] Open implementations
  send `DLE STX BCB FCS ... RCB 00` inside the TTR and **omit the trailing
  DLE ETB** on TCP. Recommendation: on send, omit DLE ETB; on receive, accept
  a record with or without trailing `10 26`; strip leading `10 02`.
  Verify with a packet capture against z/OS.

---

## 3. BCB - Block Control Byte

[MANUAL Table 45, p.144-145]

| Bits | Hex | Meaning |
|---|---|---|
| `1000 cccc` | `80`-`8F` | Normal block, sequence count cccc (mod 16) |
| `1001 ....` | `90` | Bypass sequence-count validation ("BCB ignore bit"); low nibble ignored |
| `1010 cccc` | `A0`-`AF` | Reset: receiver sets expected inbound count to cccc |
| `1011 ....` | `B0`-`BF` | Reserved (IBM) |
| `11xx ....` | `C0`-`FF` | Reserved (IBM) |
| `0xxx xxxx` | `00`-`7F` | Invalid (bit 0 must be 1) |

Counter rules [MANUAL p.145]:
- After line init (after SOH ENQ / DLE ACK0) both counters = 0, modulo 16.
- Outbound count is incremented each time the block is ACKed (i.e. when a
  response - data block, null buffer, or DLE ACK0 - comes back for it).
  The BCB sent = `0x80 | outSeq`.
- Inbound: received count must equal expected (= last received + 1 mod 16;
  first expected = 0). [EXT] So the first block each side sends has BCB `80`.
- Null buffers carry a BCB and consume a sequence number (Fig 17 shows them
  numbered). DLE ACK0 and NAK have no BCB and consume nothing.

Receiver BCB check (pseudo-code):
```
func checkBCB(b byte) action:
    if b & 0x80 == 0           -> protocol error (terminate)
    switch b & 0xF0:
      case 0x90: accept (no seq check; do NOT advance inSeq)   // [AMBIG] whether
                                                               // to advance; EXT impls don't
      case 0xA0: inSeq = b & 0x0F; accept; inSeq = (inSeq+1)&0xF
      case 0x80:
          c := b & 0x0F
          if c == inSeq:            accept; inSeq = (inSeq+1)&0xF
          elif c == (inSeq-1)&0xF:  DUPLICATE: discard block, retransmit
                                    our last un-acked (last non-NAK) block
          else:                     SEQUENCE ERROR: send a block containing
                                    record RCB=E0, SRCB=expected count (0x80|inSeq?)
                                    then terminate (manual: receiver of E0
                                    terminates; no recovery possible)
      default: reserved -> terminate
```
- E0 record: `RCB=E0 SRCB=<expected count>`; the received count is in the BCB
  [MANUAL Table 50]. [AMBIG] whether SRCB holds `0x0c` or `0x8c`; and whether
  E0 is followed by an EOR SCB. [EXT] funetnje sends `E0 <expected|0x80>`.
- JES3 deviations [MANUAL App.A p.235]: duplicate BCB -> ignored but no
  retransmit; missing BCB -> line cancelled; duplicate BCB during signon ->
  cancelled; 10 consecutive NAKs -> cancelled.

---

## 4. FCS - Function Control Sequence (2 bytes)

[MANUAL Table 47, p.147] Sent by each side to control its INBOUND flow.
1 = stream enabled (may send to me), 0 = held (do not send).

| Byte | Mask | Meaning |
|---|---|---|
| FCS1 | `80` | must be 1 |
| FCS1 | `40` | **wait-a-bit**: suspend ALL stream transmission to me |
| FCS1 | `30` | reserved (0) |
| FCS1 | `08` | SYSIN 1 |
| FCS1 | `04` | SYSIN 2 / SYSOUT 7 |
| FCS1 | `02` | SYSIN 3 / SYSOUT 6 |
| FCS1 | `01` | SYSIN 4 / SYSOUT 5 |
| FCS2 | `80` | must be 1 |
| FCS2 | `40` | console (NMR / operator command stream, RCB 9A) |
| FCS2 | `30` | reserved (0) |
| FCS2 | `08` | SYSIN 5 / SYSOUT 4 |
| FCS2 | `04` | SYSIN 6 / SYSOUT 3 |
| FCS2 | `02` | SYSIN 7 / SYSOUT 2 |
| FCS2 | `01` | SYSOUT 1 |

- All enabled, normal: `8F CF`. Wait-a-bit, all held: `C0 80`
  ([EXT] typical; at minimum set `40` in FCS1).
- Bit sharing means at most 8 SYSIN+SYSOUT streams active per direction:
  SYSIN n -> bit index n-1; SYSOUT m -> bit index 8-m, where bit index
  k=0..3 -> FCS1 mask `0x08>>k`, k=4..7 -> FCS2 mask `0x08>>(k-4)`.
```
func fcsMask(isSysout bool, n int) (byteIdx int, mask byte):
    k := isSysout ? 8-n : n-1          // n in 1..7
    if k < 4 { return 0, 0x08 >> k }
    return 1, 0x08 >> (k-4)
```
- Wait-a-bit handling [MANUAL p.147-148]:
  - If received with a NULL buffer: delay response 1-2 s (never >2 s; peer
    times out at 3 s on BSC).
  - If received with DATA: respond immediately.
  - Respond with a null buffer (IBM recommended) or DLE ACK0. NEVER respond to
    a wait-a-bit block with a data record.
  - If you receive DLE ACK0, apply the last FCS the peer sent.
  - RSCS: replies to wait-a-bit with null buffer, no delay. [App.A p.242]
- Must not send a stream's records when that stream's bit (or wait-a-bit) is
  off in the peer's last FCS. Violation -> peer may send B0 reason X'0C10'.

---

## 5. RCB / SRCB

[MANUAL Table 49-51, p.149-151]

| RCB | Meaning | SRCB |
|---|---|---|
| `00` | End of block (BSC) | none |
| `90` | Request to initiate stream | RCB of stream to start |
| `A0` | Permission to initiate stream | RCB of stream |
| `B0` | Negative permission / receiver cancel | RCB of stream, then reason |
| `C0` | Acknowledge transmission complete | RCB of stream received |
| `D0` | Ready to receive stream | RCB of stream |
| `E0` | BCB sequence error | expected count |
| `F0` | General control record (signon etc.) | EBCDIC id char |
| `9A` | Operator command / console message (NMR) | `80` (JES2 sends `00`) |
| `1nnn1000` = `98,A8,...,F8` | SYSIN stream n (n=1..7) | SYSIN ctl |
| `1nnn1001` = `99,A9,...,F9` | SYSOUT stream n (n=1..7) | SYSOUT ctl |
| other | reserved -> terminate connection | |

F0 SRCB ids: `C2`'B' signoff, `C9`'I' initial signon, `D1`'J' response signon,
`D2`'K' reset signon, `D3`'L' concurrence signon, `D4`'M' add connection,
`D5`'N' delete connection. F0 records are NOT compressed.

SYSIN SRCB: `80` standard record, `C0` job header, `E0` data set header,
`D0` job trailer.
SYSOUT SRCB: `10cc ss00`; cc: `00` no CC (`80`), `01` machine CC (`90`),
`10` ASA (`A0`), `11` CPDS page mode (`B0`); ss: `00` unspanned, `10` first,
`01` middle, `11` last segment (`+08`, `+04`, `+0C`). Control: `C0` job
header, `E0` DSH, `D0` job trailer.
End of file in a stream: RCB of stream + SRCB `00` [MANUAL p.10].
[AMBIG] whether an EOR SCB follows the `00` SRCB; [EXT] send `RCB 00 00`
(i.e. SRCB 00 + EOR SCB) and accept with or without.

B0 rejection on BSC [MANUAL Table 52]: `B0 9x C2 rc1 rc2` (SCB `C2` = 2
literal bytes) then EOR SCB `00` [EXT]. Reason codes (Fig 18):
`04` reply to ABORT, `08` shutdown, `0C`+`04/08/0C/10` stream not supported /
drained / unknown / FCS conflict, `10`+`04..10` resource lack, `14`+`04/08/0C`
HOLD/STOP/FLUSH, `18`+`04/08/0C/10/14` protocol violation, `1C` datastream
error, `20` rejected by exit/security.

Error rule: unexpected/unrecognized/incorrect RCB (unstarted stream, mixed
RCBs, undefined value, out-of-sequence valid RCB) -> terminate connection.
Exception: request (90) to start an already-started stream -> reply B0, keep
connection.

---

## 6. SCB - String Control Byte compression (BSC/CTC/TCP)

[MANUAL Table 48, p.148; p.8-10] Always used for non-F0 records even if
nothing compresses.

| Bits | Hex range | Meaning |
|---|---|---|
| `00 000000` | `00` | End of record (EOR SCB) |
| `01 000000` | `40` | Terminate transmission of this stream (transmitter abort) |
| `10 0bbbbb` | `81`-`9F` | Insert bbbbb (1..31) blanks (EBCDIC `40`) |
| `10 1ddddd` | `A1`-`BF` | Next 1 byte is a char; emit it ddddd (1..31) times |
| `11 cccccc` | `C1`-`FF` | cccccc (1..63) literal bytes follow |
| `01 xxxxxx` (x!=0) | `41`-`7F` | undefined -> compression error (B0 `1808`) |

- An SCB must appear at least every 64 bytes; max literal run 63.
- Blank char is EBCDIC X'40'. Blank SCB has no following byte.
- [AMBIG] "duplicated ddddd times": treated as total count of output chars
  (the char appears ddddd times in output, not ddddd+1). [EXT] All open
  implementations do total count. Same for blanks.
- [AMBIG] counts of 0 (`80`, `A0`, `C0`): not forbidden explicitly; never
  generate; on receive treat `80`/`A0` as no-op (`A0` still consumes 1 byte),
  `C0` as no-op.
- `40` (stream terminate): the transmitter is aborting the current file on
  this stream; discard the partial file. [AMBIG] whether further bytes follow.

### 6.1 Decompress (one record)
```
// in: bytes after RCB,SRCB.  returns (record, bytesConsumed, abort)
func decompress(in []byte) ([]byte, int, bool, error):
    out := []
    i := 0
    loop:
        if i >= len(in): return error("missing EOR SCB")
        scb := in[i]; i++
        switch:
          scb == 0x00:                   return out, i, false, nil
          scb == 0x40:                   return out, i, true,  nil   // stream abort
          scb & 0xC0 == 0x40:            return error("bad SCB")
          scb & 0xE0 == 0x80:            n := scb & 0x1F; append n x 0x40
          scb & 0xE0 == 0xA0:            n := scb & 0x1F
                                         c := in[i]; i++; append n x c
          scb & 0xC0 == 0xC0:            n := scb & 0x3F
                                         append in[i:i+n]; i += n
        if len(out) > 256 (segment max): error   // [EXT] sanity limit
```

### 6.2 Compress (one record, <=256 bytes)
Greedy; any valid encoding is accepted by receivers. Thresholds [EXT]:
blank run >= 2 -> blank SCB (1 byte replaces >=2); non-blank run >= 3 ->
dup SCB (2 bytes replace >=3).
```
func compress(rec []byte) []byte:
    out := []; lit := []            // pending literal run
    flush := func():
        for len(lit) > 0:
            n := min(63, len(lit))
            out += [0xC0 | n] + lit[:n]; lit = lit[n:]
    i := 0
    while i < len(rec):
        c := rec[i]; run := 1
        while i+run < len(rec) && rec[i+run] == c && run < 31: run++
        if c == 0x40 && run >= 2:
            flush(); out += [0x80 | run]; i += run
        elif c != 0x40 && run >= 3:
            flush(); out += [0xA0 | run, c]; i += run
        else:
            lit += rec[i]; i++         // take one byte, rescan next
            if len(lit) == 63: flush()
    flush()
    out += [0x00]                       // EOR SCB
    return out
```
Buffer fill: before appending `RCB SRCB compress(rec)` check it fits in
(bufSize - currentLen - 1 for RCB 00 [- 2 for DLE ETB if sent]); if not,
close the buffer with RCB 00 and start a new one. Compressed records never
span buffers.

---

## 7. Line-level sequences

### 7.1 Initialization / signon framing
BSC (Fig 37, p.177):
```
Primary                         Secondary
SOH ENQ  (01 2D)  ------------>
         <------------ DLE ACK0 (10 70)
DLE STX BCB FCS F0 C9 <I rec> 00 DLE ETB  ---->      (I = initial signon)
         <---- DLE STX BCB FCS F0 D1 <J rec> 00 DLE ETB
... (optional null buffers may precede the J; Fig 37 "Optional Null Buffers")
```
- Primary = node that RECEIVES DLE ACK0; it sends the I record [MANUAL p.13, p.171].
- Contention (both send SOH ENQ): on BSC resolved by hardware collision; the
  side whose SOH ENQ arrives first becomes secondary. Roles should be
  predefined (satellite "pass in the night"). [MANUAL p.177-178]
- Init error recovery (BSC, Table 62): until DLE ACK0 received (or SOH ENQ
  received and DLE ACK0 sent): unrecognized data / timeout / data check /
  overrun / lost data -> rewrite SOH ENQ; hardware faults -> terminate.
  RSCS retries 15 times. [App.A]
- CTC (p.183): side B may send SYN NAK (`32 3D`) = "you be primary"; side A
  answers SOH ENQ; B answers DLE ACK0. If B wants primary it sends SOH ENQ
  instead of SYN NAK.

TCP/IP (ch.12, p.215-216), after OPEN/ACK control record exchange:
```
Client(connector)                      Server(acceptor)
  SOH ENQ  --->                  <--- DLE ACK0      client is primary, sends I
  SYN NAK  --->                  <--- SOH ENQ
  DLE ACK0 --->                                     server is primary, sends I
```
- Client gets anything other than DLE ACK0 after SOH ENQ -> close.
- Server gets neither SOH ENQ nor SYN NAK -> close. After its SOH ENQ, if
  client sends SOH ENQ again (not DLE ACK0) -> close.
- JES TCP/IP never sends SYN NAK itself but accepts it. So for gonje as
  client: send SOH ENQ (become primary). As server: answer SOH ENQ with DLE
  ACK0, answer SYN NAK with SOH ENQ.
- Each of these 2-byte sequences travels as one TTR record in a TTB block.

### 7.2 Normal data exchange (Fig 39, p.181)
Strict half-duplex ping-pong ("pseudo full duplex", both directions
multiplexed): every block sent must be answered by exactly one transmission
from the peer, which is either:
- a data block (`DLE STX ...`) - also counts as positive ACK of the previous
  block [MANUAL p.180], or
- a null buffer (BCB+FCS+RCB 00) - preferred ACK when nothing to send, or
- DLE ACK0 - allowed ACK but discouraged: it carries no BCB so a lost block
  cannot be detected (Fig 16). JES2 and JES3 nonetheless use DLE ACK0 when
  idle; RSCS uses null buffers. Accept both.

Idle keep-alive: when no data, a null buffer (or DLE ACK0) is exchanged every
1-2 seconds [MANUAL p.186 Fig 44]. Over TCP, JES2 throttles: after 10
consecutive idle exchanges, delay 100 ms before next send, +100 ms per further
idle exchange, max 1 s [App.A JES2 TCP/IP].

### 7.3 State table (Table 46, p.146)
```
State 1 WAIT_RESPONSE / State 3 RECOVERING (same transitions):
  recv data block      -> State 2 (check BCB)
  recv DLE ACK0        -> send next block (the "last non-NAK" if a resend is
                          due, else next data/null), stay waiting
  recv NAK             -> retransmit last non-NAK transmission; wait
  recv error/garbage   -> errCount++ ; State 4
State 2 CHECK_BCB:
  BCB OK               -> process records, send next block; wait
  duplicate BCB        -> discard; retransmit last non-NAK; wait
  incorrect BCB        -> send E0 sequence-error record; (then terminate)
State 4 CHECK_ERRCOUNT:
  errCount <= limit    -> send NAK; State 3
  errCount  > limit    -> terminate
```
Limits: JES2 10 errors, JES3 20, RSCS 15 (init). [App.A]
"Last non-NAK transmission" = the last thing we sent that was not a NAK
(data block, null buffer or DLE ACK0); keep a copy for resend.
Receiving NAK is itself an error -> resend last non-NAK.

### 7.4 Timeouts and NAK (BSC, Table 63 p.180)
- BSC adapter read timeout = 3 s: if no block arrives within 3 s, send NAK.
- Unrecognized data / data check / overrun / lost data -> send NAK.
- Receiving SOH ENQ after init -> peer restarted: restart line (go to init).
- CTC/TCP: **A NAK is never sent on a CTC adapter; if one is received, take
  the link down** [MANUAL p.195]. Since TCP NJE uses CTC-style buffers and TCP
  is reliable, [EXT] recommendation for gonje over TCP: never send NAK in
  normal operation; on receiving NAK, retransmit last non-NAK once (lenient,
  BSC rule) or drop the link (CTC rule) - [AMBIG] which JES2-over-TCP expects.
  Use a much longer inactivity timeout over TCP (e.g. 30-60 s, [EXT]) since
  the peer may delay up to 1 s (JES2 idle throttle) or 2 s (wait-a-bit).
- Unexpected DLE ENQ (PREPARE not negotiated) -> respond NAK (BSC) [p.193].

### 7.5 Termination (p.180, p.184)
Send a signoff record (`RCB F0 SRCB C2 'B'` + body); no BSC sequence and no
response expected; then close/disable. On TCP: close the socket.

---

## 8. PREPARE mode (I/O suspension) - not used on TCP

[MANUAL p.185-198] RSCS-only option, negotiated by signon concurrence bit
X'80' in the first concurrence byte. JES2/JES3/POWER: not supported.
- Request: null buffer ended with DLE ETX (`10 03`) instead of DLE ETB.
  Accept: peer replies immediately with null buffer + DLE ETX -> both idle.
  Decline: reply with normal data/null buffer ending DLE ETB.
- Resume (BSC): `(20 SYN) DLE ENQ`, answered by a null buffer; then normal.
- Null buffers (not DLE ACK0) are mandatory when PREPARE is in use.
- Optional 5-15 min probe with a null buffer during suspension.
- Never allowed while wait-a-bit is set or any file is active.
gonje recommendation: never set the PREPARE concurrence bit; if a DLE ETX
null buffer is received anyway, just answer with a normal (DLE ETB) null
buffer, which declines suspension.

---

## 9. Block size rules

| Rule | Source |
|---|---|
| Min transmission buffer 300 bytes (256-byte record + SCBs + BSC ctl) | [MANUAL p.143] |
| Max transmission buffer 32K (BSC/CTC) | [MANUAL p.143] |
| Actual size = smaller of the two sizes in the I/J signon records (NCCIBFSZ) | [MANUAL p.202 note, App.A RSCS] |
| JES3 does not negotiate: mismatch -> line terminated | [App.A] |
| TCP send/receive max 8192 per TCP buffer | [MANUAL p.216] |
| Record segment max 256 data bytes; unspanned record max 255 incl. CC | [MANUAL p.8, p.157] |
| Spanned logical record max 32760 | [MANUAL p.158] |
| SNA RU max 65535 / FMH4 RU >= 300 (SNA only) | [MANUAL p.153, p.202] |

[AMBIG] Whether the negotiated buffer size counts DLE STX/DLE ETB and the TTB/
TTR headers. [EXT] treat buffer size as BCB..RCB-00 inclusive plus DLE
STX/ETB, and keep TTB.LN <= 8192; using a configured size of e.g. 1024-4096
against JES2 is safe.

---

## 10. SNA (brief, not used by gonje)
LU type 0, full duplex, BIND (X'31'...), FMH4 (`08 04` RU size, features
byte: optimized fanout, signon accepted, compaction supported bits) and
optional FMH3 (compaction table) exchanged before I/J. SNA uses RID
(RCB,SRCB,LEN) instead of RCB/SRCB, different SCB encodings, and optional
compaction. No BCB/FCS/DLE. Irrelevant over TCP/IP.

---

## 11. Ambiguities / open questions (summary)
1. DLE transparency and DLE ETB presence over TCP: manual silent. Assume no
   doubling, DLE STX kept, DLE ETB omitted on send, tolerated on receive.
2. ETB/ETX hex (`26`/`03`) not printed in the manual (standard EBCDIC).
3. First BCB value: manual says counters start at 0 -> first BCB `80`.
4. Does `90` (bypass) or `A0` (reset) advance the inbound counter? Assumed:
   A0 sets then advances; 90 does not advance.
5. E0 record SRCB encoding (`0c` vs `8c`) and trailing SCB.
6. SCB dup/blank count = total occurrences (not +1). Zero counts undefined.
7. SCB `40` (stream terminate) exact follow-on format.
8. NAK behaviour over TCP: BSC rule (resend) vs CTC rule (drop link).
9. EOF record (stream RCB + SRCB `00`): followed by EOR SCB or not.
10. Whether null buffers sent as ACK consume a BCB number (Fig 17 says yes).
