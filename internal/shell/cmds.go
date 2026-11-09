// Copyright 2026 by moshix

package shell

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gonje/internal/config"
	"gonje/internal/node"
)

func usage(u string) Result { return Result{Lines: []Line{{Text: "usage: " + u, Color: Red}}} }

func fail(err error) Result { return Result{Lines: []Line{errLine(err)}} }

func (s *Shell) exec(line string) Result {
	line = s.lineRefs(line)
	w := strings.Fields(line)
	if len(w) == 0 {
		return Result{}
	}
	cmd := strings.ToLower(w[0])
	args := w[1:]
	switch cmd {
	case "help", "?":
		if len(args) > 0 {
			return Result{Lines: helpFor(args)}
		}
		return Result{Lines: helpLines(s.width)}
	case "version":
		return Result{Lines: []Line{SegLine(Seg{s.versionText(), BoldCyan})}}
	case "stats":
		return s.statsCmd(args)
	case "nodes", "status":
		lines := s.statusLines()
		if rs := s.be.Routes(); len(rs) > 0 {
			lines = append(lines, Line{Text: fmt.Sprintf("%d route(s), see route list", len(rs)), Color: Dim})
		}
		return Result{Lines: lines}
	case "connect":
		if len(args) < 1 {
			return usage("connect <node>")
		}
		if err := s.be.Connect(args[0]); err != nil {
			return fail(err)
		}
		return Result{Lines: []Line{ok("connecting to " + strings.ToUpper(args[0]))}}
	case "drop":
		if len(args) < 1 {
			return usage("drop <node>")
		}
		if err := s.be.Drop(args[0]); err != nil {
			return fail(err)
		}
		return Result{Lines: []Line{ok("dropping " + strings.ToUpper(args[0]))}}
	case "drain":
		if len(args) != 1 {
			return usage("drain <node>")
		}
		msg, err := s.be.Drain(args[0])
		if err != nil {
			return fail(err)
		}
		return Result{Lines: []Line{warnLine(msg)}}
	case "msg", "m":
		if len(args) < 3 {
			return usage("msg <node> <user> <text>")
		}
		what := "message to " + strings.ToUpper(args[0]) + "/" + strings.ToUpper(args[1])
		m := s.startMark()
		if err := s.be.SendMessageNotify(args[0], args[1], restAfter(line, 3), s.notify(m, what)); err != nil {
			return s.markFail(m, err)
		}
		return s.sentResult(m, what+" queued", ok("message sent to "+strings.ToUpper(args[0])+"/"+strings.ToUpper(args[1])))
	case "cmd", "c":
		if len(args) < 2 {
			return usage("cmd <node> <command>")
		}
		what := "command to " + strings.ToUpper(args[0])
		m := s.startMark()
		if err := s.be.SendCommandNotify(args[0], restAfter(line, 2), s.notify(m, what)); err != nil {
			return s.markFail(m, err)
		}
		return s.sentResult(m, what+" queued", ok("command sent to "+strings.ToUpper(args[0])+
			"; responses will appear in the messages pane"))
	case "send":
		if len(args) < 3 {
			return usage("send <node> <user> <file>")
		}
		what := "file " + restAfter(line, 3) + " for " + strings.ToUpper(args[0]) + "/" + strings.ToUpper(args[1])
		m := s.startMark()
		f, err := s.be.SendFileNotify(args[0], args[1], restAfter(line, 3), s.notify(m, what))
		if err != nil {
			return s.markFail(m, err)
		}
		kind := "text"
		if f.Binary {
			kind = "binary"
		}
		l := SegLine(Seg{f.Name, BoldGreen}, Seg{" queued for ", Green}, Seg{f.ToNode + "/" + f.ToUser, Bold},
			Seg{fmt.Sprintf(" (%d records, %s)", len(f.Records), kind), Green})
		l.Color = Green
		return s.sentResult(m, fmt.Sprintf("%s queued (%d records, %s)", what, len(f.Records), kind), l)
	case "spool", "sp":
		return s.spoolCmd(args)
	case "ls":
		return Result{Lines: listDir(s.set.SendDir)}
	case "inbox":
		return Result{Lines: listDir(s.set.RecvDir)}
	case "log":
		return Result{Lines: tailFile(s.set.LogPath, 20)}
	case "level":
		if len(args) != 1 || (args[0] != "1" && args[0] != "2" && args[0] != "3") {
			return usage("level 1|2|3")
		}
		s.level = int(args[0][0] - '0')
		return Result{Lines: []Line{ok("panel level " + args[0])}}
	case "split":
		return s.splitCmd(args)
	case "local":
		return s.localCmd(args)
	case "link":
		return s.linkCmd(args)
	case "route":
		return s.routeCmd(args)
	case "routes":
		return Result{Lines: s.routeLines()}
	case "quit", "exit", "bye":
		s.quitting = true
		return Result{Quit: true, Lines: []Line{ok("closing links (no signoff) and exiting")}}
	}
	return Result{Lines: []Line{{Text: "unknown command " + w[0] + " (type help)", Color: Red}}}
}

