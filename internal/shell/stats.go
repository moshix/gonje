// Copyright 2026 by moshix

package shell

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gonje/internal/node"
)

// fmtBytes renders a byte count for people: 512 B, 1.2 KB, 3.4 MB.
func fmtBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.1f KB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
}

// fmtDur renders a duration compactly: 42s, 3m12s, 2h05m, 3d04h.
func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	s := int(d / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd%02dh", s/86400, s%86400/3600)
}

// io formats an in/out pair.
func io2(in, out int) string { return fmt.Sprintf("%d/%d", in, out) }

func (s *Shell) statsCmd(args []string) Result {
	if len(args) > 0 && strings.EqualFold(args[0], "reset") {
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		if err := s.be.ResetCounters(name); err != nil {
			return fail(err)
		}
		what := "all counters"
		if name != "" {
			what = "counters of " + strings.ToUpper(name)
		}
		return Result{Lines: []Line{ok(what + " reset")}}
	}
	c := s.be.Counters()
	if len(args) == 1 {
		for _, l := range c.Links {
			if strings.EqualFold(l.Name, args[0]) {
				return Result{Lines: s.linkStats(l)}
			}
		}
		return Result{Lines: []Line{errText("no remote node " + strings.ToUpper(args[0]))}}
	}
	if len(args) > 1 {
		return usage("stats [<node> | reset [<node>]]")
	}
	return Result{Lines: append(s.generalStats(c), s.statsTable(c, s.width)...)}
}

// kv is one "key  value" row of a stats listing.
func kv(key string, val string, c Color) Line {
	l := SegLine(Seg{fmt.Sprintf("  %-20s", key), Dim}, Seg{val, c})
	l.Cont = strings.Repeat(" ", 22)
	return l
}

func (s *Shell) generalStats(c node.Counters) []Line {
	li := s.be.Local()
	var t struct {
		msgIn, msgOut, cmdIn, cmdOut, rspOut, filesIn, filesOut, recIn, recOut int
		fbIn, fbOut, bIn, bOut                                                 int64
		errs, connects, fails                                                  int
	}
	for _, l := range c.Links {
		x := l.Total
		t.msgIn, t.msgOut, t.cmdIn, t.cmdOut = t.msgIn+x.MsgsIn, t.msgOut+x.MsgsOut, t.cmdIn+x.CmdsIn, t.cmdOut+x.CmdsOut
		t.rspOut += l.RspsOut
		t.filesIn, t.filesOut, t.recIn, t.recOut = t.filesIn+x.FilesIn, t.filesOut+x.FilesOut, t.recIn+x.RecordsIn, t.recOut+x.RecordsOut
		t.fbIn, t.fbOut, t.bIn, t.bOut = t.fbIn+x.FileBytesIn, t.fbOut+x.FileBytesOut, t.bIn+x.BytesIn, t.bOut+x.BytesOut
		t.errs += x.BCBErrors + x.NAKsIn + l.Failures
		t.connects, t.fails = t.connects+l.Connects, t.fails+l.Failures
	}
	listen := li.Listen
	if listen == "" {
		listen = "(not listening)"
	}
	errColor := Default
	if t.errs > 0 {
		errColor = Red
	}
	return []Line{
		SegLine(Seg{"gonje " + s.set.Version, BoldCyan}, Seg{"  up " + fmtDur(time.Since(c.Start)), Default},
			Seg{"  counters since " + c.Since.Format("2006-01-02 15:04:05"), Dim}),
		kv("local node", li.Name+" on "+listen, BoldYellow),
		kv("links", fmt.Sprintf("%d defined, %d up", li.Links, li.Up), Default),
		kv("messages in/out", io2(t.msgIn, t.msgOut), Cyan),
		kv("commands in/out", io2(t.cmdIn, t.cmdOut), Magenta),
		kv("responses sent", fmt.Sprint(t.rspOut), Magenta),
		kv("files in/out", fmt.Sprintf("%s (%s / %s)", io2(t.filesIn, t.filesOut), fmtBytes(t.fbIn), fmtBytes(t.fbOut)), Green),
		kv("records in/out", io2(t.recIn, t.recOut), Default),
		kv("bytes in/out", fmtBytes(t.bIn)+" / "+fmtBytes(t.bOut), Default),
		kv("errors", fmt.Sprint(t.errs), errColor),
		kv("connects", fmt.Sprintf("%d (%d failed)", t.connects, t.fails), Default),
		kv("inbound", fmt.Sprintf("%d accepted, %d rejected (NAK sent)", c.Accepted, c.Rejected), Default),
	}
}

