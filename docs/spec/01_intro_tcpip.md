# 01 – NJE intro, layers, and TCP/IP transport (SA32-0988-02)

Sources: has2y211.txt lines 575-1281 (Ch1-2), 10488-10775 (Ch12), 10775-11300
(App A JES2/JES3), plus cross-refs: Ch7 NCCR (7110-7330), Ch8 BCB/FCS/SCB/RCB
(7400-7760), Ch11 BSC/CTC init (8700-8880, 8950), App A VSE/POWER TCP (12261).

Conventions: all multi-byte binary = **big-endian**. All character fields =
**EBCDIC (CP037-ish), blank = X'40'**, left-aligned, blank-padded. Nothing in NJE
is ASCII (incl. TCP control records). "ICF" = inferred from common practice
(RSCS/JES2/HNET/funetnje/NJElib), NOT stated in the manual.

---------------------------------------------------------------------------
## 1. Overview (Ch 1-2)

### 1.1 Units of work ("NJE transfer units")
| Unit | Structure | Notes |
|---|---|---|
| SYSIN job | Job header, [RCCS/DSH], JCL+SYSIN records, Job trailer | Records nominally 80 bytes fixed; trailing blanks may be truncated. RCCS (record char. change section, LRECL+RECFM U/F/V) precedes each SYSIN data set if LRECL changes. JES2 max SYSIN rec = 252 bytes; no spanning of SYSIN (unless NCCISSIN feature). JES2 accepts only ONE job per header/trailer. |
| SYSOUT job | Job header, {Data set header(s), SYSOUT data}..., Job trailer | One or more DSHs before every data set (multiple DSHs = multiple destinations). |
| NMR (nodal message record) | NMR header + text (message or command) | Max 256 bytes total. Not spanned. Fits in one buffer. Command types: unformatted (system-dependent text, e.g. `$D...`) or formatted (DISPLAY/CANCEL/HOLD/RELEASE/REROUTE). Message NMRs may carry timestamp/orig-userid prefix (flag in header). RCB=X'9A'. JES2 sends SRCB=X'00' on NMRs. |
| NCCR (connection control rec) | RCB X'F0' + SRCB type char | I,J,K,L,M,N,B (see 3.5). |

Job header/trailer/DSH = variable-length records made of variable-length
sections. Transparent data (no translation). Headers spanned in <=256-byte
segments (JES2 segments control records in 253-byte pieces).

### 1.2 Node roles
Originating, Intermediate (store-and-forward), Target = Execution (runs JCL /
command NMR) and/or Destination (prints SYSOUT / displays message). Each node
has a unique 1-8 char EBCDIC node name used in headers and NMRs.
Addressing: `node`, `node.remote`, `node.userid`. JES2/JES3 convention
`node.IP:ip_addr` routes to TCP/IP workstation (not our concern).

### 1.3 Layers (top -> bottom)
1. Formats of NJE transfer units (headers/trailers/NMR/NCCR) – Ch 2-7
2. Presentation services (segmentation <=256B, SCB compression, buffer build) – Ch 8
3. Stream control (multileaving, FCS, RCB stream permission) – Ch 9
4. NJE connection control (I/J/B signon, NPM records) – Ch 10
5. Link protocols (BSC/CTC/SNA; TCP/IP = Ch 12, a wrapper around CTC/BSC buffers)

### 1.4 Stream summary (multileaving)
Per direction: up to 7 SYSIN + 7 SYSOUT streams + 1 NMR (console) stream + 1
control stream; FCS limits to 8 SYSIN+SYSOUT concurrently. JES2: <=7 job,
<=7 SYSOUT transmitters per line, sum <=8. JES3: 1 SYSIN + 1 SYSOUT.