// splitCmd: split on|off|<percent>.
func (s *Shell) splitCmd(args []string) Result {
	if len(args) == 2 && strings.EqualFold(args[0], "msgs") {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 3 || n > 50 {
			return usage("split msgs <lines 3-50>")
		}
		s.msgLines = n
		return Result{Lines: []Line{ok(fmt.Sprintf("messages pane %d lines", n))}}
	}
	if len(args) != 1 {
		return usage("split on|off|<percent>|msgs <lines>")
	}
	switch args[0] {
	case "on":
		return Result{Split: 1}
	case "off":
		return Result{Split: -1}
	}
	pct, err := strconv.Atoi(strings.TrimSuffix(args[0], "%"))
	if err != nil || pct < 10 || pct > 90 {
		return usage("split on|off|<percent 10-90>")
	}
	s.set.SplitRatio, s.bottomLines = pct, 0
	s.note(fmt.Sprintf("events %d%%", pct))
	return Result{Lines: []Line{ok(fmt.Sprintf("events pane %d%% of the screen", pct))}}
}

// restAfter returns line with its first n words removed, keeping the
// spacing of the remainder (message text, command text, file names).
func restAfter(line string, n int) string {
	s := strings.TrimLeft(line, " \t")
	for i := 0; i < n; i++ {
		j := strings.IndexAny(s, " \t")
		if j < 0 {
			return ""
		}
		s = strings.TrimLeft(s[j:], " \t")
	}
	return s
}

// stateColor colors a link state: UP green, DOWN red, in between yellow.
func stateColor(st node.State) Color {
	switch st {
	case node.Up:
		return BoldGreen
	case node.Down:
		return Red
	}
	return Yellow
}

func (s *Shell) statusLines() []Line {
	out := []Line{{Text: fmt.Sprintf("%-4s %-8s %-10s %-22s %-8s %s", "LINE", "NODE", "STATE", "ADDRESS", "SINCE", "INFO"), Color: Bold, Clip: true}}
	for _, st := range s.be.Status() {
		since, info := "", ""
		infoColor := Default
		if st.State == node.Up {
			since = st.Since.Format("15:04:05")
			role := "sec"
			if st.Stats.Primary {
				role = "pri"
			}
			info = fmt.Sprintf("%s in %d/%d out %d/%d ka %d buf %d/%d str %d", role, st.Stats.MsgsIn, st.Stats.FilesIn,
				st.Stats.MsgsOut, st.Stats.FilesOut, st.Stats.KeepaliveIn, st.BufSize, st.Stats.PeerBufSize, st.MaxStreams)
		} else if st.Held {
			info, infoColor = "held", Dim
		}
		if st.Queued > 0 {
			info += fmt.Sprintf(" q=%d", st.Queued)
		}
		addr := st.Addr
		if addr == "" {
			addr = "(passive)"
		}
		if st.NatOut != "" || st.NatIn != "" {
			info += fmt.Sprintf(" nat %s/%s", orDash(st.NatIn), orDash(st.NatOut))
		}
		l := SegLine(Seg{fmt.Sprintf("%-4d ", st.Line), Dim}, Seg{fmt.Sprintf("%-8s ", st.Name), BoldCyan}, Seg{fmt.Sprintf("%-10s ", st.State), stateColor(st.State)},
			Seg{fmt.Sprintf("%-22s ", addr), Default}, Seg{fmt.Sprintf("%-8s ", since), Dim}, Seg{info, infoColor})
		l.Color = stateColor(st.State)
		l.Clip = true // a table row never wraps: the INFO column is cut with "…"
		out = append(out, l)
		if st.LastErr != "" && st.State != node.Up {
			out = append(out, Line{Text: "  last error: " + st.LastErr, Color: Dim, Cont: "    "})
		}
	}
	return out
}

