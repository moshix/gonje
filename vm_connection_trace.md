# Connecting gonje to z/VM RSCS: trace and required changes

Status: 2026-11-09. Peer: z/VM RSCS node **ZVM72MSH** (zvm72.moshix.tech,
162.218.231.211). Our node: **3270BBS** on moshix.tech, public address
35.192.26.118 (gonje runs with `remote_natout=35.192.26.118`, listening on
port 1750).

## 1. RSCS link definition

    SMSG RSCS DEFINE 3270BBS TYPE TCPNJE PARM HOST=35.192.26.118 RMTPORT=1750 ITO=100

- `RMTPORT=1750` is the port RSCS **calls** (our gonje).
- RSCS also tries to **listen** on its own local port. Without `LCLPORT` it
  defaults to 175, which is privileged:

      DMTTNE188E Link 3270BBS unable to listen on port 175 error number=13 (Permission denied)

  So nothing on z/VM accepts calls: every outbound attempt from gonje to
  zvm72:1175 (or 175) gets "connection refused". Only RSCS calling us works.
  **Fix on z/VM:** add `LCLPORT=1175` to the link `PARM`, or reserve port 175
  for the RSCS virtual machine in `PROFILE TCPIP` (`PORT` statement).

## 2. What happened with gonje 0.8 / 0.9 (RSCS calls us)

All attempts follow the same handshake; only the signon I differs.

    RSCS -> gonje  OPEN    ZVM72MSH RIP=0.0.0.0  OHOST=3270BBS OIP=35.192.26.118
    gonje -> RSCS  ACK     3270BBS  RIP=35.192.26.118 OHOST=ZVM72MSH OIP=0.0.0.0
    RSCS -> gonje  00 00 00 12 00 00 00 00 | 00 00 00 02 | 32 3D | 00 00 00 00   SYN NAK
    gonje -> RSCS  ... 01 2D ...                                                    SOH ENQ
    RSCS -> gonje  ... 10 70 ...                                                    DLE ACK0
    gonje -> RSCS  signon I                                                         (see below)
    RSCS           resets the connection ~40 ms later

TTB/TTR framing of RSCS's own blocks is identical to gonje's (TTB length =
whole block incl. the 8-byte TTB and the end TTR; TTR length = record only).

### 2a. gonje I with DLE STX (TTR length 0x2F = 47)

    00 00 00 3F 00 00 00 00 | 00 00 00 2F |
    10 02 80 8F CF F0 C9 29 F3 F2 F7 F0 C2 C2 E2 40 01 00 00 00 00 00 00 20 00
    40 40 40 40 40 40 40 40 40 40 40 40 40 40 40 40 00 00 00 00 00 | 00 | 00 00 00 00
                                                                   ^^ end-of-buffer RCB

RSCS console:

    DMTTNE945E Protocol error detected by link 3270BBS, code 1818 -- link being deactivated
    DMTTNE945E Link deactivated -- data being received exceeds expected length

### 2b. gonje I without DLE STX (TTR length 0x2D = 45)

    80 8F CF F0 C9 29 F3 F2 F7 F0 C2 C2 E2 40 01 ... 00 00 00 00 00 | 00

RSCS console:

    DMTTNE182I Link 3270BBS session established
    DMTTNE916E Invalid NJE signon connection record received -- link 3270BBS is being deactivated

This looked like progress but is a red herring: RSCS expects DLE STX (see 3),
so without it every field is read two bytes off and the record is "invalid".

## 3. The reference: Sine Nomine NJE signing on to the same RSCS

From `/var/log/nje/nje.log` on moshix.tech (node RELAY, 2026-11-09 20:59:56).
RSCS called in, sent SYN NAK, Sine Nomine took primary:

    OPEN    ZVM72MSH 0.0.0.0  RELAY    35.192.26.118          (RSCS -> NJE)
    ACK     RELAY    35.192.26.118 ZVM72MSH 0.0.0.0          (NJE -> RSCS)
    < 00 00 00 12 00 00 00 00 00 00 00 02 32 3D 00 00 00 00   SYN NAK
    > 00 00 00 12 00 00 00 00 00 00 00 02 01 2D 00 00 00 00   SOH ENQ
    < 00 00 00 12 00 00 00 00 00 00 00 02 10 70 00 00 00 00   DLE ACK0

Signon I sent by Sine Nomine (accepted), TTB 0x3E = 62, TTR 0x2E = 46:

    > 00 00 00 3E 00 00 00 00 00 00 00 2E 10 02 80 8F
    > CF F0 C9 29 D9 C5 D3 C1 E8 40 40 40 01 00 00 00
    > 00 00 00 20 00 40 40 40 40 40 40 40 40 40 40 40
    > 40 40 40 40 40 00 15 00 00 00 00 00 00 00
                       ^^^^^^^^^^^ NCCIFEAT 15 00 00 00, then directly the end TTR

