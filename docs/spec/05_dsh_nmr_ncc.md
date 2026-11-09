# 05 - Data Set Header, NMR, NCC records (implementation spec)

Source: IBM SA32-0988-02 "z/OS NJE Formats and Protocols", Chapter 4 (pp.47-82),
Chapter 6 (pp.127-136), Chapter 7 (pp.137-141), plus a few cross-refs
(Ch.1 p.7, Ch.8 Tables 50/51 p.150-151, App.A p.221).
"Offset" columns are given as hex (manual) and decimal. All multi-byte binary
fields are big-endian. All `char` fields are EBCDIC, left-justified, padded
with X'40' (blank) unless stated otherwise.

Conventions used below:
- **REQ** = must be set to a meaningful value when we build it.
- **DEF** = send the default (blanks/zero) unless we have a reason.
- `cE(x)` = EBCDIC of string x padded with X'40' to field length.

---------------------------------------------------------------------------

## 1. Where the data set header sits

- An NJE SYSOUT job = Job Header, then for each SYSOUT data set: one or more
  Data Set Headers (DSH) + data records, then Job Trailer.
- An NJE SYSIN job = Job Header, JCL/SYSIN records, optionally a DSH that
  contains ONLY a Record Characteristics Change section (when SYSIN is not
  RECFM=F LRECL=80), Job Trailer.
- Record framing (Ch.8 Table 51): DSH is sent as a control record:
  - SYSOUT stream: RCB X'9x' (99-F9 range), **SRCB X'E0'** (`1110 0000`).
  - SYSIN stream: RCB X'98'-range, SRCB X'E0'.
  - Each job header / DSH / job trailer must be the **first record in its
    transmission block**, and in a separate buffer, unless NCCIPACK was
    negotiated at signon (then multiple headers per buffer are allowed).
- Segmentation: NJE records carry at most 256 bytes of data. Control records
  longer than that are segmented (JES2 segments control records into 253-byte
  pieces, App.A p.221). The 4-byte prefix NDHSEQ byte carries the segment
  number + "more follows" bit. A minimal DSH (124 bytes) needs **no
  segmentation** (NDHSEQ = X'00').
  - AMBIGUITY: Chapter 4 doesn't say how each later segment is laid out
    (whether each segment repeats the 4-byte prefix). Check the job-header /
    presentation-services notes before you build segmented headers. For a
    minimal implementation, only send DSHs <= 253 bytes (prefix + general
    section = 124 fits).

### Sections that may follow the prefix (identifier/modifier = bytes 2-3 of each section)

| Section | Type (byte 2) | Modifier (byte 3) | Notes |
|---|---|---|---|
| General section | X'00' | X'00' | REQUIRED for SYSOUT. Built by all products. |
| Record Characteristics Change | X'00' | X'40' | SYSIN only; sent ALONE (no general section). |
| 3800 section | X'00' | X'80' | 3800 printer attributes (NDHAxxxx). Optional. |
| Output Processing section (OPTB/OPTU) | X'89' | X'00' | AFP / OUTPUT JCL attributes (NDHSxxxx, 'SJPF' constant). Optional. |
| Security section | X'8C' | X'00' | RACF token (NDHTxxxx), length 84 default. Optional; JES2/JES3 build it. |
| RSCS section | X'87' | X'00' | VM spool attributes (NDHVxxxx). Optional. |
| POWER section | X'86' | X'00' | VSE/POWER attributes (NDHPxxxx). Optional. |

Parsing rule: after the 4-byte prefix, walk sections with `len = BE16(sec[0:2])`,
`type = sec[2]`, `mod = sec[3]`; skip unknown sections by length. Stop when
consumed bytes == NDHLEN.

---------------------------------------------------------------------------

## 2. DSH Prefix (Table 13) - 4 bytes

| Off hex | Off dec | Len | Field | Type | Meaning | Build |
|---|---|---|---|---|---|---|
| 00 | 0 | 2 | NDHLEN | bin | Combined length of ALL sections incl. this 4-byte prefix (range 4-32764) | REQ |
| 02 | 2 | 1 | NDHFLAGS | bin | Reserved for IBM | X'00' |
| 03 | 3 | 1 | NDHSEQ | bin | Segment sequence: X'80' = more segments follow; X'7F' = segment number from 0 | X'00' |

AMBIGUITY: The manual says NDHLEN is "combined length of all sections"; in
practice (and consistent with the job header) it includes the prefix itself.
Minimal DSH: NDHLEN = 4 + 120 = **124 (X'007C')**.

---------------------------------------------------------------------------

## 3. DSH General Section (Table 14) - 120 bytes (X'78')

Offsets relative to the start of the general section (add 4 for offset in the
whole DSH record).

