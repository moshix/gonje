# gonje — approved development plan

gonje is a Go program acting as an NJE-over-TCP/IP node (z/OS NJE Formats
and Protocols SA32-0988-02, Chapter 12). It sends/receives NMR messages and
commands and SYSOUT files, has a colored shell, and logs everything to
gonje.log. Protocol spec notes (extracted from the manual) are in docs/spec/.
First real target: Sine Nomine's RSCS-style NJE server (non-JES2), so no
JES2 path-manager records; plain I/J signon only.

## Config: gonje.cnf
key=value tokens separated by newlines, spaces or commas. `#` comments.

    local_node=MYNODE
    local_IP=0.0.0.0
    local_port=175
    send_dir=./outbox
    recv_dir=./inbox
    log_level=debug
    split_view=yes
    split_ratio=40
    panel_level=1
    remote_host_name=REMNODE          # starts a new remote block
    remote_host_addr=remote.node.com
    remote_host_port=175
    remote_keepalive=10               # seconds, default 10
    remote_password=                  # optional

Unknown keys -> warning in log, ignored. Node names 1-8 chars, uppercased.

## Packages (stdlib + golang.org/x/term only)
cmd/gonje, internal/config, internal/logx, internal/ebcdic (CP037),
internal/nje/scb, internal/nje/tcpip (OPEN/ACK/NAK, TTB/TTR),
internal/nje/records (I/J/B signon, NJH, NDH, NJT, NMR),
internal/nje/link (per-remote state machine), internal/node (manager,
listener, event bus), internal/shell (classic + split view).

## Protocol decisions (tolerant until confirmed by a trace)
- Send DLE STX at start of each TTR record; accept with or without.
  No DLE doubling; don't send DLE ETB, accept it if received.
- No path manager, multi-job option off (always 90/A0 exchange).
- Never segment outbound headers; reassemble segmented inbound headers.
- No SSL in v1.
- NAK X'01' -> stop; X'02'-X'04' -> retry after random backoff.
- Keepalive: null buffer when link idle `keepalive` seconds; no traffic
  for 3x keepalive -> link declared dead and dropped.
- Inbound commands are auto-answered: Q SYS, Q LINKS, Q FILES (response
  NMR messages); anything else -> "unsupported command" response.
- Received files: decode as EBCDIC; if it looks like real text (high
  ratio of printable chars/whitespace) save as ASCII with ASA carriage
  control stripped, else save raw bytes untranslated. Name:
  recv_dir/<FROMNODE>.<JOBNAME>.<NDHGNAME>.<yyyymmddhhmmss>.txt|.bin
- Sent files: same heuristic on the local file; text -> ASCII->EBCDIC
  records, binary -> untranslated records, no carriage control.

## Shell
Commands: help, nodes/status, connect <node>, drop <node>,
msg <node> <user> <text>, cmd <node> <command>, send <node> <user> <file>,
ls, inbox, log, level 1|2|3, split on|off, quit.
Colors: green OK, red error, cyan MSG, magenta CMD/RSP, green FILE,
yellow LINK. No column limit: output follows the real terminal width.

Split view (golang.org/x/term raw mode + ANSI): top panel = input with
line editing + history and command results; bottom panel = incoming
events, 500-line buffer, PgUp/PgDn scroll, height split_ratio%. Resize
redraws. Non-tty -> classic mode. Classic mode prints events above the
prompt without corrupting the typed line.

Panel levels:
- L1: incoming commands + incoming files.
- L2: L1 + messages, responses sent, outgoing msg/cmd/file, transfer
  progress, link up/down, signon/signoff, stream control, errors.
- L3: L2 + hex dump of EVERY record in both directions (NMR, signon,
  headers, trailers, stream control, every data record) after SCB
  decompression. Keepalives are collapsed into a summary line
  (e.g. "keepalive x12 REMNODE"). Dump format, 16 bytes/line, right
  column = EBCDIC-decoded readable chars, '.' for non-printables:
  `0000  E3 C5 E2 E3 40 D4 C5 E2  E2 C1 C7 C5 00 00 00 00  TEST MESSAGE....`
gonje.log always gets full L3 detail plus raw TTB/TTR block dumps.

## Testing
Unit tests per package; integration test: two gonje nodes in-process on
127.0.0.1 (unprivileged ports) exchange message, command (+auto-answer),
text file and binary file, under go test -race. Then manual test with two
gonje processes locally.