| RCB | Meaning | SRCB |
|---|---|---|
| X'00' | End-of-block (stand-alone, after last record in buffer) | none |
| X'90' | Request to initiate stream | RCB of stream |
| X'A0' | Permission to initiate stream | RCB of stream |
| X'B0' | Negative permission / receiver cancel | RCB of stream |
| X'C0' | Acknowledge transmission complete | RCB of stream |
| X'D0' | Ready to receive stream | RCB of stream |
| X'E0' | BCB sequence error | expected count |
| X'F0' | General control record (NCCR) | EBCDIC 'I','J','K','L','M','N','B' |
| X'98'..X'F8' (1iii1000) | SYSIN record, stream iii=1..7 | X'80' std rec, X'C0' job hdr, X'E0' DSH, X'D0' job trailer |
| X'99'..X'F9' (1iii1001) | SYSOUT record, stream iii=1..7 | 10cc0000 carriage ctl type, + hdr/trailer/DSH codes (see Ch8 notes) |
| X'9A' | Operator command / console message (NMR) | X'00' (JES2) |
Others reserved -> unrecognized RCB => terminate connection (all streams).
EOF of a stream = record with stream RCB and SRCB X'00'.

---------------------------------------------------------------------------
## 2. BSC/CTC buffer format (what TCP carries) – Ch1 + Ch8

```
[DLE STX]* BCB FCS1 FCS2 { RCB SRCB SCB data SCB data ... SCB=00 } ... RCB=00
```
*BSC framing; see 3.4 for what survives over TCP.

| Field | Len | Value |
|---|---|---|
| BCB | 1 | `1000cccc` normal, cccc = seq mod 16; X'90' = bypass seq check ("BCB ignore"); `1010cccc` reset expected count to cccc. Counters start at 0 after line init. Outbound incremented per ACK received. Duplicate BCB => peer lost our last block: resend it, discard dup. Wrong BCB => send RCB X'E0' (SRCB = expected count), receiver of E0 terminates. |
| FCS | 2 | `1w00 aaaa  1c00 bbbb`-ish: byte0 bit0 and byte1 bit0 must be 1 (X'8080' base). byte0 X'40' = wait-a-bit (suspend all). byte0 X'08',X'04',X'02',X'01' = SYSIN1-4 / SYSOUT7..5; byte1 X'08',X'04',X'02',X'01' = SYSIN5-7/SYSOUT4..1; byte1 X'40' = console. Bit ON = stream enabled. |
| RCB,SRCB | 1+1 | per table 1.4 |
| SCB | 1 | `00 000000` end-of-record (EOR); `01 000000` terminate stream (transmitter cancel, X'40'); `100bbbbb` insert b blanks; `101ddddd` dup next char d times; `11cccccc` c (<=63) literal bytes follow. SCB required at least every 64 bytes, even if uncompressed. |
| EOB RCB | 1 | X'00' after last record; buffer truncated there |

- F0 (NCCR) records are NOT SCB-compressed: RCB SRCB LEN(1 byte, from RCB to end) + body. I and J must be ONLY record in buffer. Other F0s may be blocked together but not with non-F0 records.
- Without NCCIMRCB feature, a buffer may contain records of only one RCB.
- "Null buffer" = BCB+FCS+RCB X'00' (no records) — used as ACK / idle. IBM prefers null buffers over DLE ACK0 as ack (BCB detects loss). JES2 uses DLE ACK0 when no data.
- Wait-a-bit received: respond with null buffer or DLE ACK0, never data.
- Buffer size negotiated via NCCIBFSZ in I/J (must be > 300).

---------------------------------------------------------------------------
## 3. TCP/IP transmission (Ch 12)

Supported: RSCS 3.2+, VSE/POWER 6.5+, JES2, JES3. Port **175** default.

### 3.1 Connection establishment
- Both sides passive-LISTEN on their port. Either side may CONNECT.
  The connecting side ("Side A", client) cancels its own listen for that link
  (fig 58) and sends **OPEN**; the accepting side ("Side B", server) replies
  **ACK** (or NAK). Control records are the FIRST bytes on the socket, sent
  raw (NO TTB/TTR wrapper).
- Inbound connection not matching any outstanding listen/link: CLOSE socket.
- Link state stays ACTIVE until I/J signon completes, then CONNECT.

