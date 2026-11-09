# gonje

gonje is an IBM NJE (Network Job Entry) node that speaks NJE over TCP/IP.
It connects to other NJE nodes (RSCS, JES2, another gonje), sends and
receives messages, commands and files, and has a colored operator shell.

## Build

    go build -o gonje ./cmd/gonje

Run the tests with `go test -race ./...`.

To build binaries for Linux amd64, Linux arm64 and macOS arm64 into
`bin/`, run `./build.sh`. Then `./start.sh` runs the highest-version
binary for this machine; any arguments are passed on to gonje, e.g.
`./start.sh -c test/nodeA.cnf -split`.

`./deploy.sh` copies the source to `moshix.tech:/root/gonje` and then
runs `./moshix.sh`. It never copies `gonje.cnf`, logs, inbox/outbox or
binaries, so the remote config is left alone. `-n` is a dry run, `-b`
copies without building.

`./moshix.sh` builds the already-deployed source on moshix.tech: vet,
tests, then the remote `./build.sh` into `/root/gonje/bin/`, so
`./start.sh` there runs the new build. `-t` skips vet and tests.

## Configure

Copy the sample and edit it:

    cp gonje.cnf.example gonje.cnf

The file holds `key=value` pairs separated by newlines, spaces or commas.
A word starting with `#` starts a comment (a `#` inside a value is kept).

| Key | Meaning |
|---|---|
| `local_node` | our NJE node name (1-8 characters) |
| `local_IP`, `local_port` | where we listen (default 0.0.0.0:175) |
| `send_dir` | where `ls` and `send` look for files (default ./outbox) |
| `recv_dir` | where received files are stored (default ./inbox) |
| `log_level` | debug, info, warn or error (default debug) |
| `split_view` | yes/no: start in split view (default yes) |
| `split_ratio` | event panel height in percent (default 40) |
| `panel_level` | 1, 2 or 3 (see below) |
| `msg_pane` | height of the messages pane (default 6, 3-50) |
| `msg_bell` | yes = ring the terminal bell on incoming messages |
| `route` | `route=<dest>,<via>[,<alt>]`: reach a node through a directly linked one (alt when via is down); one line per route |
| `default_route` | `default_route=<node>`: every other unknown node is sent there (must be a defined remote) |
| `timeout` | stop retrying a remote that could not be reached for this many seconds (0 = never; `connect` starts again) |
| `remote_host_name` | starts a remote node block |
| `remote_host_addr` | its host or IP; leave out to only accept calls |
| `remote_host_port` | its port (default 175) |
| `remote_keepalive` | seconds without any block from the peer before a null buffer restarts the exchange; 3x this = link dead (default 10) |
| `remote_password` | optional node password (never written to the log) |
| `remote_turn` | free (default): send whenever there is data, dead links found by TCP keepalive; strict: BSC-style turn-taking, dead after 3x keepalive of silence |
| `remote_buffersize` | NJE buffer size we offer, 300-32768 (default 8192); we send at most the smaller of ours and the peer's |
| `remote_max_streams` | concurrent file transfers each way, 1-8 (default 1) |
| `remote_strict_bcb` | yes = on a block sequence error send E0 and drop the link, as the manual says; no (default) = log it and resynchronise |

Received files are named
`<FROMNODE>.<JOBNAME>.<DSNAME>.<yyyymmddhhmmss>.txt` (text) or `.bin`.
Text files are sent as print files, with lines longer than 132
characters (the RSCS print width) split into several lines; binary
files are sent as punch files (80-byte records).

## Run

    ./gonje [-c|-f gonje.cnf] [-split=true|false] [-log gonje.log]

- `-c` or `-f` config file (default `gonje.cnf`)
- `-split=false` start with a single pane (default: `split_view`, yes)
- `-log` log file (default `gonje.log`)

On a terminal gonje takes over the screen with framed panes (Unicode
box drawing, or ASCII when the locale is not UTF-8 or `GONJE_ASCII=1`)
and restores it on exit. Split view: commands and results on top,
incoming events below. Single pane: both interleaved. Incoming
messages and command responses always have their own white-framed pane
at the bottom. Long output is
paged: `C`/Space/Enter for more, `Q`/Esc to stop. Colors follow
`NO_COLOR`; piped (non-tty) use gives plain lines.

gonje connects to every remote that has an address and reconnects after
errors. Once linked, data is sent as soon as it is ready and an idle
link sends an empty block every `remote_keepalive` seconds
(`remote_turn=strict` switches to BSC-style turn-taking). Everything
goes to the log (mode 0600): operator commands and their results, and
hex dumps of all blocks except the idle ones, which are summarised once
a minute. Messages still queued when a link drops are reported as not
delivered.

## Batch mode