// listDir lists the files in dir with size and time.
func listDir(dir string) []Line {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return []Line{errLine(err)}
	}
	var names []string
	for _, e := range ents {
		// Hide received-file sidecars and partial writes.
		// Also hide dot files (.spoolseq, editor temp files).
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasSuffix(e.Name(), ".meta") && !strings.HasSuffix(e.Name(), ".part") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := []Line{SegLine(Seg{dir, Bold}, Seg{": ", Default}, Seg{strconv.Itoa(len(names)), Yellow}, Seg{" file(s)", Default})}
	for _, n := range names {
		st, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		out = append(out, SegLine(Seg{fmt.Sprintf("  %10d", st.Size()), Yellow},
			Seg{"  " + st.ModTime().Format("01-02 15:04") + "  ", Dim}, Seg{n, Green}))
	}
	return out
}

// tailFile returns the last n lines of a file.
func tailFile(path string, n int) []Line {
	f, err := os.Open(path)
	if err != nil {
		return []Line{errLine(err)}
	}
	defer f.Close()
	var ring []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		ring = append(ring, sc.Text())
		if len(ring) > n {
			ring = ring[1:]
		}
	}
	out := []Line{{Text: "--- " + path + " ---", Color: Dim}}
	for _, l := range ring {
		out = append(out, Line{Text: l})
	}
	return out
}

// ---------------------------------------------------------------------
// local / link
// ---------------------------------------------------------------------

// stripSave removes a trailing "save" word and reports it.
func stripSave(args []string) ([]string, bool) {
	if n := len(args); n > 0 && strings.EqualFold(args[n-1], "save") {
		return args[:n-1], true
	}
	return args, false
}

// saveConfig applies edit to the config file and saves it atomically.
func (s *Shell) saveConfig(edit func(f *config.File)) Line {
	if s.set.ConfigPath == "" {
		return errText("no config file to save to")
	}
	f, err := config.OpenFile(s.set.ConfigPath)
	created := false
	if os.IsNotExist(err) && s.set.ConfigInit != "" {
		// Started with -o only: the explicit save writes a new file
		// holding the -o settings plus this change.
		f, err, created = config.NewFile(s.set.ConfigPath, s.set.ConfigInit), nil, true
	}
	if err != nil {
		return errLine(err)
	}
	edit(f)
	if err := f.Save(); err != nil {
		return errLine(err)
	}
	if created {
		s.set.ConfigInit = ""
		return ok("saved to new config file " + s.set.ConfigPath + " (the -o settings plus this change)")
	}
	return ok("saved to " + s.set.ConfigPath)
}

func (s *Shell) localCmd(args []string) Result {
	args, save := stripSave(args)
	li := s.be.Local()
	if len(args) == 0 {
		listen := li.Listen
		if listen == "" {
			listen = "(not listening)"
		}
		return Result{Lines: []Line{
			SegLine(Seg{"local node ", Default}, Seg{li.Name, BoldYellow}),
			SegLine(Seg{"configured ", Default}, Seg{fmt.Sprintf("%s port %d", li.IP, li.Port), Cyan}),
			SegLine(Seg{"listening  ", Default}, Seg{listen, Cyan}),
			SegLine(Seg{"timeout    ", Default}, Seg{fmt.Sprintf("%d s (0 = retry forever)", li.Timeout), Cyan}),
		}}
	}
	if len(args) != 2 {
		return usage("local [name <node>|ip <addr>|port <n>|timeout <s>] [save]")
	}
	var out []Line
	switch strings.ToLower(args[0]) {
	case "name":
		name, err := config.NodeName(args[1])
		if err != nil {
			return fail(err)
		}
		// Count what will bounce before doing it, for the warning.
		up, passive := 0, 0
		for _, st := range s.be.Status() {
			if st.State == node.Up {
				up++
				if st.Addr == "" {
					passive++
				}
			}
		}
		if _, err := s.be.SetLocalName(name); err != nil {
			return fail(err)
		}
		out = append(out, ok("local node is now "+name))
		if up > 0 {
			out = append(out, warnLine(fmt.Sprintf("warning: %d active link(s) will reconnect as %s", up, name)))
		}
		if passive > 0 {
			out = append(out, warnLine(fmt.Sprintf("warning: %d passive link(s) must be reconnected by the peer", passive)))
		}
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.SetLocal("local_node", name) }))
		}
	case "ip":
		if strings.ContainsAny(args[1], " ,#") {
			return fail(fmt.Errorf("bad address %q", args[1]))
		}
		if err := s.be.SetListen(args[1], li.Port); err != nil {
			return fail(err)
		}
		out = append(out, ok("listening on "+s.be.Local().Listen))
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.SetLocal("local_IP", args[1]) }))
		}
	case "timeout":
		sec, err := config.Timeout(args[1])
		if err != nil {
			return fail(fmt.Errorf("timeout: %v", err))
		}
		if err := s.be.SetTimeout(sec); err != nil {
			return fail(err)
		}
		out = append(out, ok(fmt.Sprintf("timeout %d s (0 = retry forever)", sec)))
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.SetLocal("timeout", strconv.Itoa(sec)) }))
		}
	case "port":
		p, err := config.Port(args[1])
		if err != nil {
			return fail(err)
		}
		if err := s.be.SetListen(li.IP, p); err != nil {
			return fail(err)
		}
		out = append(out, ok("listening on "+s.be.Local().Listen))
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.SetLocal("local_port", strconv.Itoa(p)) }))
		}
	default:
		return usage("local [name <node>|ip <addr>|port <n>|timeout <s>] [save]")
	}
	return Result{Lines: out}
}

