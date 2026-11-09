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
	"time"

	"gonje/internal/node"
)

// spoolEntry is one received file in recv_dir with its metadata (from
// the .meta sidecar, or guessed from the file name for older files).
type spoolEntry struct {
	name    string
	path    string
	size    int64
	when    time.Time
	meta    map[string]string
	order   []string // sidecar key order, for the detail view
	guessed bool     // no sidecar data: fields from the file name
	id      int      // stable spool id (0 = none could be given)
	fwd     *node.ForwardEntry
}

// readMeta parses a key=value sidecar.
func readMeta(path string) (map[string]string, []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	m := map[string]string{}
	var order []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			m[k] = v
			order = append(order, k)
		}
	}
	return m, order
}

// spoolEntries lists recv_dir, newest first: received files plus the
// forward spool (files for other nodes, queued or held). Files without a
// sidecar get what their name tells: <FROMNODE>.<JOBNAME>.<DSNAME>.<ts>.
// Every entry has a stable spool id; files saved by an older gonje get
// one here (oldest first), appended to their sidecar.
func spoolEntries(dir string) ([]spoolEntry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []spoolEntry
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || strings.HasSuffix(n, ".meta") || strings.HasSuffix(n, ".part") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		se := spoolEntry{name: n, path: filepath.Join(dir, n), size: info.Size(), when: info.ModTime()}
		se.read()
		out = append(out, se)
	}
	// Give ids to old files, oldest first so the numbers follow age.
	sort.SliceStable(out, func(i, j int) bool { return out[i].when.Before(out[j].when) })
	for i := range out {
		if out[i].id == 0 {
			if _, err := node.AssignSpoolID(dir, out[i].path); err == nil {
				out[i].read()
			}
		}
	}
	fwds, err := node.ForwardEntries(dir)
	if err != nil {
		return nil, err
	}
	for _, f := range fwds {
		out = append(out, forwardEntry(f))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].when.After(out[j].when) })
	return out, nil
}

// read loads the entry's sidecar (or guesses from the file name).
func (se *spoolEntry) read() {
	se.meta, se.order = readMeta(se.path + ".meta")
	if se.meta == nil {
		se.meta = map[string]string{}
	}
	se.id, _ = strconv.Atoi(se.meta["id"])
	se.guessed = se.meta["received"] == "" && se.meta["from_node"] == ""
	if se.guessed {
		p := strings.Split(se.name, ".")
		if len(p) >= 4 {
			se.meta["from_node"], se.meta["jobname"], se.meta["dsname"] = p[0], p[1], p[2]
		}
		if strings.HasSuffix(se.name, ".txt") {
			se.meta["kind"] = "text"
		} else if strings.HasSuffix(se.name, ".bin") {
			se.meta["kind"] = "binary"
		}
	}
	if t, err := time.Parse(time.RFC3339, se.meta["received"]); err == nil {
		se.when = t
	}
}

// forwardEntry shows a forward spool file like a received one.
func forwardEntry(f *node.ForwardEntry) spoolEntry {
	var size int64
	for _, r := range f.File.Records {
		size += int64(len(r))
	}
	status := "forwarding via " + f.Via
	if f.State == node.FwdHeld {
		status = "held (forward failed: " + f.Reason + ")"
	}
	m := map[string]string{"id": strconv.Itoa(f.ID), "from_node": f.File.OrigNode, "from_user": f.File.FromUser,
		"to_node": f.Dest, "to_user": f.File.ToUser, "jobname": f.File.JobName, "dsname": f.File.DSName,
		"records": strconv.Itoa(len(f.File.Records)), "kind": "forward", "status": status,
		"arrived_on": f.Arrived, "stored": f.Stored.Format(time.RFC3339)}
	order := []string{"id", "status", "from_node", "from_user", "to_node", "to_user", "jobname", "dsname", "records", "arrived_on", "stored"}
	return spoolEntry{name: filepath.Base(f.Path), path: f.Path, size: size, when: f.Stored, meta: m, order: order, id: f.ID, fwd: f}
}