Server-side validation of received OPEN:
| Condition | Response |
|---|---|
| Control record malformed/invalid | close socket (no NAK) |
| Valid but no such link / not expected session (RHOST unknown, OHOST/OIP not us) | NAK R=X'01' |
| Link exists and already connected (assume dead, silent severance) | NAK R=X'02', reset/terminate driver (may restart) |
| Link itself is attempting an active open (connect collision) | NAK R=X'03' + close socket |
| Temporary condition, retry allowed | NAK R=X'04' + close socket |
Doc wording for X'03'/X'04' says "close socket and send NAK" — implement as
send NAK then close.

Client action on received NAK:
| R | Action |
|---|---|
| X'01' | terminate session, no restart |
| X'02',X'03',X'04' | close socket, wait random N seconds, retry connect |

### 3.2 IPv4 control record (fig 59) – 33 bytes, no header
| Off | Len | Field | Encoding |
|---|---|---|---|
| 0 | 8 | TYPE | EBCDIC, blank-padded: `OPEN`=D6D7C5D540404040, `ACK`=C1C3D24040404040, `NAK`=D5C1D24040404040; SSL: `OPEN SSL`=D6D7C5D540E2E2D3 |
| 8 | 8 | RHOST | EBCDIC node name of SENDER (= RSCS LOCAL name for link), blank-padded |
| 16 (X'10') | 4 | RIP | sender IPv4, binary network order (128.112.14.1 = X'80700E01') |
| 20 (X'14') | 8 | OHOST | EBCDIC node name of intended RECEIVER |
| 28 (X'1C') | 4 | OIP | receiver IPv4, binary |
| 32 (X'20') | 1 | R | NAK reason (binary); X'00' for OPEN/ACK (ICF) |
Total 33 (X'21'). Fig 59 is a 16-byte-wide grid giving exactly these widths.

ACK contents (ICF, manual explicit only for IPv6): RHOST/RIP = ACK sender
(server), OHOST/OIP = original OPEN sender. Receiver of OPEN uses OHOST/OIP to
verify the connection reached the right site, RHOST to find link definition.
RHOST/OHOST are NJE NODE names (manual calls them "host"; for RSCS it is the
LOCAL node id) — not DNS names.

### 3.3 IPv6 control record (fig 62) – 536 bytes
| Off | Len | Field | Encoding |
|---|---|---|---|
| 0 | 8 | TYPE | EBCDIC `OPENNJE `, `ACKNJE  `, `NAKNJE  ` |
| 8 | 1 | VERSION | X'00' IPv6 signon w/ host names; X'01' IPv6 signon for AT-TLS conn.; others future |
| 9 | 8 | RNODENM | sender NJE node name, EBCDIC blank-padded |
| 17 (X'11') | 255 | RHOSTNM | sender host name (DNS/IP text), EBCDIC blank-padded |
| 272 (X'110') | 8 | ONODENM | receiver NJE node name |
| 280 (X'118') | 255 | OHOSTNM | receiver host name |
| 535 (X'217') | 1 | R | NAKNJE reason, same codes as IPv4 |
ACKNJE: R* fields = ACK sender, O* = OPEN sender (manual text garbled:
"RHODENM and RHOSTNM values based on the sending node"). If RHOSTNM=`::1`
(loopback), OHOSTNM set to current host name. On NAKNJE, TCP circuit closed.
Use IPv4 33-byte form unless peer configured for IPv6 form (no auto-detect
described; ICF: first 8 bytes distinguish `OPEN    ` vs `OPENNJE `).

### 3.4 Data framing after ACK: TTB / TTR (fig 61, 63, 64)
Every transmission block except control records:
```
TTB | TTR | rec | TTR | rec | ... | TTR(EOB)
```
**TTB – block header, 8 bytes** (fig 63, mislabelled "TTR" in the manual):
| Off | Len | Field | Value |
|---|---|---|---|
| 0 | 1 | F (flags) | X'00' — "currently not used" |
| 1 | 1 | U | X'00' unused |
| 2 | 2 | LN | big-endian; TOTAL block length INCLUDING the 8-byte TTB and the final 4-byte EOB TTR |
| 4 | 4 | UNUSED | X'00000000' reserved |

