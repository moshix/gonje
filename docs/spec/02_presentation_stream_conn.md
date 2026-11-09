# NJE Presentation Services, Stream Control, Connection Control
Source: SA32-0988-02 "z/OS NJE Formats and Protocols", text lines 7380-8692
(Ch. 8 p.143-159, Ch. 9 p.161-167, Ch. 10 p.169-176). NCCR field layouts are
from Ch. 7 (lines 7110-7378, p.137-141) because Ch. 10 refers to them; they are
included here so this note is self-contained.

All character data is EBCDIC (CP037/500 for node names: A-Z, 0-9, @#$).
Blank = X'40'. All binary integers are big-endian.
Useful EBCDIC: 'B'=C2 'I'=C9 'J'=D1 'K'=D2 'L'=D3 'M'=D4 'N'=D5.

Legend: [AMBIG-n] = ambiguity / doc error, collected at the end.

---------------------------------------------------------------------------
## 1. Non-SNA transmission buffer (BSC, CTC, and the payload inside TCP/IP NJE)

TCP/IP NJE carries the BSC-style buffer (BCB+FCS+records) inside TTB/TTR
framing (Ch. 11, not in this note's scope). Everything below applies to it.

### 1.1 Layout

| Off | Len | Field | Notes |
|-----|-----|-------|-------|
| 0 | 1 | BCB | block control byte, sec 1.2 |
| 1 | 2 | FCS | function control sequence, sec 1.3 |
| 3 | n | record 1 | RCB, SRCB, then SCB-compressed data ending in SCB X'00' |
| .. | .. | record 2..k | same; all records same stream unless NCCIMRCB |
| end | 1 | RCB X'00' | end-of-block (EOB) "stand-alone" RCB |

Record framing (compressed data records, stream control records):
```
RCB SRCB SCB data... [SCB data...]... SCB=X'00'(EOR)
```
F0 (connection control / signon) records are NOT compressed: no SCBs; the
record is `RCB SRCB DL fields...` where DL (3rd byte) = length from RCB to end
of record. [AMBIG-1]

Rules:
- Buffer size: min 300, max 32K (32768). Never send more than the peer
  advertised in NCCIBFSZ (I record) / NCCIBUFSZ (J record).
- Job header, data set header, job trailer must each be the FIRST record in a
  buffer and in separate buffers, unless both sides agreed NCCIPACK (X'10').
- Records from different streams (different RCB) may NOT share a buffer
  unless NCCIMRCB (X'04') agreed. Violation => connection terminated.
- Compressed segments are never split across buffers. If the next record
  does not fit, put RCB X'00' and send.
- F0 records may be blocked several per buffer EXCEPT I and J (must be the
  only record). F0 records cannot be mixed with non-F0 records.
- Connection must be terminated on: RCB for unstarted stream, mixed RCBs,
  undefined RCB, valid RCB out of sequence (e.g. B0 naming unstarted stream).
  Exception: request-to-initiate (90) for an already-active stream ->
  reply B0 (permission denied), do not drop connection.

### 1.2 BCB - Block Control Byte (Table 45)

| Bits | Hex | Meaning |
|------|-----|---------|
| 1000 cccc | 80-8F | Normal block, sequence count cccc (mod 16) |
| 1001 .... | 90 | Bypass sequence count validation ("BCB ignore") |
| 1010 cccc | A0-AF | Reset expected block sequence count to cccc |
| 1011 .... | B0 | Reserved |
| 11xx .... | C0-FF | Reserved |
| 0xxx xxxx | | invalid (high bit must be 1) |

Sequence logic:
- Both counters (inbound, outbound) = 0 after line init; modulo 16.
- Sender: outbound count incremented each time its block is acknowledged.
- Receiver: expects received count = last received + 1 (mod 16).
  - match: normal.
  - duplicate (== last received): peer lost our last block -> discard the
    duplicate and RETRANSMIT our last un-acked block.
  - otherwise: send RCB X'E0' (BCB sequence error) with SRCB = expected
    count; the receiver of E0 terminates the connection.
- ACK: always reply with a null block (BCB+FCS+RCB 00), not DLE ACK0, so the
  BCB count keeps advancing (Fig. 16/17).
- State table (Table 46) is BSC line-level (NAK/retransmit); for TCP only the
  BCB/duplicate logic matters.

### 1.3 FCS - Function Control Sequence (Table 47), 2 bytes

Bit set (1) = stream ENABLED; bit off = receiver asks sender to HOLD that
stream. FCS controls the inbound flow of the node that SENDS it.

Byte 0 (FCS1):

| Mask | Meaning |
|------|---------|
| 80 | must be 1 |
| 40 | Wait-a-bit: suspend ALL streams |
| 30 | reserved |
| 08 | SYSIN 1 |
| 04 | SYSIN 2 or SYSOUT 7 |
| 02 | SYSIN 3 or SYSOUT 6 |
| 01 | SYSIN 4 or SYSOUT 5 |

Byte 1 (FCS2):

| Mask | Meaning |
|------|---------|
| 80 | must be 1 |
| 40 | console (NMR) stream |
| 30 | reserved |
| 08 | SYSIN 5 or SYSOUT 4 |
| 04 | SYSIN 6 or SYSOUT 3 |
| 02 | SYSIN 7 or SYSOUT 2 |
| 01 | SYSOUT 1 |

- "All enabled, normal" FCS = X'8FCF'.
- Wait-a-bit only = X'CFCF' (FCS1 |= 0x40).
- Because bits are shared, max 8 SYSIN+SYSOUT streams concurrently each
  direction on BSC/CTC/TCP (SYSIN n + SYSOUT (9-n) share a bit).
- Do not send a request-to-initiate (90) for a stream whose FCS bit the peer
  has off; peer should answer B0 (receiver cancel).
- On receiving Wait-a-bit = 1: do NOT send data records in reply. Reply with a
  null buffer (preferred) or DLE ACK0. If the wait-a-bit came in a null buffer,
  delay 1-2 s before replying (never > 2 s; 3 s timeout). If it came with
  data, reply immediately.
- With NCCIMRCB agreed, use only the wait-a-bit and ignore per-stream bits.

### 1.4 SCB - String Control Byte, non-SNA (Table 48)

| Bits | Range | Meaning |
|------|-------|---------|
| 0000 0000 | 00 | End of record (EOR); always ends each compressed record |
| 0100 0000 | 40 | Terminate transmission of the stream (transmitter cancel) |
| 100b bbbb | 81-9F | insert bbbbb (1-31) blanks (X'40'); no data byte follows |
| 101d dddd | A1-BF | next 1 byte is duplicated ddddd (1-31) times |
| 11cc cccc | C1-FF | cccccc (1-63) uncompressed bytes follow |

- SCBs mandatory even for uncompressed data (use C1-FF literal SCBs).
- An SCB must appear at least every 64 bytes (literal run max 63).
- Counts of 0 in 80/A0/C0 forms: not defined; do not emit.
- Compaction is never used on BSC/CTC/TCP.
- Typical encoder: runs of >=2 blanks -> 8x; runs of >=3 identical
  non-blanks -> Ax c; else literal.

### 1.5 RCB - Record Control Byte (Table 49)

| Hex | Bits | Meaning | SRCB |
|-----|------|---------|------|
| 00 | 0000 0000 | End-of-block | none |
| 01-8F | | Reserved | |
| 90 | 1001 0000 | Request to initiate stream (RI) | RCB of stream to initiate |
| A0 | 1010 0000 | Permission to initiate stream | RCB of stream |
| B0 | 1011 0000 | Negative permission / receiver cancel (RC) | RCB of stream denied/cancelled (+ reason, sec 1.7) |
| C0 | 1100 0000 | Acknowledge transmission complete (EOF confirm / TC) | RCB of stream received |
| D0 | 1101 0000 | Ready to receive stream (receiver online) | RCB of stream receiver |
| E0 | 1110 0000 | BCB sequence error | expected BCB count (received count is in the BCB) |
| F0 | 1111 0000 | General control record (NCCR / signon) | EBCDIC letter, sec 1.6 |
| 98,A8,B8,C8,D8,E8,F8 | 1iii 1000 | SYSIN record, stream iii=1..7 | SYSIN control info |
| 99,A9,B9,C9,D9,E9,F9 | 1iii 1001 | SYSOUT record, stream iii=1..7 | SYSOUT control info |
| 9A | 1001 1010 | Operator command / console message (NMR) | X'80' |
| others (91-97, A1-F7 not listed, AA-FA, 9B-FF low nibble B-F) | | Reserved | |

Stream RCB formula: SYSIN n = 0x88 + n*0x10 (n=1..7 -> 98..F8);
SYSOUT n = 0x89 + n*0x10 (99..F9). Stream number 0 not allowed.

### 1.6 SRCB - Subrecord Control Byte (Tables 50, 51)

F0 (general control) SRCB = EBCDIC letter:

| Char | Hex | Meaning | Blocking |
|------|-----|---------|----------|
| A | C1 | reserved | |
| B | C2 | Network SIGNOFF | |
| C-H | C3-C8 | reserved | |
| I | C9 | Initial network SIGNON | only record in buffer |
| J | D1 | Response to initial SIGNON | only record in buffer |
| K | D2 | Reset network SIGNON | may be blocked with M/N/L; first in buffer |
| L | D3 | Accept (concurrence) SIGNON | |
| M | D4 | Add network connection | |
| N | D5 | Delete (subtract) network connection | |
| O-R | D6-D9 | reserved IBM | |
| S-Z | E2-E9 | unused | |

SYSIN (RCB 98-F8) SRCB:

| Binary | Hex | Meaning |
|--------|-----|---------|
| 1000 0000 | 80 | Standard (data) record |
| 1100 0000 | C0 | Job header |
| 1110 0000 | E0 | Data set header |
| 1101 0000 | D0 | Job trailer |
| 1111 0000 | F0 | Reserved |
| 0000 0000 | 00 | (EOF, sec 2.4) |
| 0100 0000 | 40 | (transmitter cancel, SNA RIDSRCB; see 2.5) |

SYSOUT (RCB 99-F9) SRCB, format `1 k cc ss 00`:

| Binary | Hex | Meaning |
|--------|-----|---------|
| 10cc ss00 | | data record; cc = carriage control, ss = spanning |
| cc=00 | 80 | no carriage control |
| cc=01 | 90 | machine carriage control (CCW opcode as 1st byte) |
| cc=10 | A0 | ASA carriage control |
| cc=11 | B0 | CPDS page-mode records (with CC) |
| ss=00 | +0 | standard, not spanned |
| ss=10 | +8 | first segment of spanned record |
| ss=01 | +4 | middle segment |
| ss=11 | +C | last segment |
| 1100 0000 | C0 | Job header |
| 1110 0000 | E0 | Data set header |
| 1101 0000 | D0 | Job trailer |
| 1111 0000 | F0 | Reserved |

E.g. ASA print line, unspanned = X'A0'; machine CC first segment = X'98'.

NMR (RCB 9A): SRCB X'80'. JES2 actually sends X'00' with NMRs (Table 55 note)
-> accept both.

Header/trailer segmentation (NJH/NDH/NJT internal segment layout is in the
headers chapter): each header segment is sent as its own record with SRCB
C0/E0/D0 and compressed with SCBs like any record; max 256 data bytes per
record, header must start a new buffer unless NCCIPACK.

### 1.7 B0 reason codes (Fig. 18) and wire format

Wire format (Table 52):

| Link | Bytes |
|------|-------|
| BSC/CTC/TCP | `B0 <stream RCB> C2 rc1 rc2 00` (SCB C2 = 2 literal bytes; then EOR) |
| SNA | RID `B0 <stream RCB> 01` then rc1 rc2 (SCB 05?) [AMBIG-6] |

A B0 with no reason: `B0 <stream RCB> 00`.

| rc1 | rc1rc2 | Meaning |
|-----|--------|---------|
| 04 | | Normal response to an ABORT |
| 08 | | Receiving system in SHUTDOWN |
| 0C | | RIF received for unsupported stream |
| | 0C04 | stream not supported (not issued by RSCS) |
| | 0C08 | stream drained |
| | 0C0C | unknown stream (NR) |
| | 0C10 | FCS conflict |
| 10 | | Resource lack |
| | 1004 | real storage (NR) |
| | 1008 | virtual storage |
| | 100C | spool space |
| | 1010 | CPU resources |
| 14 | | Operator command |
| | 1404 | HOLD command for connection |
| | 1408 | STOP command for connection |
| | 140C | FLUSH command for connection |
| 18 | | Protocol violation |
| | 1804 | last transmission not correctly terminated |
| | 1808 | compression/compaction error |
| | 180C | records out of sequence |
| | 1810 | mixed RCBs in a buffer |
| | 1814 | undefined RCB/SRCB combination |
| 1C | | Datastream error |
| 20 | | File rejected by installation exit / security |

When rc2 is not listed, send X'00'? Doc unclear [AMBIG-6].

---------------------------------------------------------------------------
## 1A. SNA buffer (for reference; not needed for TCP/IP)

- RU <= 65535; whole RU compressed (SCB at least every 64 bytes); SNA SCB:
  00cccccc reserved/"none", 10cccccc repeat c blanks, 11cccccc repeat next
  char c times, 01cccccc compacted. NOTE: encoding differs from non-SNA.
  Count 0 reserved.
- Each record prefixed by 3-byte RID: RIDRCB (same values as RCB),
  RIDSRCB (same as SRCB; for 90-D0 = stream RCB; for data streams also
  X'00' standard end, X'40' cancelled by transmitter), RIDRLEN
  (90-D0: 0; F0: topology length+3; data/NMR: record length-1, 0 = no data;
  01-FF = 2..256 bytes).
- Stream control record must be the only record in the RU.
- Max record 259 = 256 data + 3 RID. Max 28 concurrent streams (SNA).

---------------------------------------------------------------------------
## 1B. Data record content (applies to all links)

Unspanned (Fig. 20/21):
- LRECL includes the carriage control byte, excludes the length byte.
- Max unspanned record = 255 bytes incl. CCTL (254 data + 1 CC).
- SYSIN default: 80-byte fixed, no CC.
- Machine CC: any CCW opcode allowed; store-and-forward must pass intact.
- Trailing blanks may be truncated before transmission; receiver pads back
  to original LRECL (LRECL from data set header).
[AMBIG-2] on whether the 1-byte LRECL is on the wire for BSC/TCP.

Spanned (Fig. 22/23), SYSOUT SRCB ss bits, SYSIN needs NCCISSIN:
- Records up to 32760 bytes, split into segments < 256 bytes.
- Every segment starts with SEGL (1 byte, excludes itself and LRECL).
- First segment: SEGL(1) LRECL(2, total logical record length) [CCTL] data;
  max 253 bytes of data incl. CC.
- Later segments: SEGL(1) data; max 255 bytes.
- Segments may be individually blank-truncated (restore using SEGL), then
  concatenated and padded with blanks to the 2-byte LRECL.
- Blank truncation is done BEFORE segmentation; compression after.

---------------------------------------------------------------------------
## 2. Stream control (Ch. 9)

Streams: SYSIN 1-7 (jobs), SYSOUT 1-7 (output), plus NMR (9A) and control
(F0) which need no initiation. Max 8 SYSIN+SYSOUT concurrent per direction
on BSC/CTC/TCP (FCS); 28 on SNA. Full duplex: a stream can flow one way while
an unrelated stream flows the other; BSC/CTC is pseudo-duplex (flip-flop),
TCP behaves like BSC framing-wise.

### 2.1 Control records on BSC/CTC/TCP

| Name | Abbrev | Bytes in buffer (record) |
|------|--------|-------------------------|
| Request to initiate | RI | `90 <sRCB> 00` |
| Permission granted | PI | `A0 <sRCB> 00` |
| Receiver cancel / deny | RC | `B0 <sRCB> 00` or `B0 <sRCB> C2 rc1 rc2 00` |
| Transmission complete / EOF confirm | TC/EC | `C0 <sRCB> 00` |
| Receiver ready (online) | | `D0 <sRCB> 00` |
| End of file | EOF | `<sRCB> 00 00` (RCB=stream, SRCB=00, SCB=00 EOR) |
| Transmitter cancel | | `<sRCB> 40 00` per Table 58 / `<sRCB> xx 40` per Fig. 26 [AMBIG-3] |
| BCB error | | `E0 <expected count> 00` |

[AMBIG-1]: whether 90/A0/B0/C0/D0 carry a trailing SCB X'00'.

### 2.2 Normal stream (Fig. 24, 25)

```
SENDER                                RECEIVER
RI   90 <s>          ------------->
                     <-------------   A0 <s>          (or B0 <s> = denied)
data buffers <s> ... ------------->   (honour FCS bits / wait-a-bit)
EOF  <s> 00 00       ------------->
                     <-------------   C0 <s>          (or B0 <s> = abort file)
```
- Sender must not send data until A0 received.
- One job per RI unless multi-job (NCCIRIF) option.
- Sender may purge its spool copy only after C0. Stream is then closed.
- RI for an already active stream -> receiver returns B0.

### 2.3 Multi-job stream (NCCIRIF, X'08' in NCCIFEAT byte 0)

If both signons set NCCIRIF: after the first C0, the next job is sent without
a new 90/A0 exchange. Each job still ends with EOF -> C0 (or B0 to reject the
last job and terminate the stream). B0 in reply to RI => treat receiver as
drained until it sends D0 (receiver ready). B0 mid-transmission (with reason)
=> sender holds that file, may reopen stream for another object.

### 2.4 EOF (Table 57)

| Link | RCB | SRCB | SCB | LEN |
|------|-----|------|-----|-----|
| BSC | 98-F8 / 99-F9 | 00 | 00 | -- |
| SNA | 98-F8 / 99-F9 | 00 | -- | 00 |

### 2.5 Abnormal end

Transmitter cancel (Table 58):

| Link | RCB | SRCB | SCB | LEN |
|------|-----|------|-----|-----|
| BSC | stream | 40 | 00 | -- |
| SNA | stream | 40 | -- | 00 |

Receiver cancel (Table 59): BSC `B0 <stream> C2 rc1 rc2`, SNA RID
`B0 <stream> 01`.
EOF confirm (Table 60): `C0 <stream> 00` (table text garbled) [AMBIG-4].

Sender-initiated (Fig. 26):
```
SENDER: <s> transmitter-cancel(40)  -> RECEIVER
RECEIVER: B0 <s>                    -> SENDER  (stream now closed)
```
No more data on the stream until B0 received. A new RI is needed for the
next job. If cancel caused by system problem, keep job for retry.

Receiver-initiated (Fig. 27):
```
RECEIVER: B0 <s> [reason]  -> SENDER
SENDER:   <s> 40 (abort)   or  <s> EOF  -> RECEIVER
```

LRTS (default): RC may be answered with EOF; RC after EOF = EOF response.
JES2/RSCS hold the transmitter on RC; JES3/POWER requeue.

MRTS (NCCIMRTS, X'04' of NCCIFEAT, both I and J must set) [AMBIG-5]:
- Any RC requires a TC response (EOF is NOT valid).
- Only one RC per stream; sender discards everything after it until it
  gets the TC.
- EOF's only valid response is EC (C0).
- If receiver sends EOF while receiving RC, EOF ignored.
- Receiver drops connection with a sender that ignores an RC.
- RC = cancel of last job AND stream; sender must reply TC before new RI.

---------------------------------------------------------------------------
## 3. Connection control records (NCCR, Ch. 7 layouts + Ch. 10 protocol)

All F0 records: RCB X'F0', SRCB EBCDIC letter, uncompressed, no SCBs.

### 3.1 Initial signon "I" (primary -> secondary), Table 39

Total length X'29' = 41 bytes (NCCIDL = X'29' when NCCIFEAT present).

| Off hex | Off dec | Len | Field | Type | Value / meaning |
|---------|---------|-----|-------|------|-----------------|
| 00 | 0 | 1 | NCCRCB | bin | X'F0' |
| 01 | 1 | 1 | NCCSRCB | char | C'I' = X'C9' |
| 02 | 2 | 1 | NCCIDL | bin | length from RCB to end of record (X'29') |
| 03 | 3 | 8 | NCCINODE | char | primary node name, EBCDIC, blank padded (doc says off 4: typo) |
| 0B | 11 | 1 | NCCIQUAL | bin | member number 1-7 (default 0); JES2 = 1 |
| 0C | 12 | 4 | NCCIEVNT | bin | 0, not used in I |
| 10 | 16 | 2 | NCCIREST | bin | partial resistance. RSCS X'0000'; JES3/POWER/AS400 X'00C8'; JES2 sets |
| 12 | 18 | 2 | NCCIBFSZ | bin | max buffer size this node can RECEIVE (>300) |
| 14 | 20 | 8 | NCCILPAS | char | line password, blanks default (secure: NCCIPRAW random s1) |
| 1C | 28 | 8 | NCCINPAS | char | node password, blanks default |
| 24 | 36 | 1 | NCCIFLG | flags | X'00'; X'40' = NCCIFLGS secure (DES) signon |
| 25 | 37 | 4 | NCCIFEAT | bits | feature bits (byte 0 below) + 3 reserved bytes (26-28) |

NCCIFEAT byte 0 (offset X'25'):

| Mask | Name | Meaning | Who |
|------|------|---------|-----|
| 80 | NCCIPREP | BSC/CTC quiesce options | RSCS |
| 40 | NCCITRM | SNA termination options | none |
| 20 | - | reserved | |
| 10 | NCCIPACK | multiple NJE headers per buffer | RSCS, JES2 |
| 08 | NCCIRIF | omit RI/PI after first object in stream (multi-job) | RSCS |
| 04 | NCCIMRCB | mixed RCBs in one buffer | RSCS |
| 04 | NCCIMRTS | More Reliable Transport Service (Ch. 9) [AMBIG-5] | |
| 02 | NCCINOS | supports non-output SYSOUT | JES2 |
| 01 | NCCISSIN | supports spanned SYSIN (NDHCLREC 0-32767) | JES2 ("NCCIMDR") |

Older systems send a shorter NCCIDL (no NCCIFEAT) => assume no features.

### 3.2 Response signon "J" (secondary -> primary), Table 40

Same layout as I:

| Off hex | Len | Field | Value |
|---------|-----|-------|-------|
| 00 | 1 | NCCRCB | X'F0' |
| 01 | 1 | NCCSRCB | C'J' = X'D1' |
| 02 | 1 | NCCIDL | X'29' |
| 03 | 8 | NCCINODE | secondary node name |
| 0B | 1 | NCCIQUAL | member number (1 = JES2 node) |
| 0C | 4 | NCCIEVNT | CES: 0 if high end sends or secondary trunk; next higher if low end sends on primary trunk; X'FFFFFFFF' = predefined / non-NPM connection |
| 10 | 2 | NCCIREST | partial resistance |
| 12 | 2 | NCCIBUFSZ | max buffer secondary can receive |
| 14 | 8 | NCCILPAS | line password (secure: NCCIPRAW = random s2) |
| 1C | 8 | NCCINPAS | node password (secure: NCCIPENC = DES(s1)) |
| 24 | 1 | NCCIFLG | X'80' NCCIFLGM multi-trunk response; 3F reserved |
| 25 | 4 | NCCIFEAT | agreed features (I.FEAT AND own supported) |

Feature negotiation: secondary: J.FEAT = I.FEAT AND supported. Primary:
AFW = J.FEAT AND supported; if AFW != J.FEAT -> terminate line or just use
J.FEAT. Feature used only if set by both.

Buffer size: each side sends <= the size the other advertised
(NCCIBFSZ). Both >= 300, <= 32K.

Passwords: NCCILPAS (line) and NCCINPAS (node), 8 bytes EBCDIC, blank
padded, blanks = none. Comparison/validation rules are not specified in this
range; implement: if a password is configured, compare the received field
to it, else ignore. In secure mode (NCCIFLG X'40') those fields carry the
DES challenge/response and plain passwords are not used.

### 3.3 Reset signon "K", Table 41  [AMBIG-7 offsets]

| Off hex | Len | Field | Value |
|---------|-----|-------|-------|
| 00 | 1 | NCCRCB | F0 |
| 01 | 1 | NCCSRCB | C'K' = D2 |
| 02 | 1 | NCCCDL | record length |
| 03 | 4 | NCCCEVNT | CES; 0 if sent by high end or secondary trunk |
| 07 | 3 | - | reserved |
| 10 | 2 | NCCIREST | partial resistance |
| 12 | 8 | NCCCPENC | DES(s2) in secure signon |

Sent by secondary instead of J to bump CES / change resistance; may be
blocked with other NCC records but first in buffer.

### 3.4 Concurrence signon "L", Table 42  [AMBIG-7]

| Off hex | Len | Field | Value |
|---------|-----|-------|-------|
| 00 | 1 | NCCRCB | F0 |
| 01 | 1 | NCCSRCB | C'L' = D3 |
| 02 | 1 | NCCCDL | length |
| 03 | 4 | NCCCEVNT | CES copied from the J/K being acknowledged |
| 07 | 2 | - | reserved |
| 10 | 2 | NCCCREST | total node-to-node resistance |
| 12 | 8 | NCCCPENC | DES(s2) in secure signon |

Only JES2/JES3 use L (JES3 only on TCP/IP). Only JES2 uses K, M, N.

### 3.5 Add "M" / Subtract "N" connection, Table 43 (27 bytes = X'1B')

| Off hex | Len | Field | Value |
|---------|-----|-------|-------|
| 00 | 1 | NCCRCB | F0 |
| 01 | 1 | NCCSRCB | C'M' (D4) add / C'N' (D5) subtract |
| 02 | 1 | NCCADL | length (X'1B') |
| 03 | 8 | NCCANODA | low-end node name |
| 0B | 1 | NCCAQULA | low-end member 1-7 |
| 0C | 8 | NCCANODB | high-end node name |
| 14 | 1 | NCCAQULB | high-end member 1-7 |
| 15 | 4 | NCCAEVNT | CES |
| 19 | 2 | NCCAREST | total resistance 0-8000 |

Forward if CES newer; ignore and don't forward if already known. May be
blocked with K/L. A non-NPM node discards all NCC records except B, I, J.

### 3.6 Signoff "B", Table 44

`F0 C2` (RCB, SRCB only; no length byte documented) [AMBIG-1]. After sending,
sender resets the line for signon/drain; receiver prepares for new signon.
May be sent with streams active or idle. Abnormal disconnection = just drop.

### 3.7 Connection protocols (Ch. 10)

Primary on BSC/CTC = node that RECEIVES DLE ACK0 (for TCP see Ch. 11 OPEN
handshake). SNA: higher node name is primary.

Non-NPM (what a Go node should implement), Fig. 28:
```
Primary  --- I (feat, bufsz, pw) --->  Secondary
Primary  <-- J (CES=X'FFFFFFFF') ---   Secondary
```
Plain I / J is enough with JES2 if JES2 treats us as non-path-manager
(CONNECT NETSRV/JES2 NODE PATHMGR=NO). Our node must DISCARD M, N, K, L if
received (or answer per NPM rules if we implement NPM).

NPM (JES2<->JES2), CES = high 4 bytes of TOD clock, set by LOW end
(lower EBCDIC node name):

| Protocol | Flow |
|----------|------|
| Full primary, low initiates (BSC/TCP) Fig.29 | low I -> high J(CES=0) -> low K(CES!=0) -> high L |
| Full primary, high initiates (SNA, BSC, TCP) Fig.30 | high I -> low J(CES!=0) -> high L |
| Secondary trunk Fig.31 | I -> J(CES=0, NCCIFLGM=1) -> L |
| Trunk reset Fig.32 | low K(CES!=0) -> high L |
| Secure, high initiates Fig.33 | I(s1) -> J(e_s1, s2) -> L(e_s2) |
| Secure, low initiates | as Fig.29 but e_s2 in K |
| Secure non-NPM Fig.36 | I(s1) -> J(e_s1,s2, CES=FFFFFFFF) -> L(e_s2) |
| Secure secondary trunk Fig.34 | I(s1) -> J(e_s1,s2, CES=0) -> L(e_s2) |

After signon, each NPM sends M records about the new connection on other
lines; disconnection -> N records.

Signon rules: I and J must be alone in their buffer; extra signons on an
active session are ignored.

---------------------------------------------------------------------------
## 4. End-to-end flows (BSC-style buffers, as carried over TCP)

Notation per buffer: `[BCB FCS1 FCS2 | records | 00]`.
Counts shown assuming counters start at 0. [AMBIG-8 first BCB]

### 4.1 Start connection (we are primary, non-NPM)
```
(TCP OPEN/ACK per Ch. 11)
US   -> [80 8F CF | F0 C9 29 <node8> 01 00000000 00C8 <bfsz> <lpw8> <npw8> 00 <feat4> | 00]
PEER -> [80 8F CF | F0 D1 29 <node8> 01 FFFFFFFF xxxx <bfsz> <lpw8> <npw8> 00 <feat4> | 00]
validate J.NCCINODE == expected peer; passwords; AND features; buffer size
= min(own max, peer NCCIBUFSZ)
then idle loop: exchange null buffers [BCB 8F CF | 00] as ACKs
```
If we are secondary: wait for I, answer J with CES X'FFFFFFFF'.

### 4.2 Send a SYSOUT file to remote (we transmit on SYSOUT stream 1 = X'99')
```
US   -> [8n FCS | 90 99 00 | 00]                      RI
PEER -> [8m FCS | A0 99 00 | 00]                      PI  (B0 99 .. = denied)
US   -> [8n FCS | 99 C0 <SCB-compressed NJH seg 1> 00 | 00]  job header
          (each header segment its own record; separate buffer per header
           unless NCCIPACK)
US   -> [.. | 99 E0 <NDH...> 00 | 00]                 data set header
US   -> [.. | 99 A0 <ASA line> 00  99 A0 <line> 00 ... | 00]  data (SRCB 80/90/A0/B0, +8/4/C if spanned)
        (repeat NDH + data for each data set)
US   -> [.. | 99 D0 <NJT> 00 | 00]                    job trailer
US   -> [.. | 99 00 00 | 00]                          EOF
PEER -> [.. | C0 99 00 | 00]                          TC -> now purge job
```
Before each data buffer check peer's last FCS: SYSOUT 1 bit (FCS2 X'01')
on and wait-a-bit off; else send null buffers.

### 4.3 Receive a SYSOUT file
```
PEER -> 90 <s>       ; if s free and accepting: A0 <s>; else B0 <s> [C2 rc]
                       (already active -> B0; unknown/undefined -> drop conn)
PEER -> records with RCB=<s>: SRCB C0 = NJH segment, E0 = NDH, D0 = NJT,
        10ccss00 = data record (decompress SCBs until 00; handle spanning)
        Set FCS bit for <s> off to throttle.
PEER -> <s> 00 00    ; EOF -> commit to spool, send C0 <s>
PEER -> <s> 40 ..    ; transmitter cancel -> discard, send B0 <s>
```
SYSIN (job) streams identical with RCB 98-F8 and SYSIN SRCBs.

### 4.4 Send NMR message/command
No stream initiation. One record: `9A 80 <SCB-compressed NMR record> 00`
(JES2 sends SRCB 00 -> accept 00 or 80). Console FCS bit = FCS2 X'40'; do
not send NMR while peer has that bit off or wait-a-bit on. NMR record layout
(NMRFLAG, NMRLEVEL, NMRTYPE, NMRML, NMRTO, NMROUT, NMRFM, message) is in
Ch. 6 (other note).

---------------------------------------------------------------------------
## 5. Ambiguities / doc errors

1. F0 records "end by the data length byte", but B (signoff) has only RCB
   SRCB in Table 44. Stream-control records 90/A0/B0/C0/D0: manual never
   explicitly says whether an EOR SCB X'00' follows the SRCB; the B0-with-
   reason form (`B0 s C2 rc1 rc2`) implies SCB framing. Recommend: send
   `RCB SRCB 00` for stream control; tolerant parser (treat 00 after SRCB as
   EOR, and accept EOB directly after SRCB). Verify with JES2 trace.
2. Fig. 20/21 show a 1-byte LRECL at offset 0 of a data record. For SNA this
   corresponds to RIDRLEN; for BSC/TCP the length is implied by SCB
   decompression. Most likely no length byte on BSC wire for unspanned
   records - verify with trace. Spanned SEGL/LRECL ARE part of the data.
3. Transmitter cancel: Table 58 says SRCB X'40' SCB X'00'; Fig. 26 and the
   non-SNA SCB table say SCB X'40' ("terminate stream"). Note on p.164 says
   SNA uses RIDSRCB X'40' "rather than an SCB of X'40'" -> BSC form is
   probably `RCB(stream) SRCB SCB=40`. Accept both on receive.
4. Table 60 (EOF confirm) BSC row is garbled ("99-F9 -- 00 00"); correct
   form per Table 49 is `C0 <stream RCB>`.
5. NCCIMRTS = X'04' (Ch. 9) collides with NCCIMRCB = X'04' (Table 39). Also
   "JES2 only supports NCCIMDR (bit X'1')" - name not defined; X'01' is
   NCCISSIN. Recommend: do not set X'04' to JES2 (JES2 doesn't use MRCB).
6. B0 SNA form "B0 9x 05 01 rc1 rc2" lists 05 under SCB. Rule for single-
   byte reasons (04, 08, ...) - whether rc2 sent as 00 - not stated; send
   rc1 00.
7. K and L layouts: offsets 07 (reserved 3/2), 10 (resistance), 12 (PENC)
   leave a gap 0A-0F and the L "NCCCEVNT" says "copied from concurrence
   record" (self-reference; likely from K/J). Treat K/L by NCCCDL length and
   verify against JES2 $NCCR macro / trace. Non-NPM nodes can ignore them.
8. First BCB after line start: counters start 0; doc does not say whether
   the I record's BCB is X'80' or X'90'. Recommend sending X'80'-based with
   count 0 and accepting X'90' (ignore) and A0-AF (reset) from peer.
9. NCCIDL is "char" for I and "binary" for J - it's binary. NCCINODE offset
   4 in Table 39 is a typo (must be 3: NCCIQUAL is at X'0B').
10. NCCIPRAW listed at X'14' overlaps NCCILPAS (secure signon reuses the
   password fields); same for NCCIPENC at X'1C' in J.
11. Chapter text "J record contains CES of minus one (X'FFFFFFF')" - 7 F's
   typo; it is X'FFFFFFFF'.
12. Table 51 lists SYSIN SRCB "1111 0000" twice (reserved).
