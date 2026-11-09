// Copyright 2026 by moshix

package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// File is a config file opened for editing. Edits work on the original
// text, token by token, so every other line, comment and the layout of
// untouched keys are kept exactly as the operator wrote them.
type File struct {
	Path  string
	lines []string
	nl    bool // the file ended with a newline
	mode  os.FileMode
}

// ftok is one key=value token with its position in the file.
type ftok struct {
	line       int    // line index
	start, end int    // byte offsets of the whole token in the line
	key        string // lower-cased key
	val        string
}

// OpenFile reads a config file for editing.
func OpenFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	text := string(b)
	f := &File{Path: path, mode: st.Mode().Perm(), nl: strings.HasSuffix(text, "\n")}
	f.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		f.lines = nil
	}
	return f, nil
}

// Bytes returns the edited file content.
func (f *File) Bytes() []byte {
	s := strings.Join(f.lines, "\n")
	if f.nl || len(f.lines) > 0 {
		s += "\n"
	}
	return []byte(s)
}

// isSep reports the token separators of the config format.
func isSep(c byte) bool { return c == ' ' || c == '\t' || c == ',' || c == '\r' }

// scanLine finds the key=value tokens of one line, stopping at a token
// that starts with '#' (comment), exactly like the loader's tokenizer.
func scanLine(n int, line string) []ftok {
	var out []ftok
	for i := 0; i < len(line); {
		if isSep(line[i]) {
			i++
			continue
		}
		if line[i] == '#' {
			break
		}
		j := i
		for j < len(line) && !isSep(line[j]) {
			j++
		}
		k, v, _ := strings.Cut(line[i:j], "=")
		out = append(out, ftok{line: n, start: i, end: j, key: normKey(k), val: v})
		i = j
	}
	return out
}

func (f *File) toks() []ftok {
	var out []ftok
	for n, l := range f.lines {
		out = append(out, scanLine(n, l)...)
	}
	return out
}

// block returns the token index range [from, to) of the remote named
// name: its remote_host_name token up to the next one. ok=false if the
// remote is not in the file.
func block(toks []ftok, name string) (from, to int, ok bool) {
	from = -1
	for i, t := range toks {
		if t.key != "remote_host_name" {
			continue
		}
		if from >= 0 {
			return from, i, true
		}
		if strings.EqualFold(t.val, name) {
			from = i
		}
	}
	if from < 0 {
		return 0, 0, false
	}
	return from, len(toks), true
}

// setValue replaces the value of token t, keeping its key spelling.
func (f *File) setValue(t ftok, val string) {
	line := f.lines[t.line]
	keyText, _, _ := strings.Cut(line[t.start:t.end], "=")
	f.lines[t.line] = line[:t.start] + keyText + "=" + val + line[t.end:]
}

// removeToken deletes token t. If the line then holds no tokens at all
// it is removed entirely (including a trailing comment about the key).
func (f *File) removeToken(t ftok) {
	line := f.lines[t.line]
	end := t.end
	for end < len(line) && isSep(line[end]) && line[end] != '\r' {
		end++
	}
	rest := line[:t.start] + line[end:]
	if len(scanLine(t.line, rest)) == 0 {
		f.lines = append(f.lines[:t.line], f.lines[t.line+1:]...)
		return
	}
	f.lines[t.line] = strings.TrimRight(rest, " \t,")
}

// addAfter inserts key=val after token t: on a new line with the same
// indentation, unless more tokens follow t on its line (another block
// starts there), in which case it is spliced in right after t.
func (f *File) addAfter(t ftok, key, val string) {
	line := f.lines[t.line]
	item := key + "=" + val
	if len(scanLine(t.line, line[t.end:])) > 0 {
		f.lines[t.line] = line[:t.end] + " " + item + line[t.end:]
		return
	}
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	f.insertLine(t.line+1, indent+item)
}

func (f *File) insertLine(at int, text string) {
	f.lines = append(f.lines, "")
	copy(f.lines[at+1:], f.lines[at:])
	f.lines[at] = text
}