// applyLinkOpts applies "addr port=.. keepalive=.. password=..
// strict_bcb=.." words to rc, validating each like the config loader.
// A bare word is the address ("-" = passive).
func applyLinkOpts(rc *config.Remote, words []string) error {
	for _, w := range words {
		k, v, isKV := strings.Cut(w, "=")
		if !isKV {
			k, v = "addr", w
		}
		var err error
		switch strings.ToLower(k) {
		case "addr":
			if v == "-" {
				v = ""
			}
			rc.Addr = v
		case "port":
			rc.Port, err = config.Port(v)
		case "keepalive":
			rc.Keepalive, err = config.Keepalive(v)
		case "password":
			rc.Password, err = config.Password(v)
		case "strict_bcb":
			rc.StrictBCB, err = config.Bool(v)
		case "natin":
			rc.NatIn, err = optIPv4(v)
		case "natout":
			rc.NatOut, err = optIPv4(v)
		case "line":
			rc.Line, err = config.LineNumber(v)
			rc.LineSet = err == nil
		case "buffersize":
			rc.BufSize, err = config.BufSize(v)
		case "max_streams", "max-streams":
			rc.MaxStreams, err = config.MaxStreams(v)
		case "turn":
			if v = strings.ToLower(v); v != "free" && v != "strict" {
				err = fmt.Errorf("must be free or strict")
			}
			rc.Turn = v
		default:
			err = fmt.Errorf("unknown option %q", k)
		}
		if err != nil {
			return fmt.Errorf("%s: %v", k, err)
		}
	}
	return nil
}

func (s *Shell) linkCmd(args []string) Result {
	args, save := stripSave(args)
	if len(args) == 0 {
		return usage("link define|change|delete|list ...")
	}
	sub := strings.ToLower(args[0])
	switch sub {
	case "list":
		return Result{Lines: s.statusLines()}
	case "define":
		if len(args) < 3 {
			return usage("link define <node> <addr|-> [port=<n>] [keepalive=<s>] [password=<pw>] [strict_bcb=yes|no] [turn=free|strict] [max_streams=<n>] [buffersize=<n>] [natin=<ip>] [natout=<ip>] [line=<n>] [save]")
		}
		rc := config.Remote{Name: args[1], Port: 175, Keepalive: 10}
		if err := applyLinkOpts(&rc, args[2:]); err != nil {
			return fail(err)
		}
		if err := s.be.DefineLink(rc); err != nil {
			return fail(err)
		}
		rc, _ = s.be.RemoteConfig(rc.Name)
		out := []Line{ok("link " + rc.Name + " defined")}
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.SetRemote(&rc) }))
		}
		return Result{Lines: out}
	case "change":
		if len(args) < 3 {
			return usage("link change <node> [addr|-] [port=<n>] [keepalive=<s>] [password=<pw>] [strict_bcb=yes|no] [turn=free|strict] [max_streams=<n>] [buffersize=<n>] [natin=<ip>] [natout=<ip>] [line=<n>] [save]")
		}
		rc, err := s.be.RemoteConfig(args[1])
		if err != nil {
			return fail(err)
		}
		if err := applyLinkOpts(&rc, args[2:]); err != nil {
			return fail(err)
		}
		if err := s.be.ChangeLink(rc); err != nil {
			return fail(err)
		}
		rc, _ = s.be.RemoteConfig(rc.Name)
		out := []Line{ok("link " + rc.Name + " changed (used on the next connect)")}
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.SetRemote(&rc) }))
		}
		return Result{Lines: out}
	case "delete":
		if len(args) != 2 {
			return usage("link delete <node> [save]")
		}
		name := strings.ToUpper(args[1])
		lost, err := s.be.DeleteLink(name)
		if err != nil {
			return fail(err)
		}
		out := []Line{ok("link " + name + " deleted")}
		if lost > 0 {
			out = append(out, warnLine(fmt.Sprintf("warning: %d queued file(s) for %s discarded", lost, name)))
		}
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.DeleteRemote(name) }))
		}
		return Result{Lines: out}
	}
	return usage("link define|change|delete|list ...")
}