// statsColumn is one column of the per-link table. prio decides which
// columns go first on a narrow screen (higher = dropped earlier).
type statsColumn struct {
	head  string
	prio  int
	cells []string
}

// statsTable renders the per-link table for a pane of width columns
// (0 = no limit), dropping low-priority columns until it fits.
func (s *Shell) statsTable(c node.Counters, width int) []Line {
	cols := []*statsColumn{
		{head: "LINE"}, {head: "NODE"}, {head: "STATE"}, {head: "SESSION", prio: 2}, {head: "MSG i/o", prio: 3},
		{head: "CMD i/o", prio: 5}, {head: "FILES i/o", prio: 4}, {head: "BYTES in/out", prio: 3},
		{head: "NULL i/o", prio: 7}, {head: "CONN", prio: 6}, {head: "ERR", prio: 6}, {head: "LAST", prio: 8},
	}
	for _, l := range c.Links {
		x := l.Total
		session := "-"
		if !l.SessionSince.IsZero() {
			session = fmtDur(time.Since(l.SessionSince))
		}
		last := "-"
		if !x.LastActivity.IsZero() {
			last = x.LastActivity.Format("15:04:05")
		}
		cells := []string{strconv.Itoa(l.Line), l.Name, l.State.String(), session, io2(x.MsgsIn, x.MsgsOut), io2(x.CmdsIn, x.CmdsOut),
			io2(x.FilesIn, x.FilesOut), fmtBytes(x.BytesIn) + "/" + fmtBytes(x.BytesOut),
			io2(x.KeepaliveIn, x.KeepaliveOut), fmt.Sprint(l.Connects), fmt.Sprint(x.BCBErrors + x.NAKsIn + l.Failures), last}
		for i, col := range cols {
			col.cells = append(col.cells, cells[i])
		}
	}
	return renderTable(cols, width, func(li int, col *statsColumn) Color {
		switch col.head {
		case "NODE":
			return BoldCyan
		case "STATE":
			return stateColor(c.Links[li].State)
		case "ERR":
			if col.cells[li] != "0" {
				return Red
			}
		}
		return Default
	})
}

// renderTable lays out columns (cells per row) for a pane of width
// columns (0 = no limit): header in bold, columns padded to a common
// width, and low-priority columns dropped (highest prio number, rightmost
// first) until the table fits; prio 0 columns always stay.
func renderTable(cols []*statsColumn, width int, colorFor func(row int, col *statsColumn) Color) []Line {
	colWidth := func(col *statsColumn) int {
		w := Width(col.head)
		for _, c := range col.cells {
			w = max(w, Width(c))
		}
		return w
	}
	total := func(cs []*statsColumn) int {
		t := 2
		for i, col := range cs {
			if i > 0 {
				t += 2
			}
			t += colWidth(col)
		}
		return t
	}
	for width > 0 && total(cols) > width {
		drop := -1
		for i, col := range cols {
			if col.prio > 0 && (drop < 0 || col.prio >= cols[drop].prio) {
				drop = i
			}
		}
		if drop < 0 {
			break
		}
		cols = append(cols[:drop], cols[drop+1:]...)
	}
	row := func(get func(*statsColumn) string, color func(col *statsColumn) Color) Line {
		segs := []Seg{{"  ", Default}}
		for i, col := range cols {
			text := get(col)
			if i < len(cols)-1 {
				text += strings.Repeat(" ", colWidth(col)-Width(text)+2)
			}
			segs = append(segs, Seg{text, color(col)})
		}
		l := SegLine(segs...)
		l.Clip = true // still too wide (no column left to drop): cut, never wrap
		return l
	}
	out := []Line{row(func(c *statsColumn) string { return c.head }, func(*statsColumn) Color { return Bold })}
	if len(cols) == 0 {
		return out
	}
	for r := range cols[0].cells {
		out = append(out, row(func(col *statsColumn) string { return col.cells[r] },
			func(col *statsColumn) Color { return colorFor(r, col) }))
	}
	return out
}