**TTR – record header, 4 bytes** (fig 64; field descriptions missing from text):
| Off | Len | Field | Value |
|---|---|---|---|
| 0 | 1 | F | flags; X'00' (ICF; manual gives no values) |
| 1 | 1 | U | X'00' |
| 2 | 2 | LN | big-endian length of following record, EXCLUDING the TTR (ICF) |
EOB TTR = 4 bytes of X'00' (LN=0) terminates the block.

Contents of each TTR record (ICF – manual says only "CTC NJE data buffers are
transferred"): one BSC/CTC transmission buffer with BSC line control stripped:
- Kept: **DLE STX (X'10 02')**, BCB, FCS(2), RCB/SRCB/SCB-compressed records,
  NCCR F0 records, trailing RCB X'00' EOB.
- Kept as bare 2-byte records: **SOH ENQ X'01 2D'**, **DLE ACK0 X'10 70'**,
  **SYN NAK X'32 3D'**, NAK X'3D' (unlikely over TCP).
- Stripped vs BSC: SYN fill, DLE ETB / ETX trailer, CRC/BCC, DLE transparency
  doubling (no DLE-DLE), PAD chars. TCP gives reliability so no NAK/retry.
- I/J signon buffer = DLE STX + BCB + FCS + F0 'I' rec(uncompressed) + X'00'.
- Usually one record per block (ICF); block may hold several TTRs.
Max TCP send/receive buffer = **8192**. TCP reads are a byte stream: read TTB
(8), then loop reading TTR(4)+LN until EOB TTR; handle short reads.
VSE/POWER "empty buffer" keepalive = block with zero user data (ICF:
TTB LN=12 + EOB TTR, or a null NJE buffer).

### 3.5 Signon after OPEN/ACK
Client (connector) chooses role:
- sends **SOH ENQ** => wants to be primary. Server replies **DLE ACK0** =>
  client is primary, client sends **I** record, server replies **J**.
  If server instead replies SOH ENQ (wants primary)... doc: if anything other
  than DLE ACK0 received for SOH ENQ, client (JES) closes connection.
- sends **SYN NAK** => either role OK. Server (JES) sends SOH ENQ, client
  replies DLE ACK0, server is primary and sends I; client replies J.
  If client answers with SOH ENQ instead of DLE ACK0, server terminates.
- JES never sends SYN NAK itself but handles it. Server receiving neither
  SOH ENQ nor SYN NAK => close.
Sequences (NodeA=JES):
```
A connects:  A->B Connect, OPEN; B->A ACK; A->B SOH ENQ; B->A DLE ACK0; A->B I; B->A J
B connects:  B->A OPEN; A->B ACK; B->A SOH ENQ; A->B DLE ACK0; B->A I; A->B J
         or: B->A SYN NAK; A->B SOH ENQ; B->A DLE ACK0; A->B I; B->A J
```
NPM (JES2 path manager) variants: low end initiation (full primary trunk) is
BSC/TCP only and includes L (concurrence) record: Low(primary) sends I(s1),
High replies J(e_s1,s2), Low sends L(e_s2) (fig 60). Non-JES2 nodes: JES2 link
must be PATHMGR=NO -> only I, J, B sent; a non-JES2 node must discard NPM
records other than I/J/B (K,M,N; L only JES2/JES3, JES3 uses L only on TCP).
High/low end = compare EBCDIC node names; high = primary (JES2-JES2).

Initial signon (I) record – RCB X'F0', SRCB C'I' (X'C9'), uncompressed:
| Off | Len | Field | Notes |
|---|---|---|---|
| 0 | 1 | NCCRCB | X'F0' |
| 1 | 1 | NCCSRCB | C'I' X'C9' |
| 2 | 1 | NCCIDL | binary length RCB..end |
| 3 | 8 | NCCINODE | EBCDIC primary node name (manual table says offset 4 — typo; 3 is consistent with B for QUAL) |
| B | 1 | NCCIQUAL | member number 1-7 (0 default) |
| C | 4 | NCCIEVNT | 0 for I (CES) |
| 10 | 2 | NCCIREST | resistance: RSCS X'0000', JES3/POWER/AS400 X'00C8' |
| 12 | 2 | NCCIBFSZ | max buffer sender can receive (>300) |
| 14 | 8 | NCCILPAS | line password EBCDIC, blanks |
| 1C | 8 | NCCINPAS | node password EBCDIC, blanks |
| 24 | 1 | NCCIFLG | X'00' (X'40' = secure signon) |
| 25 | 4 | NCCIFEAT | feature bits byte 0: X'80' PREP, X'40' TRM, X'10' PACK (multi hdr/buffer), X'08' RIF, X'04' MRCB, X'02' NOS, X'01' SSIN |
Manual also lists "26 3 reserved" (overlaps NCCIFEAT — ambiguous). Typical
NCCIDL = X'29' (41) per ICF; verify against Ch7 notes.
J record: same layout, SRCB C'J' (X'D1'), NCCINODE = secondary node, NCCIEVNT
(0 normally; X'FFFFFFFF' predefined connection), NCCIFLG X'80' = multi-trunk.
I and J are each the only record in their buffer. Signoff = F0 'B' (X'C2'); no
reply expected.

### 3.6 SSL / TLS
TYPE of first control record = `OPEN SSL` (EBCDIC, one blank). Encryption
starts after ACK is exchanged (control records in clear). IPv6 VERSION X'01' =
AT-TLS. VSE/POWER, JES2, JES3 support SSL.

### 3.7 Idle / keepalive
- JES2 (App A): full duplex over TCP, always keep a receive posted; send as
  soon as buffer packed. After 10 consecutive idle exchanges (ACK / null
  buffers), delay 100 ms before next send; +100 ms per further idle I/O, max
  1 s. => Idle link = ping-pong of null buffers (or DLE ACK0) with backoff.
- VSE/POWER: sends an "empty" buffer every 2 minutes when idle (zero user data
  length) to survive middleboxes.
- BSC timeout of 3 s on wait-a-bit response is BSC-specific; no TCP timer given.

### 3.8 Generalized control records
JES2 supports "generalized NJE control records" (Ch12 end) — figure content not
extracted; layout unknown from this text. Ignore/discard unless seen.

---------------------------------------------------------------------------
## 4. App A items relevant to us
- JES2 NCCR: I,J,K,L,M,N,B; I and J alone in buffer; user SRCB S-Z allowed.
- JES2 NMR SRCB X'00'. Unformatted cmd NMRs from `$N`; formatted from `$G`.
  Messages via `$DM`, command responses, notifications.
- JES2 exits 56/57 = TCP/IP NJE header/trailer xmit/receive.
- JES2 undefined destination node => job queued locally, no error.
- JES2 jobid = halfword; receiver tries to reuse original jobid.
- JES2 SYSIN max rec 252, written in 256-byte segments.
- JES3: TCP/IP supported; only I,J,L,B; L only for TCP/IP. NMR command to JES3
  > 59 bytes rejected (INTERCOM 80-byte buffer). Only *I / *F commands allowed.
- TSO TRANSMIT files: external writer name = remote/userid in DSH; records
  passed as F 80 without carriage control.
- Feature table: TCP/IP supported by JES2, JES3, RSCS, VSE/POWER (not BDT).

---------------------------------------------------------------------------
## 5. Ambiguities / gaps
1. TTR flag values and whether TTR LN excludes the 4-byte header: not in text
   (fig 64 has no descriptions). ICF: F=0, LN excludes header.
2. Whether DLE STX prefix is retained inside TTR data: not stated; ICF yes.
3. Figure captions swapped: fig 63 "Block Header (TTR)" is the 8-byte TTB.
4. R byte for OPEN/ACK unspecified (send X'00'). Whether the RHOST/OHOST is
   swapped in ACK is only explicit for IPv6.
5. NAK X'03'/X'04' text says "close socket and send NAK" — order impossible;
   send then close. NAK X'01'/X'02': socket close not stated (ICF close).
6. I record NCCINODE offset 4 (typo, use 3); reserved "26 3" overlaps NCCIFEAT.
7. IPv6 record field "RNODENUM"/"RHODENM" typos = RNODENM.
8. Role contention when both connect simultaneously: NAK X'03' to the inbound
   one; client retries after random delay.
9. Generalized control record layout absent.
10. No explicit TCP idle timeout; keepalive behaviour product-specific.