Signon J from RSCS, TTR 0x2E = 46:

    < 00 00 00 3E 00 00 00 00 00 00 00 2E 10 02 80 8F
    < CF F0 D1 29 E9 E5 D4 F7 F2 D4 E2 C8 01 FF FF FF
    < FF 00 00 10 00 40 40 40 40 40 40 40 40 40 40 40
    < 40 40 40 40 40 00 14 00 00 00 00 00 00 00

    NJE0083I Signon complete with ZVM72MSH

First data buffer from RSCS (BCB 0x81, NMR, normal buffer with RCB 00 at the end):

    < ... 10 02 81 8F CF 9A 80 FF 00 77 04 3A "RELAY" ... "ZVM72MSH"
      "DMTNCR905I Signon of link RELAY complete, buffer size=4096" ... 00

Keepalive (null buffer) sent by Sine Nomine:

    > 00 00 00 16 00 00 00 00 00 00 00 06 10 02 81 8F CF 00 00 00 00 00

## 4. Diagnosis

| | Sine Nomine (accepted) | gonje 2a (rejected) |
|---|---|---|
| DLE STX | yes | yes |
| BCB / FCS | 80 / 8F CF | 80 / 8F CF |
| NCCIDL | X'29' (41) | X'29' (41) |
| bytes after the signon record | **none** | **00** (end-of-buffer RCB) |
| TTR length | 46 = 5 + 41 | 47 |

RSCS reads exactly NCCIDL bytes of signon record after DLE STX/BCB/FCS and
rejects anything beyond it: "data being received exceeds expected length"
(code 1818). NJE38/Hercules (HOUCICS) also sends its I/J as 46-byte records
without the trailing 00; it merely tolerated ours.

DLE STX is **not** the problem: every known peer (Hercules TCPNJE/NJE38,
z/VM RSCS TCPNJE, Sine Nomine NJE) uses it.

## 5. Changes required in gonje

1. **Signon buffers end at the signon record.** A buffer that carries an
   F0 control record (I, J; also K/L/M/N and B signoff) is sent as
   `DLE STX, BCB, FCS, F0 record` with **no** trailing RCB X'00'. All other
   buffers (data, NMR, stream control, null keepalives) keep the RCB X'00'.
   Receiving accepts both forms.
2. **DLE STX always.** Remove the 0.9 heuristic "OPEN RIP 0.0.0.0 or SYN NAK
   means no DLE STX". Blind start is DLE STX. Mirroring the peer stays;
   the fallback stays only as a safety net, bounded to one cycle per connect
   window with >= 5 s between retries (0.9 made 100+ rapid reconnects to
   RSCS when both forms were rejected), and "no DLE STX" is never stored
   unless a J actually arrived that way.
3. **Correct DLE_STX.md**, which blamed DLE STX.
4. **Tests:** golden I/J bytes as in section 3 (46-byte TTR); a fake RSCS
   peer that rejects a trailing 00 after an F0 record must sign on first time.

Worth matching later (not required for signon):

- NCCIFEAT: Sine Nomine offers X'15', RSCS answers X'14'; gonje sends 0.
- Buffer size: RSCS answered 4096 to an 8192 offer; gonje already uses the
  smaller value.
- BCB: signon buffer 0x80, first data buffer 0x81 (gonje does the same).

## 6. z/VM side

- Add `LCLPORT=1175` (or authorise port 175) so gonje can also call RSCS.
- No other RSCS change is needed for signon.

## 7. Next test (after the gonje fix)

From moshix.tech, with permission:

    gonje -verbose -log zvmtest.log -o local_node=3270BBS local_IP=0.0.0.0 local_port=1750 \
          remote_host_name=ZVM72MSH remote_host_addr=zvm72.moshix.tech remote_host_port=1175 \
          remote_natout=35.192.26.118 -w 20 -c cmd ZVM72MSH Q SYS

Expected: RSCS calls in, SYN NAK, SOH ENQ, DLE ACK0, our 46-byte I, RSCS J
(buffer 4096), `DMTNCR905I Signon of link 3270BBS complete`, then the
`Q SYS` response on stdout.

## 8. Result (2026-11-09 21:11, gonje 0.9 with the fix)

Signon with z/VM RSCS **succeeded**. gonje (3270BBS on moshix.tech) dialled
zvm72.moshix.tech:1175 (RSCS now listens there). The first attempts got
`NAK reason 04` (temporary condition, RSCS busy restarting the link), then:

    framing: DLE STX (guessed: default)
    -> 46-byte signon I (no trailing 00)
    <- signon J  node=ZVM72MSH qual=1 ces=FFFFFFFF bufsz=4096 feat=00
    signon complete as primary, buffer limit 4064
    framing: DLE STX confirmed (mirrored)
    <- DMTNCR905I Signon of link 3270BBS complete, buffer size=4096

`Q SYS` was sent and RSCS returned 15 response lines (its link table, with
3270BBS in `connect` state) on stdout. gonje.state now holds
`ZVM72MSH.dlestx=yes`. NCCIFEAT X'00' was accepted (RSCS answered X'00').
