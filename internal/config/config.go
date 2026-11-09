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
	"os"
	"strconv"
	"strings"
)

// Remote is one remote NJE node definition.
type Remote struct {
	Name      string // NJE node name, 1-8 chars, upper case
	Addr      string // host name or IP; empty = passive (accept only)
	Port      int    // TCP port, default 175
	Keepalive int    // seconds, default 10
	Password  string // optional node password, upper case
	StrictBCB bool   // manual BCB rules (E0 + terminate) instead of resync
	Turn      string // "free" (default: send any time) or "strict" (BSC alternation)
	EOFWait   int    // seconds to wait for C0 after EOF (0 = default 10)
}

// Config is the whole gonje configuration.
type Config struct {
	LocalNode  string
	LocalIP    string
	LocalPort  int
	SendDir    string
	RecvDir    string
	LogLevel   string
	SplitView  bool
	SplitRatio int
	PanelLevel int
	Remotes    []*Remote

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

// token is one key=value item plus its line number for error messages.
type token struct {
	key, val string
	line     int
}

// tokenize splits the input into key=value tokens, dropping comments.
func tokenize(r io.Reader) ([]token, error) {
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
			if !ok || k == "" {
				return nil, fmt.Errorf("line %d: expected key=value, got %q", ln, f)
			}
			toks = append(toks, token{strings.ToLower(k), v, ln})
		}
	}
	return toks, sc.Err()
}

// Parse parses config text and validates the result.
func Parse(r io.Reader) (*Config, error) {
	toks, err := tokenize(r)
	if err != nil {
		return nil, err
	}
	c := Defaults()
	var cur *Remote
	for _, t := range toks {
		bad := func(e error) error { return fmt.Errorf("line %d: %s: %v", t.line, t.key, e) }
		if strings.HasPrefix(t.key, "remote_") && t.key != "remote_host_name" && cur == nil {
			return nil, fmt.Errorf("line %d: %s before remote_host_name", t.line, t.key)
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
			cur = &Remote{Name: n, Port: 175, Keepalive: 10}
			c.Remotes = append(c.Remotes, cur)
		case "remote_host_addr":
			cur.Addr = t.val
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
		case "remote_eof_wait":
			n, err := intRange(t.val, 1, 3600)
			if err != nil {
				return nil, bad(err)
			}
			cur.EOFWait = n
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
				fmt.Sprintf("config line %d: unknown key %q ignored", t.line, t.key))
		}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.LocalNode == "" {
		return fmt.Errorf("local_node is required")
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
	if r.EOFWait < 0 || r.EOFWait > 3600 {
		return fmt.Errorf("eof_wait %d out of range 1-3600", r.EOFWait)
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
