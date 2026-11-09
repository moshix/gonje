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
)

// spoolEntry is one received file in recv_dir with its metadata (from
// the .meta sidecar, or guessed from the file name for older files).
type spoolEntry struct {
	name  string
	path  string
	size  int64
	when  time.Time
	meta  map[string]string
	order []string // sidecar key order, for the detail view
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

// spoolEntries lists recv_dir, newest first. Files without a sidecar get
// what their name tells: <FROMNODE>.<JOBNAME>.<DSNAME>.<yyyymmddhhmmss>.
func spoolEntries(dir string) ([]spoolEntry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []spoolEntry
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasSuffix(n, ".meta") || strings.HasSuffix(n, ".part") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		se := spoolEntry{name: n, path: filepath.Join(dir, n), size: info.Size(), when: info.ModTime()}
		se.meta, se.order = readMeta(se.path + ".meta")
		if se.meta == nil {
			se.meta = map[string]string{}
			p := strings.Split(n, ".")
			if len(p) >= 4 {
				se.meta["from_node"], se.meta["jobname"], se.meta["dsname"] = p[0], p[1], p[2]
			}
			if strings.HasSuffix(n, ".txt") {
				se.meta["kind"] = "text"
			} else if strings.HasSuffix(n, ".bin") {
				se.meta["kind"] = "binary"
			}
		}
		if t, err := time.Parse(time.RFC3339, se.meta["received"]); err == nil {
			se.when = t
		}
		out = append(out, se)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].when.After(out[j].when) })
	return out, nil
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
	case len(args) == 2 && args[0] == "delete":
		e, err := pick(list, args[1])
		if err != nil {
			return fail(err)
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

// pick finds an entry by its number in the listing or its exact file
// name. Only names that are in the listing match, so "../x" or a path
// can never reach a file outside recv_dir.
func pick(list []spoolEntry, arg string) (spoolEntry, error) {
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(list) {
			return spoolEntry{}, fmt.Errorf("no spool file #%d (%d files)", n, len(list))
		}
		return list[n-1], nil
	}
	for _, e := range list {
		if e.name == arg {
			return e, nil
		}
	}
	return spoolEntry{}, fmt.Errorf("no spool file %q", arg)
}

// spoolTable is the listing, newest first, dropping low-priority
// columns on narrow screens.
func (s *Shell) spoolTable(list []spoolEntry, width int) []Line {
	if len(list) == 0 {
		return []Line{{Text: s.set.RecvDir + ": no files", Color: Dim}}
	}
	cols := []*statsColumn{
		{head: "#"}, {head: "RECEIVED", prio: 4}, {head: "FROM"}, {head: "TO", prio: 5},
		{head: "JOBNAME", prio: 3}, {head: "DSNAME", prio: 2}, {head: "KIND", prio: 1},
		{head: "RECS", prio: 6}, {head: "SIZE", prio: 1}, {head: "FORMAT", prio: 7},
	}
	for i, e := range list {
		m := e.meta
		from := m["from_node"]
		if m["from_user"] != "" {
			from += "/" + m["from_user"]
		}
		format := ""
		if m["recfm"] != "" {
			format = m["recfm"] + " " + m["lrecl"] + " " + m["cc"]
		}
		cells := []string{strconv.Itoa(i + 1), e.when.Format("01-02 15:04:05"), from, m["to_user"],
			m["jobname"], m["dsname"], m["kind"], m["records"], fmtBytes(e.size), format}
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
			if col.cells[row] == "text" {
				return Green
			}
			return Magenta
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