// spoolCmd: spool | spool <#|name> | spool delete <#|name>.
func (s *Shell) spoolCmd(args []string) Result {
	list, err := spoolEntries(s.set.RecvDir)
	if err != nil {
		return fail(err)
	}
	switch {
	case len(args) == 0:
		return Result{Lines: s.spoolTable(list, s.width)}
	case len(args) == 1:
		e, err := pick(list, args[0])
		if err != nil {
			return fail(err)
		}
		return Result{Lines: spoolDetail(e)}
	case len(args) == 2 && strings.EqualFold(args[0], "delete"):
		e, err := pick(list, args[1])
		if err != nil {
			return fail(err)
		}
		if e.fwd != nil && e.fwd.State != node.FwdHeld {
			return fail(fmt.Errorf("spool #%d is being forwarded via %s", e.id, e.fwd.Via))
		}
		if err := os.Remove(e.path); err != nil {
			return fail(err)
		}
		_ = os.Remove(e.path + ".meta")
		s.set.Log.Info("spool: deleted " + e.path)
		return Result{Lines: []Line{ok("deleted " + e.name)}}
	}
	return usage("spool [<#|file> | delete <#|file>]")
}

// pick finds an entry by its spool id or its exact file name. Ids are
// stable (a file arriving between "spool" and "spool delete 3" does not
// renumber anything). Only names that are in the listing match, so
// "../x" or a path can never reach a file outside recv_dir.
func pick(list []spoolEntry, arg string) (spoolEntry, error) {
	if n, err := strconv.Atoi(strings.TrimPrefix(arg, "#")); err == nil {
		for _, e := range list {
			if e.id == n && n > 0 {
				return e, nil
			}
		}
		return spoolEntry{}, fmt.Errorf("no spool file #%d", n)
	}
	for _, e := range list {
		if e.name == arg {
			return e, nil
		}
	}
	return spoolEntry{}, fmt.Errorf("no spool file %q", arg)
}

// spoolTable is the listing, newest first, dropping low-priority
// columns on narrow screens. STATUS (only shown when the forward spool
// has files) is last, so a long reason is cut with "…".
func (s *Shell) spoolTable(list []spoolEntry, width int) []Line {
	if len(list) == 0 {
		return []Line{{Text: s.set.RecvDir + ": no files", Color: Dim}}
	}
	cols := []*statsColumn{
		{head: "#"}, {head: "RECEIVED", prio: 4}, {head: "FROM"}, {head: "TO", prio: 5},
		{head: "JOBNAME", prio: 3}, {head: "DSNAME", prio: 2}, {head: "KIND", prio: 1},
		{head: "RECS", prio: 6}, {head: "SIZE", prio: 1}, {head: "FORMAT", prio: 7},
	}
	fwd := false
	for _, e := range list {
		fwd = fwd || e.fwd != nil
	}
	if fwd {
		cols = append(cols, &statsColumn{head: "STATUS"})
	}
	for _, e := range list {
		m := e.meta
		from := m["from_node"]
		if m["from_user"] != "" {
			from += "/" + m["from_user"]
		}
		format := ""
		if m["recfm"] != "" {
			format = m["recfm"] + " " + m["lrecl"] + " " + m["cc"]
		}
		to := m["to_user"]
		if e.fwd != nil {
			to = m["to_node"] + "/" + to
		}
		id := "-"
		if e.id > 0 {
			id = strconv.Itoa(e.id)
		}
		cells := []string{id, e.when.Format("01-02 15:04:05"), from, to,
			m["jobname"], m["dsname"], m["kind"], m["records"], fmtBytes(e.size), format, m["status"]}
		for c, col := range cols {
			col.cells = append(col.cells, cells[c])
		}
	}
	return renderTable(cols, width, func(row int, col *statsColumn) Color {
		switch col.head {
		case "#":
			return Dim
		case "FROM":
			return BoldCyan
		case "KIND":
			switch col.cells[row] {
			case "text":
				return Green
			case "forward":
				return Yellow
			}
			return Magenta
		case "STATUS":
			if strings.HasPrefix(col.cells[row], "held") {
				return Red
			}
			return Yellow
		case "SIZE":
			return Yellow
		}
		return Default
	})
}

// spoolDetail is the key/value view of one file.
func spoolDetail(e spoolEntry) []Line {
	out := []Line{SegLine(Seg{"file ", Default}, Seg{e.name, Green}), kv("path", e.path, Default),
		kv("size", fmt.Sprintf("%s (%d bytes)", fmtBytes(e.size), e.size), Yellow)}
	keys := e.order
	if e.guessed {
		keys = nil
	}
	if len(keys) == 0 {
		for k := range e.meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append(out, Line{Text: "  (no metadata sidecar: from the file name)", Color: Dim})
	}
	for _, k := range keys {
		out = append(out, kv(k, e.meta[k], Default))
	}
	return out
}
