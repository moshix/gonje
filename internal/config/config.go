// Copyright 2026 by moshix

// Package config parses gonje.cnf.
//
// Format: key=value tokens separated by newlines, spaces or commas. A
// token that starts with '#' begins a comment that runs to the end of the
// line ('#' inside a value, e.g. a password, is kept). Keys are case
// insensitive. remote_host_name starts a new remote block; the remote_*
// keys that follow apply to that block. Unknown keys produce a warning and
// are ignored.
package config

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Remote is one remote NJE node definition.
type Remote struct {
	Name       string // NJE node name, 1-8 chars, upper case
	Addr       string // host name or IP; empty = passive (accept only)
	Port       int    // TCP port, default 175
	Keepalive  int    // seconds, default 10
	Password   string // optional node password, upper case
	StrictBCB  bool   // manual BCB rules (E0 + terminate) instead of resync
	Turn       string // "free" (default: send any time) or "strict" (BSC alternation)
	MaxStreams int    // concurrent SYSIN/SYSOUT streams, 1-8 (0 = default 1)
	BufSize    int    // NJE buffer size we advertise (0 = default 8192)
	NatIn      string // our inside (private) IPv4 for this link: dial source
	NatOut     string // our outside (public) IPv4 as the peer sees it
	Line       int    // LINE number (0-999), unique
	LineSet    bool   // Line was given explicitly (remote_line)
}

// Config is the whole gonje configuration.
type Config struct {
	LocalNode    string
	LocalIP      string
	LocalPort    int
	SendDir      string
	RecvDir      string
	LogLevel     string
	SplitView    bool
	SplitRatio   int
	PanelLevel   int
	Timeout      int  // seconds to keep retrying an unreachable remote (0 = forever)
	MsgPane      int  // messages pane rows (default 6, 3-50)
	MsgBell      bool // terminal bell on incoming messages
	Remotes      []*Remote
	Routes       []Route // route=<dest>,<via>[,<alt>]
	DefaultRoute string  // default_route=<node>: anything else goes there

	// Warnings collects non-fatal problems (unknown keys, ...) for the
	// caller to log once the log file is open.
	Warnings []string
}

// Defaults returns a Config with all default values and no node name.
func Defaults() *Config {
	return &Config{
		LocalIP:    "0.0.0.0",
		LocalPort:  175,
		SendDir:    "./outbox",
		RecvDir:    "./inbox",
		LogLevel:   "debug",
		SplitView:  true,
		SplitRatio: 40,
		MsgPane:    6,
		PanelLevel: 1,
	}
}

// Load reads and parses a config file.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// token is one key=value item plus where it came from, for error
// messages: a line of the config file, or (src "-o") the command line.
type token struct {
	key, val string
	line     int
	src      string // "" = config file, "-o" = command line option
}

// where names a token's source in messages: "line 7" or "-o".
func (t token) where() string {
	if t.src != "" {
		return t.src
	}
	return fmt.Sprintf("line %d", t.line)
}

// tokenize splits the input into key=value tokens, dropping comments.
func tokenize(r io.Reader) ([]token, error) { return tokenizeSrc(r, "") }

// tokenizeSrc is tokenize with the source of the text ("" or "-o").
func tokenizeSrc(r io.Reader, src string) ([]token, error) {
	var toks []token
	sc := bufio.NewScanner(r)
	ln := 0
	for sc.Scan() {
		ln++
		fields := strings.FieldsFunc(sc.Text(), func(r rune) bool {
			return r == ' ' || r == '\t' || r == ',' || r == '\r'
		})
		for _, f := range fields {
			if strings.HasPrefix(f, "#") {
				break // comment to end of line
			}
			k, v, ok := strings.Cut(f, "=")
			if !ok && len(toks) > 0 && toks[len(toks)-1].key == "route" && toks[len(toks)-1].line == ln {
				// route=<dest>,<via>[,<alt>]: the commas split it into
				// several fields; glue them back into one value.
				toks[len(toks)-1].val += "," + f
				continue
			}
			if !ok || k == "" {
				return nil, fmt.Errorf("%s: expected key=value, got %q", token{line: ln, src: src}.where(), f)
			}
			// Keys are case-insensitive and '-' is accepted for '_'
			// (e.g. remote_max-streams).
			toks = append(toks, token{normKey(k), v, ln, src})
		}
	}
	return toks, sc.Err()
}