// routeCmd: route <dest> to <via> [alt <alt>] [save] | route delete
// <dest> [save] | route default <node>|delete [save] | route list.
// Words are case-insensitive ("ROUTE MVSF TO MVSB ALT MVSC").
func (s *Shell) routeCmd(args []string) Result {
	args, save := stripSave(args)
	const use = "route <dest> to <via> [alt <alt>] [save] | route delete <dest> [save] | route default <node>|delete [save] | route list"
	if len(args) == 0 {
		return usage(use)
	}
	w := func(i int) string { return strings.ToLower(args[i]) }
	var out []Line
	switch {
	case w(0) == "list" && len(args) == 1:
		return Result{Lines: s.routeLines()}
	case w(0) == "delete" && len(args) == 2:
		dest := strings.ToUpper(args[1])
		if err := s.be.DeleteRoute(dest); err != nil {
			return fail(err)
		}
		out = append(out, ok("route to "+dest+" deleted"))
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.DeleteRoute(dest) }))
		}
	case w(0) == "default" && len(args) == 2:
		name := strings.ToUpper(args[1])
		if w(1) == "delete" {
			name = ""
		}
		if err := s.be.SetDefaultRoute(name); err != nil {
			return fail(err)
		}
		if name == "" {
			out = append(out, ok("default route removed"))
		} else {
			out = append(out, ok("default route via "+name))
		}
		if save {
			out = append(out, s.saveConfig(func(f *config.File) {
				if name == "" {
					f.DeleteLocal("default_route")
				} else {
					f.SetLocal("default_route", name)
				}
			}))
		}
	case (len(args) == 3 || len(args) == 5) && w(1) == "to" && (len(args) == 3 || w(3) == "alt"):
		rt := config.Route{Dest: args[0], Via: args[2]}
		if len(args) == 5 {
			rt.Alt = args[4]
		}
		var err error
		if rt, err = config.ParseRoute(rt.String()); err != nil {
			return fail(err)
		}
		if err := s.be.SetRoute(rt); err != nil {
			return fail(err)
		}
		text := "route to " + rt.Dest + " via " + rt.Via
		if rt.Alt != "" {
			text += " (alt " + rt.Alt + ")"
		}
		out = append(out, ok(text))
		if save {
			out = append(out, s.saveConfig(func(f *config.File) { f.SetRoute(rt) }))
		}
	default:
		return usage(use)
	}
	return Result{Lines: out}
}