One action, then exit (no listener, no screen; progress and responses
on stderr; exit code 0 = success, 1 = failure):

    gonje -c send file.txt NODEID USERID
    gonje -c cmd NODEID cp q users      # waits up to 30 s for responses (-w <s>)
    gonje -c msg NODEID USERID hello how are you?
    gonje -v                            # prints "gonje <version>"

In batch mode the config file is `gonje.cnf`, or `-f <file>` (which
also works in interactive mode). The link is closed without signoff.

## Shell commands

| Command | Action |
|---|---|
| `help [command]` | list commands, or details of one |
| `nodes` / `link list` | show links |
| `connect <node>` | connect a link (cancels a drain) |
| `drop <node>` | sign off a link (no auto-reconnect) |
| `drain <node>` | let running transfers finish, then drop |
| `link define <node> <addr> [opts] [save]` | add a link (`-` = passive) |
| `link change <node> [addr] [opts] [save]` | change a link |
| `link delete <node> [save]` | remove a link |
| `msg <node> <user> <text>` | send a message (`*` = operator) |
| `cmd <node> <command>` | send a command, e.g. `cmd NODEB Q SYS` |
| `send <node> <user> <file>` | send a file |
| `route <dest> to <via> [alt <alt>] [save]` | route a node through another link (`route delete`, `route default <node>\|delete`, `route list`) |
| `spool [<#\|file> \| delete <#\|file>]` | received files with their NJE details |
| `ls` / `inbox` | list send_dir / recv_dir |
| `local [name\|ip\|port\|timeout <value>] [save]` | show or change the local node |
| `stats [<node>\|reset [<node>]]` | traffic counters |
| `level 1\|2\|3` | event detail |
| `split on\|off\|<percent>\|msgs <n>` | split view, single pane, events or messages pane size |
| `log` / `version` | end of the log / gonje version |
| `quit` | close all links and exit (no signoff, so peers accept us straight back; `drop`/`drain` do sign off) |

Link options: `port=<n> keepalive=<s> password=<pw> strict_bcb=yes|no turn=free|strict buffersize=<n> max_streams=<n>`. A sent file the peer does not confirm within 10 s counts as sent.
With `save` the change is also written to the config file (`-c`); other
lines and comments are kept. Renaming the local node makes every active
link sign off and reconnect with the new name.

Routing: a direct link wins, then a `route`, then the `default_route`.
gonje also forwards messages and files for other nodes (store-and-forward;
origin kept, at most 16 hops, never back over the link they came in on).
Each received file gets a `.meta` sidecar used by `spool`. Files sent with
TSO/NJE38 TRANSMIT (NETDATA) are unpacked and named after the data set
(e.g. `HOUCICS.HERC01.HERC01.SENDFILE.<time>.txt`); a PDS is kept as `.bin`.

Incoming `Q SYS`, `Q LINKS` and `Q FILES` commands are answered
automatically; other commands get "unsupported command".

Keys: Tab moves the scroll focus between the panes (commands, events,
messages), Ctrl-U/Ctrl-D
scroll the focused pane, Ctrl-G/Ctrl-V (or Ctrl-Up/Ctrl-Down) resize the
events pane, Ctrl-L redraws. Line editing: Up/Down or Ctrl-P/Ctrl-N
history (saved in `~/.gonje_history`), Ctrl-R search history,
Ctrl-A/Home, Ctrl-E/End, Ctrl-B/Left, Ctrl-F/Right, Alt-B/Alt-F or
Ctrl-Left/Right by word, Backspace, Delete, Ctrl-K kill to end, Ctrl-W
kill word, Ctrl-Y yank, Ctrl-T transpose, Ctrl-C clear the line.

## Panel levels

- **L1**: incoming commands and incoming files.
- **L2**: L1 plus messages, responses, outgoing traffic, transfer
  progress, link up/down, signon/signoff, stream control and errors.
- **L3**: L2 plus a hex dump of every record in both directions;
  keepalives are collapsed into one line like `keepalive x12 NODEB`.

## Test with two local nodes

`test/nodeA.cnf` (port 1175) and `test/nodeB.cnf` (port 2175) point at
each other. In two terminals, from the repository root:

    ./gonje -c test/nodeA.cnf -log nodeA.log
    ./gonje -c test/nodeB.cnf -log nodeB.log

Then, in node A's shell:

    nodes
    msg nodeb bob hello
    cmd nodeb q sys
    send nodeb bob /etc/hosts
    level 3

Node B shows the message, the command and the file, which lands in
`test/nodeB/inbox/`. Try `drop nodeb` and `connect nodeb` too.

## License

gonje is free software, licensed under the GNU General Public License
version 2 (GPL-2.0). See [LICENSE](LICENSE).
