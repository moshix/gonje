package shell

import (
	"strings"
)

// helpEntry documents one command.
type helpEntry struct {
	group   string
	cmd     string   // command word(s), e.g. "link define"
	args    string   // argument synopsis
	desc    string   // one-line description
	detail  []string // extra lines for "help <command>"
	example string
}

// helpGroups fixes the order of the sections.
var helpGroups = []string{"Links", "Messages & commands", "Files", "Local node", "Display", "Misc"}

var helpTable = []helpEntry{
	{"Links", "nodes", "", "show all links (also: status, link list)", nil, "nodes"},
	{"Links", "connect", "<node>", "connect a link, resume auto-reconnect", nil, "connect NODEB"},
	{"Links", "drop", "<node>", "sign off a link, no auto-reconnect", nil, "drop NODEB"},
	{"Links", "drain", "<node>", "finish transfers, then drop",
		[]string{"running transfers finish; queued files wait for the next connect;",
			"the peer's new files are refused (it keeps them). drain/drop again = drop now,",
			"connect cancels the drain"}, "drain NODEB"},
	{"Links", "link define", "<node> <addr> [opts] [save]", "add a link (addr - = passive)",
		[]string{"opts: port=<n> keepalive=<s> password=<pw> strict_bcb=yes|no turn=free|strict eof_wait=<s>",
			"save writes the link to the config file (-c)"},
		"link define NODEC 10.1.1.5 port=175 keepalive=10 save"},
	{"Links", "link change", "<node> [addr] [opts] [save]", "change a link (used on next connect)",
		[]string{"same options as link define; only the given ones change"},
		"link change NODEC keepalive=30 save"},
	{"Links", "link delete", "<node> [save]", "drop and remove a link", nil, "link delete NODEC save"},
	{"Messages & commands", "msg", "<node> <user> <text>", "send a message (user * = operator)",
		nil, "msg NODEB MAINT hello there"},
	{"Messages & commands", "cmd", "<node> <command>", "send a command to a node",
		[]string{"the response comes back as RSP events"}, "cmd NODEB Q SYS"},
	{"Files", "send", "<node> <user> <file>", "send a file (path or name in send_dir)",
		[]string{"text goes as a print file, binary as a punch file"}, "send NODEB MAINT report.txt"},
	{"Files", "ls", "", "list send_dir", nil, "ls"},
	{"Files", "inbox", "", "list received files", nil, "inbox"},
	{"Local node", "local", "", "show local name, address and port", nil, "local"},
	{"Local node", "local name", "<node> [save]", "rename (active links re-signon)",
		[]string{"every active link signs off and reconnects with the new name"}, "local name NODEX save"},
	{"Local node", "local ip", "<addr> [save]", "listen on another address", nil, "local ip 0.0.0.0"},
	{"Local node", "local port", "<n> [save]", "listen on another port", nil, "local port 1175 save"},
	{"Display", "level", "1|2|3", "event detail (3 = hex dumps)", nil, "level 3"},
	{"Display", "split", "on|off|<percent>", "split view, or events pane size",
		[]string{"keys: Ctrl-G/Ctrl-Up grow, Ctrl-V/Ctrl-Down shrink the events pane",
			"Tab moves the scroll focus between the panes; Ctrl-U/Ctrl-D scroll it"}, "split 30"},
	{"Misc", "stats", "[node|reset [node]]", "traffic counters (all links or one)",
		[]string{"stats: totals and a table of all links; stats <node>: every counter",
			"stats reset [<node>]: zero the counters (in memory only)"}, "stats NODEB"},
	{"Misc", "log", "", "show the end of the log file", nil, "log"},
	{"Misc", "version", "", "show the gonje version", nil, "version"},
	{"Misc", "help", "[command]", "this list, or details of one command", nil, "help link define"},
	{"Misc", "quit", "", "close all links (no signoff) and exit", nil, "quit"},
}

// helpCol is the width of the "command args" column.
func helpCol() int {
	w := 0
	for _, e := range helpTable {
		w = max(w, Width(e.cmd)+1+Width(e.args))
	}
	return w
}

// helpLines renders the command table for a screen of the given width
// (0 = no limit). The command column is padded to a common width; a
// description that does not fit wraps onto continuation rows aligned
// under the description column, so the table never loses its shape. On
// very narrow screens the description moves to its own indented row.
func helpLines(width int) []Line {
	col := helpCol()
	descAt := 2 + col + 2
	room := 0
	if width > 0 {
		room = width - descAt
	}
	var out []Line
	for _, g := range helpGroups {
		out = append(out, Line{Text: g, Color: BoldYellow})
		for _, e := range helpTable {
			if e.group != g {
				continue
			}
			pad := strings.Repeat(" ", col-Width(e.cmd)-1-Width(e.args))
			head := []Seg{{"  ", Default}, {e.cmd, BoldCyan}, {" ", Default}, {e.args, Dim}, {pad + "  ", Default}}
			switch {
			case width <= 0 || room >= Width(e.desc):
				out = append(out, SegLine(append(head, Seg{e.desc, Default})...))
			case room >= 15:
				parts := Wrap(e.desc, room, "")
				out = append(out, SegLine(append(head, Seg{parts[0], Default})...))
				for _, p := range parts[1:] {
					out = append(out, SegLine(Seg{strings.Repeat(" ", descAt) + p, Default}))
				}
			default:
				out = append(out, fitLine(SegLine(head[:4]...), width))
				for _, p := range Wrap(e.desc, width-6, "") {
					out = append(out, Line{Text: "      " + p})
				}
			}
		}
	}
	out = append(out, Line{Text: "Keys: Up/Down history, Ctrl-R search, Tab pane focus, Ctrl-U/Ctrl-D scroll,", Color: Dim, Cont: "  "},
		Line{Text: "      Ctrl-G/Ctrl-V resize panes, Ctrl-L redraw. Long output pages (C/Q).", Color: Dim, Cont: "  "},
		Line{Text: "      help <command> for details.", Color: Dim, Cont: "  "})
	return out
}

// helpFor shows the details of one command ("link define", "msg", ...).
// It matches the longest command name the words start with.
func helpFor(words []string) []Line {
	q := strings.ToLower(strings.Join(words, " "))
	var best *helpEntry
	// Exact command (or command followed by arguments): the longest wins,
	// so "link define X" finds "link define", not "link ...".
	for i := range helpTable {
		e := &helpTable[i]
		if (q == e.cmd || strings.HasPrefix(q, e.cmd+" ")) && (best == nil || Width(e.cmd) > Width(best.cmd)) {
			best = e
		}
	}
	// Otherwise the first command that starts with the query ("link").
	for i := range helpTable {
		if best == nil && strings.HasPrefix(helpTable[i].cmd, q) {
			best = &helpTable[i]
		}
	}
	if best == nil {
		return []Line{errText("no help for " + q + " (type help)")}
	}
	out := []Line{
		SegLine(Seg{"usage: ", Bold}, Seg{best.cmd, BoldCyan}, Seg{" " + best.args, Dim}),
		{Text: "  " + best.desc, Cont: "  "},
	}
	for _, d := range best.detail {
		out = append(out, Line{Text: "  " + d, Cont: "    "})
	}
	out = append(out, SegLine(Seg{"example: ", Bold}, Seg{best.example, Cyan}))
	return out
}
