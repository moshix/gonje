# NJE job header and job trailer: implementation spec

Source: IBM SA32-0988-02, "z/OS NJE Formats and Protocols" (V2R2).
Covers Ch.2 (what an NJE job contains), Ch.3 (Job Header), Ch.5 (Job Trailer), plus
cross-references to Ch.1, Ch.8 and App.A where those affect segmentation and record types.

## 0. Conventions used here

- **Binary** fields are big-endian unsigned integers.
- **Char** fields are EBCDIC (use CP037/CP1047; they agree for A-Z, 0-9, @#$ and blank),
  left-justified and padded with EBCDIC blanks (X'40'). Valid chars: A-Z 0-9 # @ $
  (VSE/POWER may also use lower case, `-`, `.`, `/`).
- **STCK** is the 8-byte S/370 TOD clock, in GMT. Bit 51 = 1 microsecond, and the epoch
  is 1900-01-01 00:00 UTC.
  `tod = uint64(unixMicros + 2208988800*1_000_000) << 12`. This ignores leap seconds,
  which is fine because the field is only informational.
- "Sec off" = offset inside the section. "Rec off" = absolute offset in the header
  record, assuming the 4-byte prefix comes first and the general section follows it
  directly. For the job header and job trailer, the general section always comes right
  after the prefix.
- Flag-bit notation: `80` means bit 0, the high-order bit (IBM numbering).

## 1. Contents of an NJE job (Ch.2)

An NJE job is a stream of records:

```
JOB HEADER                         (always)
[DATA SET HEADER + data records]   SYSOUT: one data set header before EVERY data set
                                   SYSIN : JCL records; data set header optional (RCCS)
JOB TRAILER                        (always)
```

You can tell the record type from the RCB/SRCB, or the RID in SNA/TCP NJE. See Ch.8,
Tables 50/51:

| Stream RCB | SRCB value | Meaning |
|---|---|---|
| X'98'-X'F8' (SYSIN) | X'C0' | Job header |
| | X'E0' | Data set header |
| | X'D0' | Job trailer |
| | X'80' | Standard (data) record |
| X'99'-X'F9' (SYSOUT) | X'C0' / X'E0' / X'D0' | Job header / data set header / job trailer |
| | X'80' / X'90' / X'A0' / X'B0' | Data record: no CC / machine CC / ASA CC / CPDS |
| | bits `....ss..` | Spanned data: 1000=first, 0100=middle, 1100=last |

Rules from Ch.8, p.~149:
- Each job header, data set header and job trailer must be the **first record in its
  transmission block**.
- Unless the signon flag NCCIPACK was negotiated, each of these headers must go in a
  **separate buffer**.

## 2. Header sections: type/modifier identifiers

Every section starts with `LEN(2) TYPE(1) MOD(1)`. LEN counts the section's own 4 bytes.

| Section | TYPE | MOD | Who builds it | Required? |
|---|---|---|---|---|
| Job header general | X'00' | X'00' | all | **Yes** |
| Job header scheduling | X'8A' | X'00' | JES2, JES3 | No |
| Job header security | X'8C' | X'00' | JES2 | No |
| Job header job accounting | X'8D' | X'00' | JES2 | No |
| Job header JES2 section | X'84' | X'00' | JES2 | No |
| Job header POWER section | X'86' | X'00' | POWER | No |
| Installation-defined (header or trailer) | X'C0'-X'FF' (B'11xxxxxx') | any | user | No |
| Job trailer general | X'00' | X'00' | all | **Yes** |
| Job trailer accounting | X'89' | X'00' | JES2, JES3, RSCS | No |

Receiver rules (p.19 and the NJHLEN description):
- Products may ignore sections they do not understand, but must **not discard** them.
  A store-and-forward node passes them on.
- A section can be **longer** than this spec defines. Read the fields you know, then skip
  to `section start + LEN`.
- A section can be **shorter** (older senders). Treat any field past LEN as default:
  zero or blank.
- Walk sections like this: `off = 4; while off < NJHLEN { l = BE16(rec[off:]); t =
  rec[off+2]; ...; off += l }`. Stop if `l < 4`.

## 3. Job header prefix (Table 4)

| Rec off (dec/hex) | Len | Field | Type | Value / meaning |
|---|---|---|---|---|
| 0 / 0x00 | 2 | NJHLEN | binary | Length of the WHOLE job header, all sections plus this 4-byte prefix. Valid range 8-32764. |
| 2 / 0x02 | 1 | NJHFLAGS | - | Reserved for IBM. Send X'00'. Ignore on receive. |
| 3 / 0x03 | 1 | NJHSEQ | binary | Segment sequence byte. Bit X'80' = more segments follow. Bits X'7F' = segment number, starting at 0. Send X'00' when unsegmented. |

### 3.1 Segmentation

What the manual itself says:
- NJE records (record segments) carry at most **256 bytes** of data (Ch.1, "Spanning Data").
- Headers are "segmented for transmission". NJHSEQ / NDHSEQ / NJTSEQ hold the
  segment number (low 7 bits) and a "more follow" bit (X'80').
- App.A (JES2): "JES2 segments control records in lengths of **253** bytes." The JES2
  job header can grow to 31637 bytes.
- JES3 exit IATUX36 sees only "the first segment of the job header". So segments are
  separate NJE records, each processed on its own.

What the manual does **not** say explicitly (AMBIGUOUS):

1. Whether every segment carries its own 4-byte prefix. The usual implementation
   (JES2, RSCS, and open-source NJE such as funetnje/HNET) does: each segment is a
   control record of the same SRCB type (X'C0' job header, X'E0' DSH, X'D0' trailer)
   that starts with its own 4-byte prefix. In that prefix, SEQ = n | (more ? X'80' : 0).
2. What LEN means in the 2nd and later segments: the total header length, or the
   segment length. Implementations differ.

Recommended handling:

- **Receive.** Reassemble like this:
  1. Take segment 0 whole.
  2. For each following segment (SEQ & 0x7F == n, expected n = 1, 2, ...), append its
     bytes after its 4-byte prefix.
  3. Stop when (SEQ & 0x80) == 0.

  Length rules:
  - Do not rely on LEN in later segments. Use the record length from the transport.
  - Take the total length from segment 0's NJHLEN, but cap it at the bytes you actually
    reassembled.
  - If segment 0 has SEQ = X'00', the header is complete.

- **Send.** Never segment. Keep each header record at **252 bytes or less**. The
  minimal job header below is 216 bytes and the trailer is 52 bytes, so both fit in one
  segment. Set NJHSEQ = NJTSEQ = X'00'.

  If you must segment later (for example, with a security or accounting section added):
  - Split the logical header into 253-byte chunks (JES2 practice), counting the prefix.
  - Give every chunk a prefix: LEN = total header length, FLAGS = 0,
    SEQ = idx | X'80' on all but the last chunk.
  - Send each chunk as its own job-header (X'C0') record.

  This exact layout is our inference; check it against a real JES2 trace before relying
  on it.

## 4. Job header general section (Table 5): TYPE X'00', MOD X'00'

Defined length = **X'D4' = 212 bytes**. The last field, NJHGNTYN, starts at X'CC' and
is 8 bytes long. Prefix + general = **216 bytes (X'D8')**.

Older nodes may send a shorter general section. NJHGJNO (X'C8') and NJHGNTYN (X'CC')
are the newest fields, so expect NJHGLEN = 200 (X'C8') or 204 (X'CC') from some
senders. Use defaults for anything beyond NJHGLEN.

"Req (send)" column:
- **R** = must contain a meaningful value.
- **D** = fill with the default shown.
- **Z** = zero/blank is fine.

| Sec off hex | Sec off dec | Rec off dec | Len | Field | Type | Default | Req (send) | Meaning / notes |
|---|---|---|---|---|---|---|---|---|
| 00 | 0 | 4 | 2 | NJHGLEN | bin | - | R | Length of the general section. Send 212 (X'00D4'). |
| 02 | 2 | 6 | 1 | NJHGTYPE | bin | 0 | R | Section type. **X'00'** = general. **This is NOT a flags byte.** |
| 03 | 3 | 7 | 1 | NJHGMOD | bin | 0 | R | Modifier, X'00'. |
| 04 | 4 | 8 | 2 | NJHGJID | bin | 0 | R | Job number at the origin node. Valid 1-65535; AS/400 and RSCS use 1-9999. **Use 1-9999 for compatibility.** |
| 06 | 6 | 10 | 1 | NJHGJCLS | char | 'A' | D | Execution class (A-Z, 0-9). Send 'A' (X'C1'). |
| 07 | 7 | 11 | 1 | NJHGMCLS | char | 'A' | D | Message class. Send 'A'. |
| 08 | 8 | 12 | 1 | NJHGFLG1 | bits | 0 | Z | Flag byte. Bit meanings are in the next table. |
| 09 | 9 | 13 | 1 | NJHGPRIO | bin | 0 | Z | Selection/transmission priority 0-15; 15 is highest. JES2 uses it on receive only if NJHGF1PR is set. RSCS maps its 0-99 to 0-15. |
| 0A | 10 | 14 | 1 | NJHGORGQ | bin | 0 | Z | Origin system qualifier (MAS member). Send 0. |
| 0B | 11 | 15 | 1 | NJHGJCPY | bin | **1** | D | Job-level copy count. JES2 multiplies it by the DSH copy count. **Send 1. A 0 might produce zero copies at JES2** (not stated in the manual; send 1 to be safe). |
| 0C | 12 | 16 | 1 | NJHGLNCT | bin | 0 | D | Lines per page. X'00' = use the destination's default. X'FF' = no forced ejects (RSCS sends X'FF' for print). |
| 0D | 13 | 17 | 1 | - | - | - | Z | Reserved. Send X'00'. |
| 0E | 14 | 18 | 2 | NJHGHOPS | bin | 0 | Z | Hop count. The origin sends 0. Intermediate nodes increment it, and JES2 holds the job past a threshold. |
| 10 | 16 | 20 | 8 | NJHGACCT | char | blanks | Z | Network account number. Send blanks. |
| 18 | 24 | 28 | 8 | NJHGJNAM | char | none | **R** | Job name. There is no default. RSCS uses the VM userid. JES3 falls back to `NJEJOB`. POWER uses JOBnnnnn if blank. **Always fill it.** |
| 20 | 32 | 36 | 8 | NJHGUSID | char | blanks | R | Notify userid at node NJHGNTYN. JES2 sets it to the origin user if nothing else applies. RSCS and AS/400 treat it as the origin userid when NJHGORGR is blank. |
| 28 | 40 | 44 | 8 | NJHGPASS | char/bin | 0 | Z | Job password. JES2 blanks it for SYSOUT. RSCS sends binary zeros. Send X'00'×8. |
| 30 | 48 | 52 | 8 | NJHGNPAS | char/bin | 0 | Z | New password. Send X'00'×8. |
| 38 | 56 | 60 | 8 | NJHGETS | STCK | 0 | R* | Time the job entered the system (GMT). Used for SMF. 0 is tolerated, but send the current time. |
| 40 | 64 | 68 | 8 | NJHGORGN | char | none | **R** | **Origin node**. Never changed while the job exists. Used for security and auditing. |
| 48 | 72 | 76 | 8 | NJHGORGR | char | blanks | **R** | **Origin userid**, or the remote workstation it came from. |
| 50 | 80 | 84 | 8 | NJHGXEQN | char | blanks | R(SYSIN) | Execution node. For SYSIN jobs, this is the routing target. |
| 58 | 88 | 92 | 8 | NJHGXEQU | char | blanks | Z/R | Execution userid. RSCS: for SYSIN, the destination VM; for SYSOUT, the origin userid. |
| 60 | 96 | 100 | 8 | NJHGPRTN | char | blanks | R | Default print destination **node**. RSCS SYSOUT: the destination node. |
| 68 | 104 | 108 | 8 | NJHGPRTR | char | blanks | R | Default print destination **user** or remote. RSCS SYSOUT: the destination user. JES2 uses it to initialise NDHGRMT. |
| 70 | 112 | 116 | 8 | NJHGPUNN | char | blanks | R | Default punch destination node. |
| 78 | 120 | 124 | 8 | NJHGPUNR | char | blanks | R | Default punch destination user. RSCS writes X'00'... if the destination is SYSTEM. |
| 80 | 128 | 132 | 8 | NJHGFORM | char | blanks | Z | Default forms. JES3 and RSCS send zeros. Send blanks. |
| 88 | 136 | 140 | 4 | NJHGICRD | bin | 0 | Z | Number of card images in a SYSIN job. Send 0 for SYSOUT. |
| 8C | 140 | 144 | 4 | NJHGETIM | bin | 0 | Z | Estimated CPU time. |
| 90 | 144 | 148 | 4 | NJHGELIN | bin | 0 | Z | Estimated print lines. |
| 94 | 148 | 152 | 4 | NJHGECRD | bin | 0 | Z | Estimated punch cards. |
| 98 | 152 | 156 | **20** | NJHGPRGN | char | blanks | Z | Programmer name, shown on the JES2 and POWER separator pages. |
| AC | 172 | 176 | 8 | NJHGROOM | char | blanks | Z | Room. JES2 uses only 4 characters. |
| B4 | 180 | 184 | 8 | NJHGDEPT | char | blanks | Z | Department. |
| BC | 188 | 192 | 8 | NJHGBLDG | char | blanks | Z | Building. |
| C4 | 196 | 200 | 4 | NJHGNREC | bin | 0 | R(SYSOUT) | For SYSOUT, the sum of data records across all data sets. Header records do not count, and a spanned record counts as 1. The receiver uses it in its receipt message. Not set for SYSIN. |
| C8 | 200 | 204 | 4 | NJHGJNO | bin | 0 | Z | Expanded job number, 1 to 4,294,967,295. JES2/JES3 only. Send 0 or the same value as NJHGJID. |
| CC | 204 | 208 | 8 | NJHGNTYN | char | blanks | R | Node of the user to notify. Pairs with NJHGUSID. |
| D4 | 212 | 216 | - | (end) | | | | |

Notes on field names in the request:
- "NJHGPUN" is NJHGPUNN. "NJHGPUSR" does not exist; the print user is **NJHGPRTR**.
- "NJHGTYPE flags" is a mix-up: NJHGTYPE is the section type byte, and the flags are in
  **NJHGFLG1**.
- NJHGNREC appears only once, at X'C4'.

### 4.1 NJHGFLG1 bits (rec off 12)

| Mask | Name | Meaning |
|---|---|---|
| X'80' | NJHGF1PR | Recompute priority. If set, JES2 uses NJHGPRIO as the job's priority; if clear, it uses the installation default. |
| X'70' | - | Reserved. |
| X'08' | NJHGF1CF | 1 = do **not** send forwarding-notification NMRs. Only RSCS honours it (TAG SENTMSG). |
| X'04' | NJHGF1CA | 1 = do **not** send acceptance-notification NMRs. Only RSCS honours it. JES2 sends $HASP122/$HASP546 regardless. |
| X'02' | NJHGF1PE | NJHGPASS is encrypted (8 bytes implied). |
| X'01' | NJHGF1NE | NJHGNPAS is encrypted. PE=0 with NE=1 is not allowed. |

### 4.2 Optional job header sections (summary; skip on receive, preserve if forwarding)

- **Scheduling, X'8A'**: LEN(2) TYPE MOD, then NJHEPAGE(4 @+4) and NJHEBYTE(4 @+8).
  Length 12.
- **Security, X'8C'**: NJHTLEN default 84. Has a prefix subsection: NJHTLENP=4 @+4,
  NJHTFLG0 @+6 (X'80' = owner), reserved @+7. Then a data subsection starting at +8:
  - NJHTLENT=80, NJHTVERS=1, NJHTFLG1 (X'80' encrypted, X'40' external, X'20' non-RACF
    node), NJHTSTYP=7, NJHTFLG2, NJHTPOEX=3.
  - Then 8-byte char fields at subsection offsets X'08' SECL, X'10' CNOD, X'18' SUSR,
    X'20' SNOD, X'28' SGRP, X'30' POEN, X'38' reserved, X'40' OUSR, X'48' OGRP.
  - The manual's subsection offsets restart at 0 after the prefix. So absolute =
    section + 8 + subsection offset, giving 8+80 = 88. That does not match the default
    NJHTLEN of 84: **AMBIGUOUS**. **Do not send this section.**
- **Job accounting, X'8D'**: NJHAOFFS(2), NJHAFLG1(1), reserved(1), then variable
  substrings. The table offsets are inconsistent: "NJHAJLEN 8" and "NJHAJAC1 11".
  Do not send it.
- **JES2, X'84'**: FLG1 @+4 and FLG2 @+5. The table prints "5" for both; FLG1 is
  probably @+4. Then ACCT char 4 @+8, USID @X'0C', USR @X'14', GRP @X'1C', SUSR @X'24',
  SGRP @X'2C' (8 each). Length X'34' = 52. Do not send it.
- **POWER, X'86'**: VSE only. Ignore it.

## 5. Job trailer prefix (Table 29)

| Rec off | Len | Field | Type | Value |
|---|---|---|---|---|
| 0 / 0x00 | 2 | NJTLEN | bin | Total trailer length, including the prefix. Range 8-32K. |
| 2 / 0x02 | 1 | NJTFLAGS | bin | Reserved. Send X'00'. |
| 3 / 0x03 | 1 | NJTSEQ | bin | Same as NJHSEQ: X'80' = more segments, X'7F' = segment number. Send X'00'. |

## 6. Job trailer general section (Table 30): TYPE X'00', MOD X'00'

Defined length = **X'30' = 48 bytes**. Prefix + general = **52 bytes (X'34')**.

| Sec off hex | Sec off dec | Rec off dec | Len | Field | Type | Default | Req (send) | Meaning |
|---|---|---|---|---|---|---|---|---|
| 00 | 0 | 4 | 2 | NJTGLEN | bin | - | R | Send 48 (X'0030'). The manual calls this the "job header general section" length, a copy-paste error. |
| 02 | 2 | 6 | 1 | NJTGTYPE | bin | - | R | **Must be X'00'.** |
| 03 | 3 | 7 | 1 | NJTGMOD | bin | 0 | R | X'00'. |
| 04 | 4 | 8 | 1 | NJTGFLG1 | bits | 0 | Z | Reserved. |
| 05 | 5 | 9 | 1 | NJTGXCLS | char | 'A' | D | Actual execution class. AS/400 and JES3 send zero, so accept X'00' on receive. Send 'A' (X'C1'). |
| 06 | 6 | 10 | 2 | - | | | Z | Reserved. |
| 08 | 8 | 12 | 8 | NJTGSTRT | STCK | 0 | Z | Time execution started (GMT). Only JES2 sets it. |
| 10 | 16 | 20 | 8 | NJTGSTOP | STCK | 0 | Z | Time execution ended (GMT). For spin data, every trailer except the last is 0. |
| 18 | 24 | 28 | 4 | - | | | Z | Reserved. |
| 1C | 28 | 32 | 4 | NJTGALIN | bin | 0 | R* | Total print lines, not multiplied by copies. RSCS and AS/400 send the record count of a print file. **Send the line count.** |
| 20 | 32 | 36 | 4 | NJTGACRD | bin | 0 | Z | Total card images. Use for punch or SYSIN. |
| 24 | 36 | 40 | 4 | - | | | Z | Reserved. |
| 28 | 40 | 44 | 1 | NJTGIXPR | bin | 0 | Z | Initial execution selection priority, 0-15. JES2 shifts it into JCTIPRIO. Send the same value as NJHGPRIO. |
| 29 | 41 | 45 | 1 | NJTGAXPR | bin | 0 | Z | Actual execution priority, 0-15. |
| 2A | 42 | 46 | 1 | NJTGIOPR | bin | 0 | Z | Initial output priority, 0-255. JES3 uses max(IOPR, AOPR), capped at 15, as the priority of received output. |
| 2B | 43 | 47 | 1 | NJTGAOPR | bin | 0 | Z | Actual output priority. |
| 2C | 44 | 48 | 1 | NJTGCOMP | bits | 0 | Z | Completion indicator. Bit meanings are in the next table. |
| 2D | 45 | 49 | 3 | NJTGCODE | bin | 0 | Z | Completion code. If NJTJCAB is set: system abend code in the first 12 bits, user abend code in the last 12. If NJTJCCC is set: the step condition code. |
| 30 | 48 | 52 | - | (end) | | | | |

### 6.1 NJTGCOMP (rec off 48)

| Mask/value | Name | Meaning |
|---|---|---|
| X'80' | NJTJCAB | NJTGCODE holds an abend code. |
| X'40' | NJTJCCC | NJTGCODE holds a completion code. |
| low 6 bits (X'3F') | NJTGCCDE | Completion type. The manual prints the mask as "2f", a typo. |
| 0 | NJTGCUNK | No completion information. |
| 1 | NJTGCNRM | Ended normally. CODE = highest step condition code. |
| 2 | NJTGCECC | Ended by a condition code. CODE = the terminating step's condition code. |
| 3 | NJTGCJCL | JCL error. |
| 4 | NJTGCCAN | Cancelled. |
| 5 | NJTGCABN | Abended. |
| 6 | NJTGCCAB | Converter abended. |
| 7 | NJTGCSEC | Security error. |
| 8 | NJTGCEOM | Abended in end-of-memory processing. |

### 6.2 Trailer accounting section, X'89' / X'00' (optional; do not send)

| Off | Len | Field | Meaning |
|---|---|---|---|
| 0 | 2 | NJTSLEN | Section length. |
| 2 | 1 | NJTSTYPE | X'89'. |
| 3 | 1 | NJTSMOD | X'00'. |
| 4 | 4 | NJTSAPAG | Actual number of "begin page" structured fields. |
| 8 | 4 | NJTSABYT | Actual bytes. |

Length is 12.

## 7. Recommended MINIMAL job header for sending one SYSOUT file

Inputs:
- FROMNODE / FROMUSER: our node and user.
- TONODE / TOUSER: the destination node and user.
- JOBID: 1-9999.
- JOBNAME: defaults to FROMUSER.
- NREC: number of data records in the file.

Notation:
- E(s) = the EBCDIC of s, upper-cased and blank-padded to 8 bytes.
- B8 = 8 bytes of X'40'.
- Z = X'00' bytes.

Total record = 216 bytes. Send it as one record with SRCB X'C0' on a SYSOUT stream
(RCB X'99'-X'F9').

| Rec off | Len | Field | Value |
|---|---|---|---|
| 0 | 2 | NJHLEN | X'00D8' (216) |
| 2 | 1 | NJHFLAGS | X'00' |
| 3 | 1 | NJHSEQ | X'00' |
| 4 | 2 | NJHGLEN | X'00D4' (212) |
| 6 | 1 | NJHGTYPE | X'00' |
| 7 | 1 | NJHGMOD | X'00' |
| 8 | 2 | NJHGJID | JOBID (BE16) |
| 10 | 1 | NJHGJCLS | X'C1' ('A') |
| 11 | 1 | NJHGMCLS | X'C1' ('A') |
| 12 | 1 | NJHGFLG1 | X'00' |
| 13 | 1 | NJHGPRIO | X'00'. Optional: any 0-15. |
| 14 | 1 | NJHGORGQ | X'00' |
| 15 | 1 | NJHGJCPY | **X'01'** |
| 16 | 1 | NJHGLNCT | X'00' (destination default). RSCS-style senders use X'FF'. |
| 17 | 1 | reserved | X'00' |
| 18 | 2 | NJHGHOPS | X'0000' |
| 20 | 8 | NJHGACCT | B8 |
| 28 | 8 | NJHGJNAM | E(JOBNAME) |
| 36 | 8 | NJHGUSID | E(FROMUSER) (notify user) |
| 44 | 8 | NJHGPASS | Z×8 |
| 52 | 8 | NJHGNPAS | Z×8 |
| 60 | 8 | NJHGETS | STCK(now) |
| 68 | 8 | NJHGORGN | E(FROMNODE) |
| 76 | 8 | NJHGORGR | E(FROMUSER) |
| 84 | 8 | NJHGXEQN | E(FROMNODE). The "job" ran on our node. |
| 92 | 8 | NJHGXEQU | E(FROMUSER). RSCS does the same for SYSOUT. |
| 100 | 8 | NJHGPRTN | E(TONODE) |
| 108 | 8 | NJHGPRTR | E(TOUSER), or B8 for the node's local printer or SYSTEM |
| 116 | 8 | NJHGPUNN | E(TONODE) |
| 124 | 8 | NJHGPUNR | E(TOUSER), or B8 |
| 132 | 8 | NJHGFORM | B8 |
| 140 | 4 | NJHGICRD | 0 |
| 144 | 4 | NJHGETIM | 0 |
| 148 | 4 | NJHGELIN | 0 |
| 152 | 4 | NJHGECRD | 0 |
| 156 | 20 | NJHGPRGN | B×20, or the user's name in EBCDIC padded with blanks |
| 176 | 8 | NJHGROOM | B8 |
| 184 | 8 | NJHGDEPT | B8 |
| 192 | 8 | NJHGBLDG | B8 |
| 200 | 4 | NJHGNREC | NREC (BE32) |
| 204 | 4 | NJHGJNO | JOBID (BE32), or 0 |
| 208 | 8 | NJHGNTYN | E(FROMNODE) |

**Important:** the actual SYSOUT routing (destination node/user, class, forms, copies)
lives in the **data set header**: NDHGNODE / NDHGRMT / NDHGCLAS, and so on (see the
data set header notes). The job-header PRTN/PRTR fields are only defaults. Set both
consistently.

Do not send scheduling, security, accounting or JES2 sections. JES2 accepts their
absence; RSCS and AS/400 never send them.

## 8. Recommended MINIMAL job trailer

Total = 52 bytes. Send it as one record with SRCB X'D0' on the same stream.

| Rec off | Len | Field | Value |
|---|---|---|---|
| 0 | 2 | NJTLEN | X'0034' (52) |
| 2 | 1 | NJTFLAGS | X'00' |
| 3 | 1 | NJTSEQ | X'00' |
| 4 | 2 | NJTGLEN | X'0030' (48) |
| 6 | 1 | NJTGTYPE | X'00' |
| 7 | 1 | NJTGMOD | X'00' |
| 8 | 1 | NJTGFLG1 | X'00' |
| 9 | 1 | NJTGXCLS | X'C1' ('A') |
| 10 | 2 | reserved | X'0000' |
| 12 | 8 | NJTGSTRT | Z×8, or STCK |
| 20 | 8 | NJTGSTOP | Z×8, or STCK |
| 28 | 4 | reserved | 0 |
| 32 | 4 | NJTGALIN | NREC (print) |
| 36 | 4 | NJTGACRD | 0 (or NREC for punch) |
| 40 | 4 | reserved | 0 |
| 44 | 1 | NJTGIXPR | Same as NJHGPRIO |
| 45 | 1 | NJTGAXPR | Same as NJHGPRIO |
| 46 | 1 | NJTGIOPR | Same as NJHGPRIO |
| 47 | 1 | NJTGAOPR | Same as NJHGPRIO |
| 48 | 1 | NJTGCOMP | X'00' (unknown). Alternative: X'41' (CC flag + normal end) with CODE 0. |
| 49 | 3 | NJTGCODE | X'000000' |

## 9. Parsing checklist (receive side)

1. Check that the record is at least 4 bytes, and that NJHLEN is ≥ 8 and ≤ the bytes
   available.
2. Reassemble the segments as described in 3.1 if NJHSEQ & X'80' is set.
3. Walk the sections from offset 4. You must find TYPE X'00' (general). Others are
   optional.
4. For the general section, copy its bytes into a zeroed 212-byte buffer. Copy at most
   NJHGLEN bytes, so short sections get zero defaults. Then decode fixed offsets.
   - Trim trailing X'40' and X'00' from char fields.
   - Also treat all-X'00' char fields as empty: RSCS/JES3 send zeros in FORM, PUNR and
     so on.
5. Keep the raw header bytes if you might forward the job (store-and-forward must keep
   unknown sections).
6. Trailer: same procedure. The general section is 48 bytes.

## 10. Ambiguities / errata noticed in the manual

- **Segmentation.** Whether later segments repeat the 4-byte prefix, and what LEN means
  in them, is not explicit. See 3.1. JES2 uses 253-byte segments.
- **NJHLEN.** The valid range is "8-32764", but a header with only a general section is
  always far larger than 8. The real minimum depends on NJHGLEN.
- **NJHGJNO.** The table repeats "C8 4 binary 0 Reserved" after the NJHGJNO row. The
  old layout had X'C8' reserved, and NJHGJNO later replaced it.
- **Old general-section lengths.** Some senders' general sections end before
  NJHGNTYN/NJHGJNO.
- **NJTGLEN.** The description calls it "job header general section", a copy-paste
  error.
- **NJTGCCDE mask.** It is printed "2f"; the correct mask is X'3F'.
- **NJTGXCLS.** The default is 'A', but AS/400 and JES3 send X'00'.
- **Security and accounting sections.** Their offset tables are internally inconsistent
  (see 4.2).
- **JES2 section.** NJH2FLG1 and NJH2FLG2 are both printed at offset 5.
- **NJHGPASS.** JES2 sends blanks and RSCS sends zeros for SYSOUT. Accept both.
- **NJHGF1CF / NJHGF1CA.** The polarity is "1 = do not send"; easy to invert.
