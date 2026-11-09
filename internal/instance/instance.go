// Copyright 2026 by moshix

// Package instance keeps one interactive gonje per config file (the
// gonje.pid lock file next to the config) and explains why the listener
// port is busy when it is.
package instance

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gonje/internal/config"
	"gonje/internal/nje/tcpip"
)

// PidFile is the name of the lock file, in the config file's directory.
const PidFile = "gonje.pid"

// Info is the content of a pid file.
type Info struct {
	PID     int
	Version string
	Node    string
	Listen  string // ip:port
	Config  string // absolute config path
	Started time.Time
}

// PidPath returns the pid file path for a config file; without one
// (cfgPath "": everything given with -o) it is in the current directory.
func PidPath(cfgPath string) string {
	if cfgPath == "" {
		return PidFile
	}
	return filepath.Join(filepath.Dir(absPath(cfgPath)), PidFile)
}

// absPath is the absolute, symlink-free form of p (as far as it exists),
// so two spellings of the same config file compare equal.
func absPath(p string) string {
	if p == "" {
		return ""
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if r, err := filepath.EvalSymlinks(a); err == nil {
		return r
	}
	return a
}

// ListenAddr is the listener address of a config ("ip:port").
func ListenAddr(cfg *config.Config) string {
	return net.JoinHostPort(cfg.LocalIP, strconv.Itoa(cfg.LocalPort))
}

// NewInfo describes this process for cfg loaded from cfgPath ("" = no
// config file, only -o options).
func NewInfo(version string, cfg *config.Config, cfgPath string) Info {
	return Info{PID: os.Getpid(), Version: version, Node: cfg.LocalNode, Listen: ListenAddr(cfg),
		Config: absPath(cfgPath), Started: time.Now()}
}

// encode writes the key=value form of the pid file.
func (i Info) encode() []byte {
	return []byte(fmt.Sprintf("pid=%d\nversion=%s\nnode=%s\nlisten=%s\nconfig=%s\nstarted=%s\n",
		i.PID, i.Version, i.Node, i.Listen, i.Config, i.Started.Format(time.RFC3339)))
}

// Read parses a pid file.
func Read(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var i Info
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "pid":
			i.PID, _ = strconv.Atoi(v)
		case "version":
			i.Version = v
		case "node":
			i.Node = v
		case "listen":
			i.Listen = v
		case "config":
			i.Config = v
		case "started":
			i.Started, _ = time.Parse(time.RFC3339, v)
		}
	}
	if i.PID <= 0 {
		return nil, fmt.Errorf("%s: no pid", path)
	}
	return &i, nil
}

// Alive reports whether a process with this pid exists (kill(pid, 0):
// EPERM means it exists but belongs to someone else).
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Running describes a live instance for the operator.
func (i *Info) Running() string {
	return fmt.Sprintf("another gonje (pid %d, node %s, started %s) is already running with this config on %s",
		i.PID, i.Node, i.Started.Format("15:04"), i.Listen)
}

// ErrRunning is returned by Acquire when a live gonje holds the lock.
type ErrRunning struct{ Info *Info }

func (e *ErrRunning) Error() string { return e.Info.Running() }

// Lock is a held pid file.
type Lock struct {
	path string
	pid  int
}

// Acquire takes the pid file of cfgPath for info. It fails with
// *ErrRunning when a live process already holds it for the same config.
// A stale file (process gone) is removed: stale reports it. When a live
// process holds the file for another config in the same directory, no
// lock is taken (nil Lock, no error) so that instance keeps its file.
func Acquire(cfgPath string, info Info) (l *Lock, stale *Info, err error) {
	path := PidPath(cfgPath)
	for try := 0; try < 2; try++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, werr := f.Write(info.encode())
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				os.Remove(path)
				return nil, stale, werr
			}
			return &Lock{path: path, pid: info.PID}, stale, nil
		}
		if !os.IsExist(err) {
			return nil, stale, err
		}
		old, rerr := Read(path)
		switch {
		case rerr == nil && old.PID != info.PID && Alive(old.PID):
			if sameInstance(old, &info) {
				return nil, stale, &ErrRunning{Info: old}
			}
			return nil, stale, nil // another config's instance
		case rerr == nil:
			stale = old
		}
		// Stale (or unreadable): remove it and try again.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, stale, err
		}
	}
	return nil, stale, fmt.Errorf("%s: cannot create the pid file", path)
}

// sameInstance reports whether a running gonje has "this config": the
// same config file, or, when either runs without one (-o only), the
// same local node and listen address.
func sameInstance(old, me *Info) bool {
	if old.Config != "" && me.Config != "" {
		return old.Config == me.Config
	}
	return strings.EqualFold(old.Node, me.Node) && old.Listen == me.Listen
}

// Release removes the pid file if it is still ours. Safe on nil.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	if i, err := Read(l.path); err == nil && i.PID == l.pid {
		os.Remove(l.path)
	}
}

// Diagnose explains a failed bind of cfg's listener: a running gonje
// (from the pid file), an NJE node answering our OPEN probe, or another
// program; plus a hint when the port is also a remote node's port.
func Diagnose(cfg *config.Config, cfgPath string, bindErr error) string {
	addr := ListenAddr(cfg)
	if !isAddrInUse(bindErr) {
		return fmt.Sprintf("cannot listen on %s: %v", addr, bindErr)
	}
	var msg string
	if i, err := Read(PidPath(cfgPath)); err == nil && i.PID != os.Getpid() && Alive(i.PID) &&
		((i.Config != "" && i.Config == absPath(cfgPath)) || i.Listen == addr) {
		msg = i.Running()
	} else if ProbeNJE(addr, cfg.LocalNode, 2*time.Second) {
		msg = fmt.Sprintf("an NJE node (probably another gonje or NJE server) is listening on %s", addr)
	} else {
		msg = fmt.Sprintf("address %s is in use by another program", addr)
	}
	for _, r := range cfg.Remotes {
		if r.Port == cfg.LocalPort {
			msg += fmt.Sprintf("; port %d is also configured as the port of remote node %s — is a peer running on this machine?",
				cfg.LocalPort, r.Name)
		}
	}
	return msg
}

// isAddrInUse reports EADDRINUSE.
func isAddrInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }

// ProbeNJE connects to addr, sends an NJE OPEN and reports whether the
// answer is an NJE control record (ACK or NAK). The probe names itself
// PROBE, so a real node answers NAK and logs nothing worse.
func ProbeNJE(addr, node string, timeout time.Duration) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1" // listening on all addresses: try loopback
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	open := &tcpip.Control{Type: tcpip.TypeOpen, RHost: "PROBE", RIP: net.IPv4(127, 0, 0, 1),
		OHost: strings.ToUpper(node), OIP: net.ParseIP(host)}
	if _, err := c.Write(open.Marshal()); err != nil {
		return false
	}
	r, _, err := tcpip.ReadControl(c)
	return err == nil && (r.Type == tcpip.TypeACK || r.Type == tcpip.TypeNAK)
}