// SetLocal sets a top-level key (local_node, local_IP, local_port...).
// Existing tokens are updated in place; otherwise a new line is added
// after the last top-level setting before the first remote block.
func (f *File) SetLocal(key, val string) {
	lk := normKey(key)
	toks := f.toks()
	found := false
	for _, t := range toks {
		if t.key == lk {
			f.setValue(t, val)
			found = true
		}
	}
	if found {
		return
	}
	var last *ftok
	for i := range toks {
		if toks[i].key == "remote_host_name" {
			if last == nil {
				// Only remote blocks: put the setting above the first one.
				f.insertLine(toks[i].line, key+"="+val)
				return
			}
			break
		}
		last = &toks[i]
	}
	if last == nil {
		f.lines = append(f.lines, key+"="+val)
		return
	}
	f.addAfter(*last, key, val)
}

// remoteKeys lists the keys gonje writes for a remote, in file order,
// with the value to write ("" = remove the key).
func remoteKeys(r *Remote, toks []ftok) [][2]string {
	strict := ""
	if r.StrictBCB {
		strict = "yes"
	} else {
		for _, t := range toks {
			if t.key == "remote_strict_bcb" {
				strict = "no" // keep an explicit "no" that is already there
			}
		}
	}
	turn := ""
	if r.Turn == "strict" {
		turn = "strict"
	} else {
		for _, t := range toks {
			if t.key == "remote_turn" {
				turn = "free"
			}
		}
	}
	bufSize := ""
	if r.BufSize > 0 {
		bufSize = strconv.Itoa(r.BufSize)
	}
	maxStreams := ""
	if r.MaxStreams > 0 {
		maxStreams = strconv.Itoa(r.MaxStreams)
	}
	return [][2]string{
		{"remote_host_addr", r.Addr},
		{"remote_host_port", strconv.Itoa(r.Port)},
		{"remote_keepalive", strconv.Itoa(r.Keepalive)},
		{"remote_password", r.Password},
		{"remote_strict_bcb", strict},
		{"remote_turn", turn},
		{"remote_max_streams", maxStreams},
		{"remote_buffersize", bufSize},
	}
}

// SetRemote adds the remote's block at the end of the file or, if a
// block for that name exists, updates only its gonje keys (other keys,
// comments and layout stay as they are).
func (f *File) SetRemote(r *Remote) {
	toks := f.toks()
	from, to, ok := block(toks, r.Name)
	if !ok {
		if n := len(f.lines); n > 0 && strings.TrimSpace(f.lines[n-1]) != "" {
			f.lines = append(f.lines, "")
		}
		f.lines = append(f.lines, "remote_host_name="+r.Name)
		for _, kv := range remoteKeys(r, nil) {
			if kv[1] != "" {
				f.lines = append(f.lines, kv[0]+"="+kv[1])
			}
		}
		return
	}
	if toks[from].val != r.Name {
		f.setValue(toks[from], r.Name)
	}
	for _, kv := range remoteKeys(r, toks[from:to]) {
		// Re-scan after every edit: offsets and line numbers move.
		toks = f.toks()
		from, to, _ = block(toks, r.Name)
		var have []ftok
		for _, t := range toks[from:to] {
			if t.key == kv[0] {
				have = append(have, t)
			}
		}
		switch {
		case kv[1] == "":
			// Remove from the end so earlier offsets stay valid.
			for i := len(have) - 1; i >= 0; i-- {
				f.removeToken(have[i])
			}
		case len(have) > 0:
			for _, t := range have {
				f.setValue(t, kv[1])
			}
		default:
			f.addAfter(toks[to-1], kv[0], kv[1])
		}
	}
}