// normKey lower-cases a key and accepts '-' for '_'.
func normKey(k string) string { return strings.ReplaceAll(strings.ToLower(k), "-", "_") }

// Parse parses config text and validates the result.
func Parse(r io.Reader) (*Config, error) {
	toks, err := tokenize(r)
	if err != nil {
		return nil, err
	}
	c, err := build(toks)
	if err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// build applies tokens in order to the defaults (no validation).
// remote_host_name starts a new remote block, except that in -o tokens
// it continues the block of a remote that already exists (so -o can
// change a remote of the config file).
func build(toks []token) (*Config, error) {
	c := Defaults()
	var cur *Remote
	for _, t := range toks {
		bad := func(e error) error { return fmt.Errorf("%s: %s: %v", t.where(), t.key, e) }
		if strings.HasPrefix(t.key, "remote_") && t.key != "remote_host_name" && cur == nil {
			return nil, fmt.Errorf("%s: %s before remote_host_name", t.where(), t.key)
		}
		switch t.key {
		case "local_node":
			n, err := NodeName(t.val)
			if err != nil {
				return nil, bad(err)
			}
			c.LocalNode = n
		case "local_ip":
			c.LocalIP = t.val
		case "local_port":
			p, err := port(t.val)
			if err != nil {
				return nil, bad(err)
			}
			c.LocalPort = p
		case "send_dir":
			c.SendDir = t.val
		case "recv_dir":
			c.RecvDir = t.val
		case "log_level":
			v := strings.ToLower(t.val)
			switch v {
			case "debug", "info", "warn", "error":
				c.LogLevel = v
			default:
				return nil, bad(fmt.Errorf("must be debug, info, warn or error"))
			}
		case "split_view":
			b, err := boolean(t.val)
			if err != nil {
				return nil, bad(err)
			}
			c.SplitView = b
		case "split_ratio":
			n, err := intRange(t.val, 10, 90)
			if err != nil {
				return nil, bad(err)
			}
			c.SplitRatio = n
		case "msg_pane":
			n, err := intRange(t.val, 3, 50)
			if err != nil {
				return nil, bad(err)
			}
			c.MsgPane = n
		case "msg_bell":
			b, err := boolean(t.val)
			if err != nil {
				return nil, bad(err)
			}
			c.MsgBell = b
		case "route":
			r, err := ParseRoute(t.val)
			if err != nil {
				return nil, bad(err)
			}
			c.Routes = append(c.Routes, r)
		case "default_route":
			n, err := NodeName(t.val)
			if err != nil {
				return nil, bad(err)
			}
			c.DefaultRoute = n
		case "timeout":
			n, err := Timeout(t.val)
			if err != nil {
				return nil, bad(err)
			}
			c.Timeout = n
		case "panel_level":
			n, err := intRange(t.val, 1, 3)
			if err != nil {
				return nil, bad(err)
			}
			c.PanelLevel = n
		case "remote_host_name":
			n, err := NodeName(t.val)
			if err != nil {
				return nil, bad(err)
			}
			if t.src != "" {
				if r := c.Remote(n); r != nil {
					cur = r // -o continues an existing remote's block
					continue
				}
			}
			cur = &Remote{Name: n, Port: 175, Keepalive: 10}
			c.Remotes = append(c.Remotes, cur)
		case "remote_host_addr":
			cur.Addr = t.val
		case "remote_natin", "remote_natout":
			ip, err := IPv4(t.val)
			if err != nil {
				return nil, bad(err)
			}
			if t.key == "remote_natin" {
				cur.NatIn = ip
			} else {
				cur.NatOut = ip
			}
		case "remote_line":
			n, err := LineNumber(t.val)
			if err != nil {
				return nil, bad(err)
			}
			cur.Line, cur.LineSet = n, true
		case "remote_host_port":
			p, err := port(t.val)
			if err != nil {
				return nil, bad(err)
			}
			cur.Port = p
		case "remote_keepalive":
			n, err := Keepalive(t.val)
			if err != nil {
				return nil, bad(err)
			}
			cur.Keepalive = n
		case "remote_buffersize":
			n, err := BufSize(t.val)
			if err != nil {
				return nil, bad(err)
			}
			cur.BufSize = n
		case "remote_max_streams":
			n, err := MaxStreams(t.val)
			if err != nil {
				return nil, bad(err)
			}
			cur.MaxStreams = n
		case "remote_turn":
			v := strings.ToLower(t.val)
			if v != "free" && v != "strict" {
				return nil, bad(fmt.Errorf("must be free or strict"))
			}
			cur.Turn = v
		case "remote_strict_bcb":
			b, err := boolean(t.val)
			if err != nil {
				return nil, bad(err)
			}
			cur.StrictBCB = b
		case "remote_password":
			pw, err := Password(t.val)
			if err != nil {
				return nil, bad(err)
			}
			cur.Password = pw
		default:
			c.Warnings = append(c.Warnings,
				fmt.Sprintf("config %s: unknown key %q ignored", t.where(), t.key))
		}
	}
	return c, nil
}

// LoadWithOptions reads the config file at path (if it exists, or
// always when required) and then applies the -o option strings, in
// order, as if they followed the file: later values override, a remote
// named again in -o is extended, new remotes are added. The result is
// validated as a whole; a failure that only the -o tokens cause names
// "-o". hasFile reports whether a config file was read.
func LoadWithOptions(path string, required bool, opts []string) (c *Config, hasFile bool, err error) {
	var toks []token
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		hasFile = true
		if toks, err = tokenize(strings.NewReader(string(b))); err != nil {
			return nil, true, err
		}
		// The file must be valid on its own (the shell saves to it).
		if _, err := Parse(strings.NewReader(string(b))); err != nil {
			return nil, true, err
		}
	case required || len(opts) == 0 || !os.IsNotExist(err):
		return nil, false, err
	}
	for _, o := range opts {
		ot, err := tokenizeSrc(strings.NewReader(o), "-o")
		if err != nil {
			return nil, hasFile, err
		}
		toks = append(toks, ot...)
	}
	if c, err = build(toks); err != nil {
		return nil, hasFile, err
	}
	if err := c.validate(); err != nil {
		if len(opts) > 0 {
			err = fmt.Errorf("-o: %w", err)
		}
		return nil, hasFile, err
	}
	return c, hasFile, nil
}

