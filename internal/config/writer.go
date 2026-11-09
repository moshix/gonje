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
	crlf  bool // the file uses CRLF line ends (kept for inserted lines)
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
	f.crlf = len(f.lines) > 0 && strings.HasSuffix(f.lines[0], "\r")
	if text == "" {
		f.lines = nil
	}
	return f, nil
}

// NewFile starts a config file that does not exist yet with text (Save
// creates it, mode 0644, or 0600 when it holds a password).
func NewFile(path, text string) *File {
	f := &File{Path: path, mode: 0o644, nl: true}
	if text != "" {
		f.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	return f
}

// Bytes returns the edited file content.
func (f *File) Bytes() []byte {
	lines := f.lines
	if f.crlf {
		// Lines added by the editors have no "\r": give them one so the
		// file does not end up with mixed line ends.
		lines = make([]string, len(f.lines))
		for i, l := range f.lines {
			if !strings.HasSuffix(l, "\r") {
				l += "\r"
			}
			lines[i] = l
		}
	}
	s := strings.Join(lines, "\n")
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
// name: its remote_host_name token up to (and including) the last
// remote_* token before the next remote_host_name. The loader accepts
// top-level keys anywhere, so route=, timeout= and the like that follow
// the last remote block are not part of it (deleting the remote must
// never take them along). Non-remote tokens inside the range are left
// alone by the editors. ok=false if the remote is not in the file.
func block(toks []ftok, name string) (from, to int, ok bool) {
	from = -1
	for i, t := range toks {
		if t.key == "remote_host_name" && strings.EqualFold(t.val, name) {
			from = i
			break
		}
	}
	if from < 0 {
		return 0, 0, false
	}
	to = from + 1
	for i := from + 1; i < len(toks) && toks[i].key != "remote_host_name"; i++ {
		if isRemoteKey(toks[i].key) {
			to = i + 1
		}
	}
	return from, to, true
}

// isRemoteKey reports whether a (normalized) key belongs to a remote
// block.
func isRemoteKey(key string) bool { return strings.HasPrefix(key, "remote_") }

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
				// Only remote blocks: put the setting above the first
				// one and above the comment lines directly before it
				// (they describe that remote).
				at := toks[i].line
				for at > 0 && strings.HasPrefix(strings.TrimSpace(f.lines[at-1]), "#") {
					at--
				}
				f.insertLine(at, key+"="+val)
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
	line := "" // only explicit line numbers are written
	if r.LineSet {
		line = strconv.Itoa(r.Line)
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
		{"remote_natin", r.NatIn},
		{"remote_natout", r.NatOut},
		{"remote_line", line},
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
		if isRemoteKey(t.key) {
			inBlock[t.line] = append(inBlock[t.line], t)
		}
	}
	// Lines that also hold tokens outside the block (or top-level keys
	// in the middle of it) keep those tokens.
	others := map[int]bool{}
	for i, t := range toks {
		if (i < from || i >= to || !isRemoteKey(t.key)) && t.line >= first && t.line <= last {
			others[t.line] = true
		}
	}
	// A comment or blank line inside the block is dropped only when the
	// next line with keys belongs to the block alone: a comment that
	// introduces a top-level key (route=, timeout=...) stays with it.
	keep := map[int]bool{}
	next := -1 // the next line with tokens, scanning upwards
	for n := last; n >= first; n-- {
		if inBlock[n] != nil || others[n] {
			next = n
			continue
		}
		if next >= 0 && others[next] && inBlock[next] == nil {
			keep[n] = true
		}
	}
	// The comment lines directly above the remote_host_name line (no
	// blank line between) describe this remote: they go with it.
	if strings.TrimSpace(f.lines[first][:toks[from].start]) == "" {
		for first > 0 && strings.HasPrefix(strings.TrimSpace(f.lines[first-1]), "#") {
			first--
		}
	}
	var out []string
	for n, l := range f.lines {
		switch {
		case n < first || n > last || keep[n]:
			out = append(out, l)
		case others[n] && inBlock[n] == nil:
			out = append(out, l) // only top-level keys on this line
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

// routeTok finds the route=<dest>,... token for dest anywhere in the
// file (it may share a line with other keys). A route value contains
// commas, which the scanner treats as separators, so the token is
// widened to the end of the "dest,via[,alt]" run.
func (f *File) routeTok(dest string) (ftok, bool) {
	for n, l := range f.lines {
		for _, t := range scanLine(n, l) {
			if t.key != "route" {
				continue
			}
			t = widenRoute(l, t)
			d, _, _ := strings.Cut(t.val, ",")
			if strings.EqualFold(d, dest) {
				return t, true
			}
		}
	}
	return ftok{}, false
}

// widenRoute extends a route token over the bare words that follow it
// on its line, exactly like the loader glues them (route=X,A,B and
// route=X, A are scanned as route=X plus bare words).
func widenRoute(line string, t ftok) ftok {
	end := t.end
	for i := end; i < len(line); {
		if isSep(line[i]) {
			i++
			continue
		}
		j := i
		for j < len(line) && !isSep(line[j]) {
			j++
		}
		if line[i] == '#' || strings.Contains(line[i:j], "=") {
			break // a comment or the next key=value token
		}
		end, i = j, j
	}
	_, v, _ := strings.Cut(line[t.start:end], "=")
	t.end, t.val = end, v
	return t
}

// lastRouteLine is the last line holding a route token, or -1.
func (f *File) lastRouteLine() int {
	last := -1
	for n, l := range f.lines {
		for _, t := range scanLine(n, l) {
			if t.key == "route" {
				last = n
			}
		}
	}
	return last
}

// SetRoute adds or replaces the route=<dest>,<via>[,<alt>] line for
// r.Dest. A new line goes after the last route line, else after the
// last top-level setting before the first remote block.
func (f *File) SetRoute(r Route) {
	text := "route=" + r.String()
	if t, ok := f.routeTok(r.Dest); ok {
		f.setValue(t, r.String())
		return
	}
	if last := f.lastRouteLine(); last >= 0 {
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
	t, ok := f.routeTok(dest)
	if ok {
		f.removeToken(t)
	}
	return ok
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
	// A symlinked gonje.cnf: write the file it points to, so the link
	// stays a link (renaming over it would replace it with a copy).
	path := f.Path
	if real, err := filepath.EvalSymlinks(f.Path); err == nil {
		path = real
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Make the rename itself durable. Not every system can sync a
	// directory; the file content is already synced, so ignore that.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	f.mode = mode
	return nil
}