| Off hex | Off dec | Len | Field | Type | Default | Range | Meaning | Build |
|---|---|---|---|---|---|---|---|---|
| 00 | 0 | 2 | NDHGLEN | bin | - | 4-32764 | Length of general section = **X'0078' (120)** | REQ |
| 02 | 2 | 1 | NDHGTYPE | bin | - | - | Section type **X'00'** | REQ |
| 03 | 3 | 1 | NDHGMOD | bin | 0 | - | Modifier **X'00'** = general section | REQ |
| 04 | 4 | 8 | NDHGNODE | char | - | - | Destination NODE for the data set (may be set from NJHGPRTN / NJHGPUNN) | REQ |
| 0C | 12 | 8 | NDHGRMT | char | blanks | - | Destination userid, remote workstation (e.g. R12) or printer id (from NJHGPRTR/NJHGPUNR). AS/400: blank/zero => 'SYSTEM'. RSCS: X'00' when SYSTEM is the destination. JES2/JES3 TSO TRANSMIT: if NDHGRMT == NDHGXWTR, NDHGRMT is the TSO destination userid. | REQ (user) / DEF |
| 14 | 20 | 8 | NDHGPROC | char | blanks | - | JCL procstep name active at creation. RSCS puts the VM FILENAME here (see NDHGF2RM). | DEF |
| 1C | 28 | 8 | NDHGSTEP | char | blanks | - | Stepname at creation. RSCS puts VM FILETYPE here. | DEF |
| 24 | 36 | 8 | NDHGDD | char | blanks | - | DDNAME at creation. JES3 substitutes NJHGROOM or '*NJEWKnn' if blank. | DEF (or e.g. 'SYSPRINT') |
| 2C | 44 | 2 | NDHGDSNO | bin | 0 | 0-32767 | Data set number within the job (uniqueness counter). JES2 ignores on input if <= 100. | 1,2,3... per data set (101+ to be safe is not required) |
| 2E | 46 | 1 | - | - | - | - | Reserved | X'00' |
| 2F | 47 | 1 | NDHGCLAS | char | 'A' | A-Z,0-9 | SYSOUT class. JES2 forces 'A' if not alphanumeric. | REQ ('A') |
| 30 | 48 | 4 | NDHGNREC | bin | 0 | - | Record count. **JES2 uses it as the line count** of the data set; spanned record counts as 1. | REQ (actual count) |
| 34 | 52 | 1 | NDHGFLG1 | bits | 0 | - | Flag byte 1 (below) | X'00' typical |
| 35 | 53 | 1 | NDHGRCFM | bits | 0 | - | Record format + carriage control (below) | REQ |
| 36 | 54 | 2 | NDHGLREC | bin | 0 | 1-32760 | Max LRECL of any record, **including the CC byte** if CC present | REQ |
| 38 | 56 | 1 | NDHGDSCT | bin | 0 | 0-255 | Copy count. JES3: 0 => 1 copy. | X'01' |
| 39 | 57 | 1 | NDHGFCBI | bin (signed) | 0 | -31..31 | 3211 FCB index (neg = left, pos = right) | X'00' |
| 3A | 58 | 1 | NDHGLNCT | bin | 0 | - | Lines per page; valid only if NDHGF1LC on. X'00' = printer default, X'FF' = no forced page ejects | X'00' |
| 3B | 59 | 1 | - | - | - | - | Reserved | X'00' |
| 3C | 60 | 8 | NDHGFORM | char | blanks | - | Forms name. JES2/JES3 use first 4 chars; zero or blanks = STD. (3800/POWER: X'00' = hw default, X'40' = sw default) | DEF (blanks) |
| 44 | 68 | 8 | NDHGFCB | char | blanks | - | FCB name (first 4 used by JES2). Zero/blanks = default. | DEF |
| 4C | 76 | 8 | NDHGUCS | char | blanks | - | UCS / print train name (first 4 used). | DEF |
| 54 | 84 | 8 | NDHGXWTR | char | blanks | - | External writer name. If == NDHGRMT, used by TSO/E TRANSMIT/RECEIVE and CMS SENDFILE to deliver to an individual MVS user. | DEF, or = NDHGRMT for TRANSMIT-style |
| 5C | 92 | 8 | NDHGNAME | char | blanks | A-Z,0-9 | Data set name qualifier at creation node (JES2/JES3: from DSNAME=, default '?'). Used to build RACF resource name. | DEF or a short name |
| 64 | 100 | 1 | NDHGFLG2 | bits | 0 | - | Flag byte 2 (below) | X'80' for print |
| 65 | 101 | 1 | NDHGUCSO | bits | 0 | - | UCS options (below) | X'00' |
| 66 | 102 | 2 | - | - | - | - | Reserved | X'0000' |
| 68 | 104 | 8 | NDHGPMDE | char | blanks | - | Process mode: 'LINE', 'PAGE', 'SOSI1', 'SOSI2'. JES2 inspects data if blank. | 'LINE    ' recommended |
| 70 | 112 | 4 | NDHGSEGN | bin | 0 | - | Segment number of a segmented data set (JES2 sets if <= 99999) | X'00000000' |
| 74 | 116 | 4 | - | - | - | - | Reserved | zeros |
| 78 | 120 | - | (end) | | | | **General section length = 120** | |

Fields asked about that **do not exist** in this section of the manual:
- `NDHGDSNM` - no such field. The only data-set-name field is **NDHGNAME**
  (8-char qualifier). (The full dsname is not carried in the general section.)
- `NDHGCPDS` - no such field. CPDS (page-mode) data is signalled by
  NDHGRCFM CC bits `11` (MODCA/AFPDS) and data-record SRCB X'B0'.

### 3.1 NDHGFLG1 (offset X'34')