func (c *Config) validate() error {
	if c.LocalNode == "" {
		return fmt.Errorf("local_node is required")
	}
	if err := AssignLines(c.Remotes); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range c.Remotes {
		if r.Name == c.LocalNode {
			return fmt.Errorf("remote %s has the same name as local_node", r.Name)
		}
		if seen[r.Name] {
			return fmt.Errorf("remote %s defined twice", r.Name)
		}
		seen[r.Name] = true
	}
	dests := map[string]bool{}
	for _, r := range c.Routes {
		if err := ValidateRoute(r, c); err != nil {
			return err
		}
		if dests[r.Dest] {
			return fmt.Errorf("route to %s defined twice", r.Dest)
		}
		dests[r.Dest] = true
	}
	if c.DefaultRoute != "" && c.Remote(c.DefaultRoute) == nil {
		return fmt.Errorf("default_route %s is not a configured remote node", c.DefaultRoute)
	}
	return nil
}

// Route sends traffic for node Dest through the directly linked node
// Via, or Alt when Via's link is not up.
type Route struct {
	Dest, Via, Alt string
}

// String is the config form "<dest>,<via>[,<alt>]".
func (r Route) String() string {
	s := r.Dest + "," + r.Via
	if r.Alt != "" {
		s += "," + r.Alt
	}
	return s
}