// routeLines is the "routes" / "route list" table: one row for every
// defined remote (direct) and every route destination, with the state
// of the link actually used, plus a "* default" row.
func (s *Shell) routeLines() []Line {
	state := map[string]node.State{}
	var order []string
	for _, st := range s.be.Status() {
		state[st.Name] = st.State
		order = append(order, st.Name)
	}
	lineOf := map[string]string{}
	for _, st := range s.be.Status() {
		lineOf[st.Name] = strconv.Itoa(st.Line)
	}
	cols := []*statsColumn{{head: "LINE"}, {head: "NODE"}, {head: "PATH"}, {head: "ALT", prio: 2}, {head: "STATE", prio: 1}}
	var states []node.State
	// LINE is the line of the link the row's traffic uses.
	addRow := func(name, path, alt string, st node.State, via string) {
		for i, c := range []string{lineOf[via], name, path, alt, st.String()} {
			cols[i].cells = append(cols[i].cells, c)
		}
		states = append(states, st)
	}
	for _, n := range order {
		addRow(n, "direct", "", state[n], n)
	}
	var def *node.RouteStatus
	for _, r := range s.be.Routes() {
		if r.Default {
			rr := r
			def = &rr
			continue
		}
		used := r.Using
		if used == "" {
			used = r.Via // none up: show the via link's state
		}
		addRow(r.Dest, "via "+r.Via, r.Alt, state[used], used)
	}
	if def != nil {
		addRow("*", "default via "+def.Via, "", state[def.Via], def.Via)
	}
	if len(states) == 0 {
		return []Line{{Text: "no links or routes", Color: Dim}}
	}
	return renderTable(cols, s.width, func(row int, col *statsColumn) Color {
		switch col.head {
		case "NODE":
			if col.cells[row] == "*" {
				return BoldYellow
			}
			return BoldCyan
		case "STATE":
			return stateColor(states[row])
		}
		return Default
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// nodeTaking lists the commands whose node arguments may be given as a
// LINE number, and how many leading arguments are node positions
// (-1 = every word; msg/cmd/send only the first, the rest is free text).
var nodeTaking = map[string]int{
	"connect": -1, "drop": -1, "drain": -1, "stats": -1, "link": -1, "route": -1,
	"msg": 1, "m": 1, "cmd": 1, "c": 1, "send": 1,
}

// lineRefs rewrites LINE references in node positions to node names:
// "line <n>" (two words), "l<n>" and a bare "<n>" when no node of that
// name exists. msg/cmd/send keep their free text untouched.
func (s *Shell) lineRefs(line string) string {
	w := strings.Fields(line)
	if len(w) < 2 {
		return line
	}
	n, ok := nodeTaking[strings.ToLower(w[0])]
	if !ok {
		return line
	}
	byLine := map[string]string{}
	names := map[string]bool{}
	for _, st := range s.be.Status() {
		byLine[strconv.Itoa(st.Line)] = st.Name
		names[st.Name] = true
	}
	resolve := func(word string) (string, bool) {
		u := strings.ToUpper(word)
		if names[u] {
			return word, false
		}
		num := strings.TrimPrefix(u, "L")
		if name, ok := byLine[num]; ok && num != "" && strings.Trim(num, "0123456789") == "" {
			return name, true
		}
		return word, false
	}
	if n == 1 {
		// Only the first argument (the target); keep the text as typed.
		if strings.EqualFold(w[1], "line") && len(w) > 2 {
			if name, ok := byLine[w[2]]; ok {
				return w[0] + " " + name + rest(line, 3)
			}
			return line
		}
		if name, ok := resolve(w[1]); ok {
			return w[0] + " " + name + rest(line, 2)
		}
		return line
	}
	// Every word is a candidate, except sub-command keywords and option
	// values (they contain '=' or are words like "to"/"alt").
	var out []string
	out = append(out, w[0])
	for i := 1; i < len(w); i++ {
		if strings.EqualFold(w[i], "line") && i+1 < len(w) {
			if name, ok := byLine[w[i+1]]; ok {
				out = append(out, name)
				i++
				continue
			}
		}
		word := w[i]
		if !strings.Contains(word, "=") {
			if name, ok := resolve(word); ok && !(i == 1 && (w[0] == "stats" || w[0] == "link" || w[0] == "route") && isSubcommand(word)) {
				word = name
			}
		}
		out = append(out, word)
	}
	return strings.Join(out, " ")
}

// rest returns line from its n-th word on, with the leading blank, or "".
func rest(line string, n int) string {
	r := restAfter(line, n)
	if r == "" {
		return ""
	}
	return " " + r
}

func isSubcommand(w string) bool {
	switch strings.ToLower(w) {
	case "reset", "define", "change", "delete", "list", "default", "to", "alt":
		return true
	}
	return false
}

// optIPv4 validates a NAT address; an empty value clears it.
func optIPv4(v string) (string, error) {
	if v == "" || v == "-" {
		return "", nil
	}
	return config.IPv4(v)
}

// markFail marks the echoed line ✗ and returns the error result.
func (s *Shell) markFail(m *sendMark, err error) Result {
	if m != nil {
		m.state = markFail
	}
	return fail(err)
}

// sentResult is the result of a send that was accepted: no line when the
// echoed command line carries the status mark (it is logged), else (Exec
// called from code, no echo) the classic confirmation line.
func (s *Shell) sentResult(m *sendMark, logText string, classic Line) Result {
	if m == nil {
		return Result{Lines: []Line{classic}}
	}
	s.set.Log.Info("shell: " + logText)
	return Result{}
}