// DeleteRemote removes the remote's block: its tokens, the lines that
// held only those tokens, and comment lines inside the block. It
// reports whether the block existed.
func (f *File) DeleteRemote(name string) bool {
	toks := f.toks()
	from, to, ok := block(toks, name)
	if !ok {
		return false
	}
	first, last := toks[from].line, toks[to-1].line
	inBlock := map[int][]ftok{}
	for _, t := range toks[from:to] {
		inBlock[t.line] = append(inBlock[t.line], t)
	}
	// Lines that also hold tokens outside the block keep those tokens.
	others := map[int]bool{}
	for i, t := range toks {
		if (i < from || i >= to) && t.line >= first && t.line <= last {
			others[t.line] = true
		}
	}
	var out []string
	for n, l := range f.lines {
		switch {
		case n < first || n > last:
			out = append(out, l)
		case others[n]:
			// Shared line (e.g. the next block starts here): drop only
			// this block's tokens, right to left.
			bt := inBlock[n]
			for i := len(bt) - 1; i >= 0; i-- {
				l = l[:bt[i].start] + strings.TrimLeft(l[bt[i].end:], " \t,")
			}
			out = append(out, l)
		}
		// Otherwise the line belonged to the block only (or was a
		// comment inside it): drop it.
	}
	f.lines = out
	return true
}

// routeLine reports whether line n is a route=<dest>,... line and its
// destination. A route line holds only the route (plus a comment).
func (f *File) routeLine(n int) (string, bool) {
	t := scanLine(n, f.lines[n])
	if len(t) == 0 || t[0].key != "route" {
		return "", false
	}
	dest, _, _ := strings.Cut(t[0].val, ",")
	return strings.ToUpper(dest), true
}

// commentOf returns the " # ..." part of a line, if any.
func commentOf(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || isSep(line[i-1])) {
			j := i // keep the blanks before the comment as they were
			for j > 0 && (line[j-1] == ' ' || line[j-1] == '\t') {
				j--
			}
			if j == 0 {
				return " " + line[i:]
			}
			return line[j:]
		}
	}
	return ""
}

// SetRoute adds or replaces the route=<dest>,<via>[,<alt>] line for
// r.Dest. A new line goes after the last route line, else after the
// last top-level setting before the first remote block.
func (f *File) SetRoute(r Route) {
	text := "route=" + r.String()
	last := -1
	for n := range f.lines {
		if dest, ok := f.routeLine(n); ok {
			if dest == r.Dest {
				f.lines[n] = text + commentOf(f.lines[n])
				return
			}
			last = n
		}
	}
	if last >= 0 {
		f.insertLine(last+1, text)
		return
	}
	// Like SetLocal: after the top-level settings.
	toks := f.toks()
	at := len(f.lines)
	for i, t := range toks {
		if t.key == "remote_host_name" {
			at = t.line
			if i > 0 {
				at = toks[i-1].line + 1
			}
			break
		}
	}
	f.insertLine(at, text)
}

// DeleteRoute removes the route line for dest; it reports whether one
// existed.
func (f *File) DeleteRoute(dest string) bool {
	for n := range f.lines {
		if d, ok := f.routeLine(n); ok && d == strings.ToUpper(dest) {
			f.lines = append(f.lines[:n], f.lines[n+1:]...)
			return true
		}
	}
	return false
}

// DeleteLocal removes a top-level key (e.g. default_route).
func (f *File) DeleteLocal(key string) {
	lk := normKey(key)
	toks := f.toks()
	for i := len(toks) - 1; i >= 0; i-- {
		if toks[i].key == lk {
			f.removeToken(toks[i])
		}
	}
}

// Save checks that the edited text still loads, then writes it
// atomically: a temporary file in the same directory, renamed over the
// original. The file mode is kept, except that a file holding a
// password is made 0600.
func (f *File) Save() error {
	data := f.Bytes()
	cfg, err := Parse(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("edited config would not load: %w", err)
	}
	mode := f.mode
	for _, r := range cfg.Remotes {
		if r.Password != "" {
			mode = 0o600
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), "."+filepath.Base(f.Path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		return err
	}
	f.mode = mode
	return nil
}