// linkStats lists every counter of one link.
func (s *Shell) linkStats(l node.LinkCounters) []Line {
	x := l.Total
	session, last := "-", "-"
	if !l.SessionSince.IsZero() {
		role := "secondary"
		if x.Primary {
			role = "primary"
		}
		session = fmt.Sprintf("%s (%s, peer buffer %d)", fmtDur(time.Since(l.SessionSince)), role, x.PeerBufSize)
	}
	if !x.LastActivity.IsZero() {
		last = fmt.Sprintf("%s (%s ago)", x.LastActivity.Format("15:04:05"), fmtDur(time.Since(x.LastActivity)))
	}
	addr := l.Addr
	if addr == "" {
		addr = "(passive)"
	}
	lastErr := l.LastErr
	if lastErr == "" {
		lastErr = "-"
	}
	state := l.State.String()
	if l.Held {
		state += " (held)"
	}
	return []Line{
		SegLine(Seg{fmt.Sprintf("LINE %d ", l.Line), Dim}, Seg{l.Name, BoldCyan}),
		kv("state", state, stateColor(l.State)),
		kv("address", addr, Default),
		kv("nat in/out", orDash(l.NatIn)+" / "+orDash(l.NatOut), Default),
		kv("session uptime", session, Default),
		kv("connected total", fmtDur(l.Connected), Default),
		kv("buffer size", bufText(l.BufSize, x.PeerBufSize), Default),
		kv("max streams", fmt.Sprintf("%d (active in/out %d/%d)", l.MaxStreams, x.ActiveIn, x.ActiveOut), Default),
		kv("connects/failures", io2(l.Connects, l.Failures), Default),
		kv("last error", lastErr, Dim),
		kv("blocks in/out", io2(x.BlocksIn, x.BlocksOut), Default),
		kv("bytes in/out", fmtBytes(x.BytesIn)+" / "+fmtBytes(x.BytesOut), Default),
		kv("null buffers in/out", io2(x.KeepaliveIn, x.KeepaliveOut), Dim),
		kv("messages in/out", io2(x.MsgsIn, x.MsgsOut), Cyan),
		kv("commands in/out", io2(x.CmdsIn, x.CmdsOut), Magenta),
		kv("responses in/out", io2(l.RspsIn, l.RspsOut), Magenta),
		kv("files in/out", fmt.Sprintf("%s (%s / %s)", io2(x.FilesIn, x.FilesOut), fmtBytes(x.FileBytesIn), fmtBytes(x.FileBytesOut)), Green),
		kv("records in/out", io2(x.RecordsIn, x.RecordsOut), Default),
		kv("BCB seq errors", fmt.Sprint(x.BCBErrors), Default),
		kv("NAKs received", fmt.Sprint(x.NAKsIn), Default),
		kv("last activity", last, Default),
		kv("idle reply delay", x.IdleDelay.String(), Default),
	}
}

// bufText shows our buffer size and, when signed on, the peer's and the
// size we actually send (the smaller one).
func bufText(ours, peer int) string {
	if peer == 0 {
		return fmt.Sprintf("%d", ours)
	}
	return fmt.Sprintf("%d (peer %d, sending up to %d)", ours, peer, min(ours, peer))
}