| Mask | Name | Meaning |
|---|---|---|
| X'80' | NDHGF1SP | Spin data set. (POWER: if set, job number taken from NJHGJID.) |
| X'40' | NDHGF1HD | Destination node should HOLD the data set (JES2: output hold queue). |
| X'20' | NDHGF1LG | Data set is the JES2 job log. |
| X'10' | NDHGF1OV | Page overflow allowed (print over page fold). |
| X'08' | NDHGF1IN | Use punch interpreter feature (see NDHGF1DF). |
| X'04' | NDHGF1LC | NDHGLNCT (X'3A') is valid. |
| X'02' | NDHGF1ST | Job log contains job statistics. |
| X'01' | NDHGF1DF | With NDHGF1IN: IN=0,DF=0 use default; IN=1,DF=0 interpret; IN=0,DF=1 do not interpret; IN=1,DF=1 interpret. |

### 3.2 NDHGRCFM (offset X'35') - record format and carriage control

The manual documents two bit groups in this byte; the layout matches the
OS DCB RECFM byte (JES2 copies RECFM from the DCB and "allows all flag
settings"):

| Bits (0=MSB) | Pattern | Meaning |
|---|---|---|
| 0-1 | `11.. ....` (X'C0') | Undefined (U) |
| 0-1 | `10.. ....` (X'80') | Fixed (F) |
| 0-1 | `01.. ....` (X'40') | Variable (V) |
| 5-6 | `.... .00.` (X'00') | No carriage control |
| 5-6 | `.... .10.` (X'04') | **ASA** carriage control |
| 5-6 | `.... .01.` (X'02') | **Machine** carriage control |
| 5-6 | `.... .11.` (X'06') | MODCA / AFPDS print file |

Bits 2-4 and 7 are not described by the manual. By DCB convention they are
T=X'20', B(blocked)=X'10', S(spanned/standard)=X'08'; JES2 transmits whatever is
set. Safe to send 0 in those bits.

Common values seen in the manual: RSCS print = **X'42'** (V + machine),
RSCS/AS400 punch = **X'80'** (F, no CC), POWER = X'Cx' (undefined).

The data-record SRCB must agree with NDHGRCFM (Ch.8 Table 51, SYSOUT RCB):
SRCB X'80' = no CC, X'90' = machine CC, X'A0' = ASA CC, X'B0' = CPDS page mode.

NDHGLREC hints (RSCS uses it to pick the virtual device): 1-132 -> 1403,
133-150 -> 3211, 151-204 -> 3800, 205-32765 -> AFP. JES3 defaults: print
133 with CC / 132 without; punch 81 with CC / 80 without.

### 3.3 NDHGFLG2 (offset X'64')

| Mask | Name | Meaning |
|---|---|---|
| X'80' | NDHGF2PR | Data set is to be **printed** |
| X'40' | NDHGF2PU | Data set is to be **punched** |
| X'20' | NDHGF2RM | RSCS: take FILENAME/FILETYPE from NDHGPROC/NDHGSTEP |
| X'10' | NDHGF2HB | Hold before processing (disposition HOLD or LEAVE) |
| X'08' | NDHGF2HA | Hold after processing (disposition KEEP or LEAVE); also set NDHGF1HD for older nodes |
| X'04' | NDHGF2HX | JES3: hold for external writer |
| X'02' | NDHGF2TR | Truncate trailing blanks (JES3) |
| X'01' | NDHGF2NO | "Non-output" SYSOUT (JES2 JCL images / EVENTLOG). Only send if peer set NCCINOS at signon. |

### 3.4 NDHGUCSO (offset X'65')

| Mask | Name | Meaning |
|---|---|---|
| X'80' | NDHGUCSD | Load UCS with block-data-check option |
| X'40' | NDHGUCSF | Load UCS with FOLD option |
| X'3F' | - | Reserved |

---------------------------------------------------------------------------

## 4. Record Characteristics Change Section (Table 15) - 8 bytes

Type X'00', modifier X'40'. SYSIN only. Not required if all SYSIN is RECFM F
LRECL 80. When present it is the **only** section after the prefix (no general
section). JES3 never uses it; JES2 does not create or accept a segmented one,
and does not accept it before the JOB statement.

| Off hex | Off dec | Len | Field | Type | Meaning |
|---|---|---|---|---|---|
| 00 | 0 | 2 | NDHCLEN | bin | Section length (X'0008') |
| 02 | 2 | 1 | NDHCTYPE | bin | X'00' |
| 03 | 3 | 1 | NDHCMOD | bin | X'40' |
| 04 | 4 | 1 | NDHCFLG1 | byte | Reserved, X'00' |
| 05 | 5 | 1 | NDHCRCFM | bin | X'C0' U / X'80' F / X'40' V |
| 06 | 6 | 2 | NDHCLREC | bin | Max SYSIN record length, 0-252 (0-32767 if spanned SYSIN, NCCISSIN, negotiated). POWER max 128. JES2 max SYSIN record 252. |

Full DSH = 4 (prefix) + 8 = 12 bytes, NDHLEN = X'000C'.

## 5. Other sections (brief)

- **3800 section** (X'00'/X'80'): NDHAFLG1 (X'80' TRC/OPTCD=J, X'40' burst,
  X'20' no-burst), NDHAFLCT flash count, NDHATREF, NDHATAB1-4 (8 each),
  NDHAFLSH, NDHAMODF, NDHACPYG (8 x 1-byte copy groups). Skip for now.
- **Output processing section** (X'89'/X'00'): NDHSLEN, NDHSFLEN, NDHSFLG1,
  NDHSNSTR, NDHSGPID, then OPTB prefix ('SJPF', version X'02', plen X'1C',
  data length, verb 'Output  ', ...) followed by OPTU text units (keys). Skip;
  on receive, skip by length.
- **Security section** (X'8C'/X'00'), default length 84: prefix sub-section
  (NDHTLENP=4, NDHTFLG0 X'80' owner vs submitter) + data sub-section
  (NDHTLENT=80, version 1, flags, session type 7, POE class, security label,
  creating node, submit user/node/group, POE name, owner user/group). Do not
  send; on receive skip or log.
- **RSCS section** (X'87'/X'00'): CP class, device type, distribution code,
  12-char CP FILENAME (X'10') / FILETYPE (X'1C'), priority, version, 136-byte
  TAG. **POWER section** (X'86'/X'00'). Skip on receive.

---------------------------------------------------------------------------

## 6. NMR - Nodal Message Record (Chapter 6)

### 6.1 Framing and limits

- NMR record: **RCB X'9A', SRCB X'80'** (Ch.6 intro; Ch.8 Table 51).
- NMRs are not spooled at intermediate nodes; if the next hop is down the NMR
  is discarded (no guaranteed error back).
- Whole NMR (header + text) **cannot exceed 256 bytes** (Ch.1 p.7).
- Fixed header = **30 bytes (X'1E')**; text starts at NMRMSG (X'1E').
- All text is EBCDIC. The manual does not name a code page; use CP037 (or
  IBM-1047) consistently; node/userids uppercase.
- Record length on the wire = 30 + (bytes actually present in NMRMSG).
  See 6.6 for how NMRML relates to that.

### 6.2 Common header layout

| Off hex | Off dec | Len | Field | Type | Meaning |
|---|---|---|---|---|---|
| 00 | 0 | 1 | NMRFLAG | bits | Flags (6.3) |
| 01 | 1 | 1 | NMRLEVEL (hi 4 bits) / NMRPRIO (lo 4 bits) | bin | Importance level / output priority |
| 02 | 2 | 1 | NMRTYPE | bits | Type flags (6.3) |
| 03 | 3 | 1 | NMRML | bin | Length of NMRMSG information (rules in 6.6) |
| 04 | 4 | 9 | NMRTO | mixed | Destination: |
| 04 | 4 | 8 | - NMRTONOD | char | Destination node |
| 0C | 12 | 1 | - NMRTOQUL | bin | Destination qualifier (member/system id), default 0 |
| 0D | 13 | 8 | NMROUT | char/bin | Userid / remote / console / MCS info (6.4). Default zeros. |
| 15 | 21 | 9 | NMRFM | mixed | Origin: |
| 15 | 21 | 8 | - NMRFMNOD | char | Originating node |
| 1D | 29 | 1 | - NMRFMQUL | bin | Origin qualifier, default 0 |
| 1E | 30 | var | NMRMSG | mixed | Command text / formatted command / message |

### 6.3 Flag bits

NMRFLAG (byte 0):

| Mask | Name | Command meaning | Message meaning |
|---|---|---|---|
| X'80' | NMRFLAGC | 1 = NMR contains a **command** | 0 = **message** |
| X'40' | NMRFLAGW | NMROUT = originating remote (JES2 remote number) | NMROUT = destination remote (NMRRMT, 'Rnnnn') |
| X'20' | NMRFLAGT | NMROUT = **originating userid** | NMROUT = **destination userid** (NMRUSER) |
| X'10' | NMRFLAGU | NMROUT = originating console id | NMROUT = destination console (NMRUCM/NMRUCMA/NMRLINET) |
| X'08' | NMRFLAGR | Console is remote-authorized only | not used |
| X'04' | NMRFLAGJ | Console NOT job-authorized | not used |
| X'02' | NMRFLAGD | Console NOT device-authorized | not used |
| X'01' | NMRFLAGS | Console NOT system-authorized | not used |

(Authority bits are "negative": 0 = authorized. The receiving node still
applies its own node command authority.)

NMRLEVEL / NMRPRIO (byte 1):
- Commands: not used; "should be set" to X'70' / X'07' => **byte = X'77'**.
- Messages, NMRLEVEL (high nibble): X'10' non-essential, X'30' normal,
  X'50' delayed operator action, X'70' essential, X'80' immediate operator
  action, X'F0' extremely important. RSCS: X'70' for notify messages, X'30'
  for command responses. NMRPRIO (low nibble): everybody sets 7.
  => typical message byte: **X'77'** (notify) or **X'37'** (cmd response).

NMRTYPE (byte 2):

| Mask | Name | Command meaning | Message meaning |
|---|---|---|---|
| X'F0' | NMRTYPEX | reserved | reserved |
| X'10' | NMRTYPE5 | (unformatted cmd table only) sending subsystem included in NMRMSG | - |
| X'08' | NMRTYPE4 | 0 for commands (security signature use) | 1 = **sending userid** included in NMRMSG |
| X'04' | NMRTYPET | 0 for commands (security signature use) | 1 = **NO timestamp** in NMRMSG |
| X'02' | NMRTYPEF | 1 = **formatted** command, 0 = unformatted | not used |
| X'01' | NMRTYPED | reserved | DOM request (not set/used by anyone) |

### 6.4 Identifying the contents (Table 33) and NMROUT (Table 34)

Table 33:

| NMRFLAGC | NMRTYPEF | Contents |
|---|---|---|
| 1 | 0 | Unformatted command |
| 1 | 1 | Formatted command |
| 0 | n/a | Message |

Table 34 (NMRFLAG bits W=X'40', T=X'20', U=X'10'):

| W | T | U | NMROUT contains |
|---|---|---|---|
| 0 | 0 | 0 | MCS routing info: NMRDESC (X'0D',2) + NMRROUT (X'0F',2) + NMRCONID (X'11',4) |
| 0 | 0 | 1 | Console identifier: NMRUCM (X'0D',1) + NMRUCMA (X'0E',1) + NMRLINET (X'0F',2) |
| 0 | 1 | 0 | Userid (NMRUSER, 8 char) |
| 1 | 0 | 0 | Remote workstation id (NMRRMT, 8 char, JES2 'Rnnnn') |
| 1 | 1 | 0 | Userid OR remote - origin couldn't tell; destination must decide (JES3: check remote consoles first, else TSO userid) |

Message-specific NMROUT overlays (Table 38):

| Off hex | Len | Field | When |
|---|---|---|---|
| 0D | 1 | NMRUCM | MCS console id, NMRFLAGU set |
| 0E | 1 | NMRUCMA | MCS console area, NMRFLAGU set |
| 0F | 2 | NMRLINET | multi-line WTO line spacer, NMRFLAGU set |
| 0D | 2 | NMRDESC | MCS descriptor codes, T and U both 0 (POWER: X'0800' if no userid) |
| 0F | 2 | NMRROUT | MCS routing codes, T and U both 0 (RSCS/POWER: X'4100' if no userid; JES3 may put a console number) |
| 11 | 4 | NMRCONID | Extended console id |
| 0D | 8 | NMRRMT | Remote name, NMRFLAGW set |
| 0D | 8 | NMRUSER | Destination userid, NMRFLAGT set |

### 6.5 Interpreting NMRMSG for messages (Table 37)

| NMRTYPE4 (X'08') | NMRTYPET (X'04') | NMRMSG layout |
|---|---|---|
| 0 | 0 | 8-byte timestamp, text at NMRMSG+8 |
| 0 | 1 | text at NMRMSG+0 |
| 1 | 0 | 8-byte timestamp, 8-byte userid, text at NMRMSG+X'10' |
| 1 | 1 | 8-byte userid (NMRECSID), text at NMRMSG+8 |

- NMRECSID (X'1E', 8 char) = originating userid when NMRTYPE4 is set (RSCS sets
  this for messages from a VM user).
- Text max 132 bytes.
- AMBIGUITY: the 8-byte timestamp format is not defined in this chapter
  (commonly EBCDIC 'hh.mm.ss'). Treat as opaque on input; when sending, set
  NMRTYPET=1 and omit it.
- AMBIGUITY: Table 37 says NMRTYPET is "at offset X'00'" - it is in NMRTYPE
  at offset X'02' (typo).

### 6.6 NMRML rules

| NMR kind | NMRML range | What NMRML counts |
|---|---|---|
| Unformatted command | 1-132 | Length of command text in NMRMSG |
| Formatted command | 1-132 | Length of formatted block; manual says it "includes NMRFM, even if it is not specified" (unclear - see ambiguities) |
| Message | 1-148 | Length of NMRMSG **including userid** if present, **excluding timestamp** if present |

So for a message: bytes on the wire after the header =
`NMRML + (NMRTYPET==0 ? 8 : 0)`.
AMBIGUITY: 148 = 8 (ts) + 8 (userid) + 132 (text) - but NMRML excludes the
timestamp, so the realistic max NMRML is 140. Accept up to 148 on input,
clamp to record length.

### 6.7 Unformatted command NMR (Table 35)

NMRFLAGC=1, NMRTYPEF=0. Text is **system-dependent syntax** for the target
(e.g. a JES2 command as typed at the target console). NMROUT = originating
userid/remote/console (per W/T/U). NMRTOQUL identifies which member should
process it (JES3 sets; others 0).

When the command completes, the executing node keeps the header and builds
**response message NMRs** back to NMRFMNOD with NMROUT = the original
originator (same W/T/U type), NMRFLAGC=0. Responses use NMRLEVEL X'30'
(RSCS). One NMR per response line.

### 6.8 Formatted command NMR (Table 36)

NMRFLAGC=1, NMRTYPEF=1. Only JES2 sends; AS/400, RSCS, JES3 accept and
convert; POWER silently discards. Body at NMRMSG:

| Off hex | Off dec | Len | Field | Meaning |
|---|---|---|---|---|
| 1E | 30 | 1 | NMRFOP | Opcode: NMRFOPD display, NMRFOPC cancel, NMRFOPA release, NMRFOPH hold, NMRFOPR reroute |
| 1F | 31 | 1 | NMRFFLG | X'80' NMRFFLGO (cancel: purge output / route: reroute output), X'40' NMRFFLGD (cancel + dump) |
| 20 | 32 | 2 | NMRFJID | Job id (0 unless given on $G) |
| 22 | 34 | 8 | NMRFORGN | Originating node of the job |
| 2A | 42 | 8 | NMRFJNAM | Job name |
| 32 | 50 | 8 | NMRFD | Reroute destination |
| 3A | 58 | 8 | NMRFR | Reroute remote (if not implied by NMRFD) |
| 42 | 66 | 4 | NMRFJNO | Expanded job number (up to 4,294,967,295) |

AMBIGUITY: the manual lists the opcodes as a numbered list 1-5 without hex
values; their encoded values are not given here. Do not send formatted
commands; on receive, log and/or reply "not supported".

---------------------------------------------------------------------------

## 7. Networking Connection Control Records (Chapter 7)

All NCC records: **RCB X'F0'**, SRCB = EBCDIC letter, never compressed.
I and J must be the only record in the transmission buffer; other F0 records
may be blocked together but not with non-F0 records (Ch.8 note).

| Type | SRCB char | EBCDIC | Who uses |
|---|---|---|---|
| Initial signon | 'I' | X'C9' | all |
| Response signon | 'J' | X'D1' | all |
| Reset signon | 'K' | X'D2' | JES2 only |
| Concurrence signon | 'L' | X'D3' | JES2, JES3 (JES3 only for TCP/IP NJE) |
| Add connection | 'M' | X'D4' | JES2 only |
| Subtract connection | 'N' | X'D5' | JES2 only |
| Signoff | 'B' | X'C2' | all |

### 7.1 Initial signon 'I' (Table 39) - 41 bytes (X'29')

| Off hex | Off dec | Len | Field | Meaning |
|---|---|---|---|---|
| 00 | 0 | 1 | NCCRCB | X'F0' |
| 01 | 1 | 1 | NCCSRCB | C'I' (X'C9') |
| 02 | 2 | 1 | NCCIDL | Length from RCB to end of record (X'29') |
| 03 | 3 | 8 | NCCINODE | Primary node name (EBCDIC) |
| 0B | 11 | 1 | NCCIQUAL | Member number (1-7) |
| 0C | 12 | 4 | NCCIEVNT | Not used for I (0) |
| 10 | 16 | 2 | NCCIREST | Partial node-to-node resistance (JES3/POWER/AS400 X'00C8', RSCS 0) |
| 12 | 18 | 2 | NCCIBFSZ | Max transmission buffer we can receive (> 300) |
| 14 | 20 | 8 | NCCILPAS | Line password (BSC) - blanks; or NCCIPRAW (secure signon string 1) |
| 1C | 28 | 8 | NCCINPAS | Node password - blanks if none |
| 24 | 36 | 1 | NCCIFLG | X'00'; X'40' NCCIFLGS = secure signon protocol |
| 25 | 37 | 4 | NCCIFEAT | Feature bits (in the first feature byte, X'25'): X'80' NCCIPREP, X'40' NCCITRM, X'10' NCCIPACK (multiple headers per buffer), X'08' NCCIRIF, X'04' NCCIMRCB (mixed RCBs per buffer), X'02' NCCINOS (non-output SYSOUT), X'01' NCCISSIN (spanned SYSIN). JES2 supports NCCIMDR (bit X'1') |
| 29 | 41 | | end | |

AMBIGUITIES: Table 39 prints NCCINODE at offset "4" (Table 40 says 3 - 3 is
correct: 3 + 8 = X'0B' = NCCIQUAL). Table 39 also shows "26 3 reserved",
overlapping NCCIFEAT (X'25' len 4); treat X'25'-X'28' as NCCIFEAT and the
record as ending at X'29'. Which feature bits sit in which of the 4 bytes is
not stated; assume the listed masks apply to byte X'25'. Send NCCIFEAT=0
for a minimal implementation.

### 7.2 Response signon 'J' (Table 40) - same layout as I

| Off hex | Len | Field | Meaning |
|---|---|---|---|
| 00 | 1 | NCCRCB | X'F0' |
| 01 | 1 | NCCSRCB | C'J' (X'D1') |
| 02 | 1 | NCCIDL | Length (X'29') |
| 03 | 8 | NCCINODE | Secondary node name |
| 0B | 1 | NCCIQUAL | Member number (1 = JES2 node) |
| 0C | 4 | NCCIEVNT | 0 if high end sends or secondary trunk; next higher sequence if low end and primary trunk; X'FFFFFFFF' = predefined connection |
| 10 | 2 | NCCIREST | Partial resistance |
| 12 | 2 | NCCIBUFSZ | Largest buffer the secondary can receive |
| 14 | 8 | NCCILPAS / NCCIPRAW | Line password / secure-signon unencrypted string 2 |
| 1C | 8 | NCCINPAS / NCCIPENC | Node password / secure-signon encrypted string 1 |
| 24 | 1 | NCCIFLG | X'80' NCCIFLGM multi-trunk response |
| 25 | 4 | NCCIFEAT | Feature bits (as 7.1) |

### 7.3 Reset signon 'K' (Table 41) and Concurrence 'L' (Table 42)

| Off hex | Len | Field | Meaning |
|---|---|---|---|
| 00 | 1 | NCCRCB | X'F0' |
| 01 | 1 | NCCSRCB | C'K' (X'D2') or C'L' (X'D3') |
| 02 | 1 | NCCCDL | Record length |
| 03 | 4 | NCCCEVNT | K: 0 if sent by high end/secondary trunk. L: CES value echoed |
| 07 | 2 | NCCCREST | Total node-to-node resistance (L); NCCIREST (K) |
| (12) | 8 | NCCCPENC | Encrypted password 2, secure signon only |

AMBIGUITY (significant): the tables print NCCCREST/NCCIREST at offset "10"
and NCCCPENC at "12" while also listing "7 2 reserved" / "7 3 reserved" -
internally inconsistent. The widely implemented non-secure layout is:
`F0 D3 09 EEEEEEEE RRRR` (9 bytes, resistance at offset 7). Use that; if
NCCCDL > 9 on receive, treat bytes beyond 9 as secure-signon data.

### 7.4 Add 'M' / Subtract 'N' connection (Table 43) - 27 bytes (X'1B')

JES2 only sends these; they announce link status between two nodes in the
network. A non-JES2 node can accept and ignore them.

| Off hex | Off dec | Len | Field | Meaning |
|---|---|---|---|---|
| 00 | 0 | 1 | NCCRCB | X'F0' |
| 01 | 1 | 1 | NCCSRCB | C'M' add (X'D4') / C'N' subtract (X'D5') |
| 02 | 2 | 1 | NCCADL | Record length (X'1B') |
| 03 | 3 | 8 | NCCANODA | Low-end node name |
| 0B | 11 | 1 | NCCAQULA | Low-end member number (1-7, default 1) |
| 0C | 12 | 8 | NCCANODB | High-end node name |
| 14 | 20 | 1 | NCCAQULB | High-end member number (1-7, default 1) |
| 15 | 21 | 4 | NCCAEVNT | Connection event sequence (CES) |
| 19 | 25 | 2 | NCCAREST | Total node-to-node resistance (0-8000) |
| 1B | 27 | | end | |

### 7.5 Signoff 'B' (Table 44) - 2 bytes

| Off | Len | Field | Value |
|---|---|---|---|
| 00 | 1 | NCCRCB | X'F0' |
| 01 | 1 | NCCSRCB | C'B' (X'C2') |

(For TCP/IP NJE the I/J records are wrapped differently - see the TCP/IP
chapter notes; this chapter defines only the record bodies.)

---------------------------------------------------------------------------

## 8. Recommended exact byte layouts

EBCDIC (CP037) used below: blank 40, 'A'-'I' C1-C9, 'J'-'R' D1-D9,
'S'-'Z' E2-E9, '0'-'9' F0-F9.
Example nodes: our node `GONJE` = C7 D6 D5 D1 C5 40 40 40; target `NODEA` =
D5 D6 C4 C5 C1 40 40 40; user `MOSHE` = D4 D6 E2 C8 C5 40 40 40;
our sender `OPER` = D6 D7 C5 D9 40 40 40 40.

All below are the NMR body; it is sent as an NJE record with RCB X'9A',
SRCB X'80' (subject to normal record compression/framing).

### 8.a Text message to user U at node N

```
off  bytes                         field
00   20                            NMRFLAG: FLAGC=0 (message), FLAGT=1 (NMROUT=dest userid)
01   77                            NMRLEVEL=7 (essential) / NMRPRIO=7
02   0C                            NMRTYPE: TYPE4=1 (sender userid in text) + TYPET=1 (no timestamp)
                                   (use 04 instead to omit sender userid)
03   ML                            NMRML = 8 + len(text)   (or len(text) if NMRTYPE=04)
04   D5 D6 C4 C5 C1 40 40 40       NMRTONOD = 'NODEA'
0C   00                            NMRTOQUL
0D   D4 D6 E2 C8 C5 40 40 40       NMROUT/NMRUSER = 'MOSHE'
15   C7 D6 D5 D1 C5 40 40 40       NMRFMNOD = 'GONJE'
1D   00                            NMRFMQUL
1E   D6 D7 C5 D9 40 40 40 40       NMRECSID = sender userid 'OPER' (only if TYPE4)
26   <EBCDIC text, 1..132 bytes>   message text (no padding needed)
```
Total length = 30 + ML (<= 30+140 = 170).

Variant - message to the **operator console** of node N (not a user):
NMRFLAG = X'00'; NMROUT = NMRDESC X'0000', NMRROUT X'4100', NMRCONID
X'00000000' (what RSCS/POWER send when no userid). Alternatively
NMRFLAGU (X'10') with NMRUCM/NMRUCMA = 0 for the master console.
Variant - to JES2 remote Rnn: NMRFLAG = X'40', NMROUT = 'R12     '.

### 8.b Operator command to node N (unformatted)

```
off  bytes                         field
00   A0                            NMRFLAG: FLAGC=1 (command) + FLAGT=1 (NMROUT=originating userid)
                                   authority bits R/J/D/S = 0
01   77                            NMRLEVEL/NMRPRIO "should be" X'70'/X'07'
02   00                            NMRTYPE: TYPEF=0 (unformatted), TYPE4/TYPET=0
03   ML                            NMRML = len(command), 1..132
04   D5 D6 C4 C5 C1 40 40 40       NMRTONOD = 'NODEA'
0C   00                            NMRTOQUL (0 = any member)
0D   D6 D7 C5 D9 40 40 40 40       NMROUT = originating userid 'OPER'
15   C7 D6 D5 D1 C5 40 40 40       NMRFMNOD = 'GONJE'
1D   00                            NMRFMQUL
1E   <EBCDIC command text>         e.g. '$D A' for JES2 (system-dependent syntax)
```
Alternative origin as a console: NMRFLAG = X'90' (C + U), NMROUT = console
id. Responses: the target returns one **message NMR per response line**,
NMRTONOD = GONJE, NMRFLAGC=0, NMROUT = 'OPER' with NMRFLAGT set (it reuses
the header data we sent), NMRLEVEL typically X'3x'. JES2 typically includes a
timestamp (NMRTYPET=0). Route by NMROUT userid to the waiting requester.

Note: whether the target executes it depends on its node authority settings
for our node (outside this chapter); the command text format is entirely
up to the target product (JES2 '$' commands, RSCS commands, etc.).

### 8.c Parsing an inbound NMR (algorithm)

```
if len(rec) < 30: drop
flag, lvl, typ, ml = rec[0], rec[1], rec[2], rec[3]
toNode, toQual = ebcdic(rec[4:12]).trimRight(), rec[12]
out  = rec[13:21]
fmNode, fmQual = ebcdic(rec[21:29]).trimRight(), rec[29]
body = rec[30:]
if toNode != ourNode: forward to next hop (do not spool) or drop
if flag & 0x80:                         # command
    if typ & 0x02: formatted command -> parse 8.3 table; usually reject
    else: cmdText = ebcdic(body[:ml])
    origin kind from W/T/U bits (0x40/0x20/0x10) -> out = userid/remote/console
    execute / reject; reply with message NMRs to fmNode, NMROUT=out, same W/T/U bit
else:                                   # message
    level, prio = lvl >> 4, lvl & 0x0F
    p = 0
    ts = None
    if not (typ & 0x04): ts = body[0:8]; p = 8   # timestamp present
    sender = None
    n = ml
    if typ & 0x08: sender = ebcdic(body[p:p+8]).trim(); p += 8; n -= 8
    text = ebcdic(body[p:p+n])  (clamp to len(body))
    dest by Table 34: T(0x20)->user out, W(0x40)->remote out,
      U(0x10)->console (out[0]=UCM, out[1]=UCMA, out[2:4]=LINET),
      none -> operator: DESC=out[0:2], ROUT=out[2:4], CONID=out[4:8]
      W+T -> userid or remote, destination decides
```

### 8.d Minimal SYSOUT DSH (print, ASA CC, LRECL 133) to user U at node N

```
00 00 7C 00 00                     prefix: NDHLEN=124, NDHFLAGS=0, NDHSEQ=0
04 00 78 00 00                     NDHGLEN=120, TYPE=00, MOD=00
08 cE('NODEA')                     NDHGNODE
10 cE('MOSHE')                     NDHGRMT  (blanks for node default / LOCAL)
18 cE('')  20 cE('')  28 cE('SYSPRINT')   PROC, STEP, DD
30 00 01                           NDHGDSNO = 1
32 00                              reserved
33 C1                              NDHGCLAS 'A'
34 nn nn nn nn                     NDHGNREC = number of records
38 00                              NDHGFLG1
39 84                              NDHGRCFM = F + ASA   (X'80' if no CC, X'82' machine)
3A 00 85                           NDHGLREC = 133 (incl. CC byte)
3C 01                              NDHGDSCT copies
3D 00                              NDHGFCBI
3E 00                              NDHGLNCT
3F 00                              reserved
40 cE('') FORM  48 cE('') FCB  50 cE('') UCS  58 cE('') XWTR  60 cE('') NAME
68 80                              NDHGFLG2 = print
69 00                              NDHGUCSO
6A 00 00                           reserved
6C cE('LINE')                      NDHGPMDE
74 00 00 00 00                     NDHGSEGN
78 00 00 00 00                     reserved     -> total 0x7C = 124 bytes
```
(Offsets here are absolute within the DSH record, i.e. general-section offset
+ 4.) Data records then use SYSOUT SRCB X'A0' (ASA). For machine CC use
NDHGRCFM X'82'/X'42' and SRCB X'90'; for no CC use X'80' and SRCB X'80'.
For a TSO/E RECEIVE-able (NETDATA) file, set NDHGXWTR = NDHGRMT = userid and
punch (NDHGFLG2 X'40', NDHGRCFM X'80', LRECL 80).

---------------------------------------------------------------------------

## 9. Ambiguities / gotchas summary

1. NDHLEN: "combined length of all sections" - include the 4-byte prefix.
2. DSH segmentation layout for >253 bytes not described in Ch.4.
3. NDHGRCFM bits 2-4,7 undocumented; they follow DCB RECFM convention.
4. NDHGLNCT text says "NDHGF1LC (offset X'01' bit)" - actually NDHGFLG1
   (X'34') mask X'04'.
5. No NDHGDSNM / NDHGCPDS fields exist; dataset "name" is NDHGNAME only.
6. NMR timestamp format undefined; send with NMRTYPET=1.
7. NMRML message max 148 vs. practical 140; formatted-command NMRML
   "includes NMRFM" wording is unclear.
8. NMRTYPE5 (X'10') listed in unformatted command table while X'F0' marked
   reserved - send 0.
9. Formatted command opcode values (NMRFOP) not given numerically.
10. Table 37 mislocates NMRTYPET (says offset 0; it is NMRTYPE, offset 2).
11. NCC: Table 39 NCCINODE offset typo (4 vs 3); reserved field overlaps
    NCCIFEAT; K/L record offsets internally inconsistent (use 9-byte form).
12. EBCDIC code page not specified by these chapters.
13. NMRTOQUL/NMRFMQUL: 0 = default; JES2 MAS members may need the member
    number - start with 0.