// ParseRoute parses "<dest>,<via>[,<alt>]" (names only, see ValidateRoute).
func ParseRoute(v string) (Route, error) {
	p := strings.Split(v, ",")
	if len(p) < 2 || len(p) > 3 {
		return Route{}, fmt.Errorf("route must be <dest>,<via>[,<alt>]")
	}
	var r Route
	var err error
	if r.Dest, err = NodeName(p[0]); err != nil {
		return r, err
	}
	if r.Via, err = NodeName(p[1]); err != nil {
		return r, err
	}
	if len(p) == 3 {
		if r.Alt, err = NodeName(p[2]); err != nil {
			return r, err
		}
	}
	return r, nil
}

// ValidateRoute checks a route against the configuration: via and alt
// must be defined links; dest can be neither us nor the via node.
func ValidateRoute(r Route, c *Config) error {
	switch {
	case r.Dest == c.LocalNode:
		return fmt.Errorf("route to %s: that is the local node", r.Dest)
	case r.Dest == r.Via || r.Dest == r.Alt:
		return fmt.Errorf("route to %s cannot go via itself", r.Dest)
	case c.Remote(r.Via) == nil:
		return fmt.Errorf("route to %s: via %s is not a configured remote node", r.Dest, r.Via)
	case r.Alt != "" && c.Remote(r.Alt) == nil:
		return fmt.Errorf("route to %s: alt %s is not a configured remote node", r.Dest, r.Alt)
	}
	return nil
}

// Remote returns the remote with the given (case-insensitive) name.
func (c *Config) Remote(name string) *Remote {
	name = strings.ToUpper(name)
	for _, r := range c.Remotes {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// NodeName validates and upper-cases an NJE node name: 1-8 characters
// from A-Z, 0-9, @, # and $.
func NodeName(s string) (string, error) {
	s = strings.ToUpper(s)
	if len(s) < 1 || len(s) > 8 {
		return "", fmt.Errorf("node name %q must be 1-8 characters", s)
	}
	for _, r := range s {
		ok := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '@' || r == '#' || r == '$'
		if !ok {
			return "", fmt.Errorf("node name %q has invalid character %q", s, r)
		}
	}
	return s, nil
}

func port(s string) (int, error) { return intRange(s, 1, 65535) }

// Port validates a TCP port number (1-65535).
func Port(s string) (int, error) { return port(s) }

// IPv4 validates a dotted IPv4 address (NAT settings).
func IPv4(s string) (string, error) {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("%q is not an IPv4 address", s)
	}
	return ip.To4().String(), nil
}

// LineNumber validates a LINE number (0-999).
func LineNumber(s string) (int, error) {
	n, err := intRange(s, 0, 999)
	if err != nil {
		return 0, fmt.Errorf("line: %v", err)
	}
	return n, nil
}

// AssignLines gives every remote without an explicit remote_line the
// next free LINE number, in order of appearance from 0, and rejects
// duplicate explicit numbers (Sine Nomine style "LINE n").
func AssignLines(rs []*Remote) error {
	used := map[int]string{}
	for _, r := range rs {
		if !r.LineSet {
			continue
		}
		if other, dup := used[r.Line]; dup {
			return fmt.Errorf("line %d used by both %s and %s", r.Line, other, r.Name)
		}
		used[r.Line] = r.Name
	}
	next := 0
	for _, r := range rs {
		if r.LineSet {
			continue
		}
		for used[next] != "" {
			next++
		}
		r.Line = next
		used[next] = r.Name
	}
	return nil
}

// MaxStreams validates a stream count: 1-8 (the FCS has bits for 8
// shared SYSIN/SYSOUT streams).
func MaxStreams(s string) (int, error) {
	n, err := intRange(s, 1, 8)
	if err != nil {
		return 0, fmt.Errorf("max_streams: %v", err)
	}
	return n, nil
}

// Timeout validates the general connect timeout in seconds (0 = never).
func Timeout(s string) (int, error) { return intRange(s, 0, 86400) }

// BufSize validates an NJE buffer size (the manual's range 300-32768).
func BufSize(s string) (int, error) {
	n, err := intRange(s, 300, 32768)
	if err != nil {
		return 0, fmt.Errorf("buffersize: %v", err)
	}
	return n, nil
}

// Keepalive validates a keepalive interval in seconds (1-3600).
func Keepalive(s string) (int, error) { return intRange(s, 1, 3600) }

// Password validates and upper-cases a node password (0-8 characters,
// no blanks or commas, which the file format could not hold).
func Password(s string) (string, error) {
	if len(s) > 8 {
		return "", fmt.Errorf("password longer than 8 characters")
	}
	if strings.ContainsAny(s, " \t,") || strings.HasPrefix(s, "#") {
		return "", fmt.Errorf("password may not contain blanks or commas or start with #")
	}
	return strings.ToUpper(s), nil
}

// Bool parses yes/no style values.
func Bool(s string) (bool, error) { return boolean(s) }

// ValidateRemote checks a remote definition the way the loader does.
func ValidateRemote(r *Remote, local string) error {
	n, err := NodeName(r.Name)
	if err != nil {
		return err
	}
	r.Name = n
	if n == strings.ToUpper(local) {
		return fmt.Errorf("remote %s has the same name as the local node", n)
	}
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("port %d out of range 1-65535", r.Port)
	}
	if r.Keepalive < 1 || r.Keepalive > 3600 {
		return fmt.Errorf("keepalive %d out of range 1-3600", r.Keepalive)
	}
	if strings.ContainsAny(r.Addr, " \t,#") {
		return fmt.Errorf("address %q contains blanks, commas or #", r.Addr)
	}
	pw, err := Password(r.Password)
	if err != nil {
		return err
	}
	r.Password = pw
	for _, ip := range []string{r.NatIn, r.NatOut} {
		if ip != "" {
			if _, err := IPv4(ip); err != nil {
				return err
			}
		}
	}
	if r.Line < 0 || r.Line > 999 {
		return fmt.Errorf("line %d out of range 0-999", r.Line)
	}
	if r.BufSize != 0 && (r.BufSize < 300 || r.BufSize > 32768) {
		return fmt.Errorf("buffersize %d out of range 300-32768", r.BufSize)
	}
	if r.MaxStreams < 0 || r.MaxStreams > 8 {
		return fmt.Errorf("max_streams %d out of range 1-8", r.MaxStreams)
	}
	switch r.Turn = strings.ToLower(r.Turn); r.Turn {
	case "", "free", "strict":
	default:
		return fmt.Errorf("turn must be free or strict")
	}
	return nil
}

func intRange(s string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%d out of range %d-%d", n, lo, hi)
	}
	return n, nil
}

func boolean(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "yes", "y", "true", "on", "1":
		return true, nil
	case "no", "n", "false", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("%q is not yes/no", s)
}

// secretRE matches password=<value> and remote_password=<value> (any
// case, '-' for '_') as a word of a command line or -o string.
var secretRE = regexp.MustCompile(`(^|[\s,])((?i:remote[_-]password|password)=)[^\s,]*`)

// MaskSecrets replaces password values with "***" (for logs, history and
// echoed commands).
func MaskSecrets(line string) string {
	return secretRE.ReplaceAllString(line, "${1}${2}***")
}

// Keys lists every key the loader accepts (normalized: lower case, '_').
var Keys = []string{"local_node", "local_ip", "local_port", "send_dir", "recv_dir", "log_level",
	"panel_level", "split_view", "split_ratio", "msg_pane", "msg_bell", "timeout", "route", "default_route",
	"remote_host_name", "remote_host_addr", "remote_host_port", "remote_keepalive", "remote_password",
	"remote_turn", "remote_buffersize", "remote_max_streams", "remote_line", "remote_natin",
	"remote_natout", "remote_strict_bcb"}

// IsKey reports whether k is a config key (any case, '-' for '_').
func IsKey(k string) bool {
	k = normKey(k)
	for _, x := range Keys {
		if x == k {
			return true
		}
	}
	return false
}
