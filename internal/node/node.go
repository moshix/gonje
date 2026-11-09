// Copyright 2026 by moshix

// Package node is the NJE node manager: it owns the listener, one link
// per configured remote (connect, accept, reconnect), the outbound file
// queues, the event bus, received-file storage and the auto-answer for
// inbound commands.
package node

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/user"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"gonje/internal/config"
	"gonje/internal/ebcdic"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
	"gonje/internal/nje/tcpip"
)

// Version is reported by Q SYS. main sets it from its global version
// variable at startup, which is the single source of truth.
var Version = "dev"

// State of a link.
type State int

// Link states.
const (
	Down       State = iota
	Connecting       // we dialled out: OPEN / signon as primary in progress
	Accepting        // inbound OPEN accepted: signon as secondary in progress
	Up               // signed on
	Draining         // signed on, finishing transfers before signoff (drain)
)

func (s State) String() string {
	switch s {
	case Connecting:
		return "CONNECTING"
	case Accepting:
		return "ACCEPTING"
	case Up:
		return "UP"
	case Draining:
		return "DRAINING"
	}
	return "DOWN"
}

// Options are tuning knobs that are not in gonje.cnf (tests shorten them).
type Options struct {
	RetryMin    time.Duration // first reconnect delay (default 5s)
	RetryMax    time.Duration // reconnect backoff cap (default 60s)
	NAKMin      time.Duration // random wait after NAK 02-04 (default 1s)
	NAKMax      time.Duration // (default 5s)
	Handshake   time.Duration // per-step handshake timeout (default 30s)
	StartJitter time.Duration // random delay before first connect (500ms)
	StableAfter time.Duration // a link up this long resets the backoff (60s)
	// DialHandshake: per-step timeout when we dialled (default 15 s,
	// shorter than Handshake so a peer that prefers to call us gets in).
	DialHandshake time.Duration
	// PreferInbound: during our own dial, always accept the peer's
	// inbound connection (batch mode). OnlyRemote: dial and accept only
	// this remote (batch mode).
	PreferInbound bool
	OnlyRemote    string
	// StreamSettle overrides the link's post-signon settle time (tests).
	StreamSettle time.Duration
	User         string // local user id for origin fields
	Log          *logx.Logger
}

func (o *Options) defaults() {
	if o.RetryMin == 0 {
		o.RetryMin = 5 * time.Second
	}
	if o.RetryMax == 0 {
		o.RetryMax = 60 * time.Second
	}
	if o.NAKMin == 0 {
		o.NAKMin = 2 * time.Second
	}
	if o.NAKMax <= o.NAKMin {
		o.NAKMax = o.NAKMin + 3*time.Second
	}
	if o.Handshake == 0 {
		o.Handshake = link.DefaultHandshake
	}
	if o.StartJitter == 0 {
		o.StartJitter = 500 * time.Millisecond
	}
	if o.StableAfter == 0 {
		o.StableAfter = time.Minute
	}
	if o.DialHandshake == 0 {
		o.DialHandshake = min(15*time.Second, o.Handshake)
	}
	if o.User == "" {
		o.User = defaultUser()
	}
	o.User = nameField(o.User)
	if o.User == "" {
		o.User = "GONJE"
	}
	if o.Log == nil {
		o.Log = logx.Discard()
	}
}

// defaultUser returns the OS login name (upper case, max 8 characters).
func defaultUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// remote is the runtime state of one configured remote node.
// All fields are guarded by Node.mu.
type remote struct {
	cfg         *config.Remote
	state       State
	gen         int // bumped on every new attempt; stale attempts give up
	sess        *link.Session
	cancelDial  context.CancelFunc
	retry       *time.Timer
	retryDelay  time.Duration
	held        bool // operator dropped it: no automatic reconnect
	queue       []*link.OutboundFile
	lastErr     string
	since       time.Time
	lastCmd     time.Time // when we last sent this node a command
	bounce      bool      // reconnect after the current session (local rename)
	pendingCmds int       // commands sent, no response yet (shown in the title)
	dialStart   time.Time // when the current outbound attempt began
	tryStart    time.Time // first failed attempt since the last signon (timeout)
	draining    bool      // "drain": sign off once no transfer is active

	// Counters for "stats" (in memory only, guarded by Node.mu). totals
	// holds finished sessions; base is the live session's snapshot at
	// the last "stats reset", so live counts = sess.Stats() - base.
	totals    link.Stats
	base      link.Stats
	connects  int
	failures  int
	connected time.Duration // finished sessions
	connStart time.Time     // start of the live session (or last reset)
	rspIn     int
	rspOut    int
}

// Node is a running gonje NJE node.
type Node struct {
	cfg   *config.Config
	opts  Options
	log   *logx.Logger
	bus   *Bus
	start time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// lmu guards the local identity (cfg.LocalNode/LocalIP/LocalPort)
	// and the listener, which the operator can change at runtime.
	lmu     sync.RWMutex
	ln      net.Listener
	started bool

	lnmu  sync.Mutex
	lines map[string]int // remote name -> LINE number (event lines)

	mu       sync.Mutex
	accepted int       // inbound connections signed on (stats)
	rejected int       // inbound OPENs answered with NAK (stats)
	since    time.Time // counters since (start or "stats reset")
	remotes  map[string]*remote
	order    []string
	stopping bool
	jobID    int
	fileID   int
}

// New creates a node; call Listen and Start to run it.
func New(cfg *config.Config, opts Options) *Node {
	opts.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{cfg: cfg, opts: opts, log: opts.Log, bus: NewBus(opts.Log), start: time.Now(), since: time.Now(),
		ctx: ctx, cancel: cancel, remotes: map[string]*remote{}}
	_ = config.AssignLines(cfg.Remotes) // configs built in code (tests) get lines too
	n.lines = map[string]int{}
	for _, r := range cfg.Remotes {
		n.setLine(r.Name, r.Line, false)
		n.remotes[r.Name] = &remote{cfg: r, retryDelay: opts.RetryMin}
		n.order = append(n.order, r.Name)
	}
	n.bus.lineOf = n.lineOf
	return n
}

// lineOf returns the LINE number of a remote (for event lines). It uses
// its own small lock: events are emitted while n.mu is held.
func (n *Node) lineOf(name string) (int, bool) {
	n.lnmu.Lock()
	defer n.lnmu.Unlock()
	l, ok := n.lines[strings.ToUpper(name)]
	return l, ok
}

// setLine records (or with del, forgets) a remote's LINE number.
func (n *Node) setLine(name string, line int, del bool) {
	n.lnmu.Lock()
	defer n.lnmu.Unlock()
	if del {
		delete(n.lines, name)
	} else {
		n.lines[name] = line
	}
}

// Config returns the node's configuration.
func (n *Node) Config() *config.Config { return n.cfg }

// User returns the local user id used as message/file origin.
func (n *Node) User() string { return n.opts.User }

// Bus returns the event bus.
func (n *Node) Bus() *Bus { return n.bus }

// emit publishes an event.
func (n *Node) emit(k link.Kind, level int, node, text string) {
	n.bus.Publish(link.Event{Time: time.Now(), Kind: k, Level: level, Node: node, Text: text})
}

// localName returns the current local node name.
func (n *Node) localName() string {
	n.lmu.RLock()
	defer n.lmu.RUnlock()
	return n.cfg.LocalNode
}

// LocalInfo describes the local node for the shell.
type LocalInfo struct {
	Name     string
	IP       string // configured listen address
	Port     int    // configured listen port
	Listen   string // actual listener address ("" if not listening)
	Links    int    // links defined
	Up       int    // links up
	Draining int    // links draining
	Pending  int    // commands still waiting for a first response
	Timeout  int    // general connect timeout in seconds (0 = never give up)
}

// Local returns the local node's identity and listener.
func (n *Node) Local() LocalInfo {
	n.lmu.RLock()
	li := LocalInfo{Name: n.cfg.LocalNode, IP: n.cfg.LocalIP, Port: n.cfg.LocalPort}
	n.mu.Lock()
	li.Timeout = n.cfg.Timeout
	n.mu.Unlock()
	if n.ln != nil {
		li.Listen = n.ln.Addr().String()
	}
	n.lmu.RUnlock()
	n.mu.Lock()
	for _, r := range n.remotes {
		// Responses can take 10-30 s; give up showing them as pending
		// after the response window (5 minutes).
		if r.pendingCmds > 0 && time.Since(r.lastCmd) < responseWindow {
			li.Pending += r.pendingCmds
		}
	}
	n.mu.Unlock()
	for _, st := range n.Status() {
		li.Links++
		switch st.State {
		case Up:
			li.Up++
		case Draining:
			li.Draining++
		}
	}
	return li
}

// Listen binds the NJE listener (local_IP:local_port; port 0 = any).
func (n *Node) Listen() error {
	n.lmu.Lock()
	defer n.lmu.Unlock()
	addr := net.JoinHostPort(n.cfg.LocalIP, strconv.Itoa(n.cfg.LocalPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	n.ln = ln
	n.log.Info(fmt.Sprintf("node %s listening on %s", n.cfg.LocalNode, ln.Addr()))
	return nil
}

// Addr returns the listener address (after Listen).
func (n *Node) Addr() net.Addr {
	n.lmu.RLock()
	defer n.lmu.RUnlock()
	return n.ln.Addr()
}

// Start begins accepting connections and connects to every remote that
// has an address, after a small random delay to spread out the startup
// connect race between two nodes that both dial.
func (n *Node) Start() {
	n.lmu.Lock()
	n.started = true
	if n.ln != nil {
		n.startAccept(n.ln)
	}
	n.lmu.Unlock()
	n.recoverForwards() // files accepted for other nodes before a restart
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, name := range n.order {
		r := n.remotes[name]
		if r.cfg.Addr != "" {
			n.scheduleLocked(r, randDuration(0, n.opts.StartJitter))
		}
	}
}

func (n *Node) startAccept(ln net.Listener) {
	n.wg.Add(1)
	go n.acceptLoop(ln)
}

// Stop closes all links WITHOUT signoff (so peers accept us again as
// soon as we restart), closes the listener and waits (bounded).
func (n *Node) Stop() {
	n.mu.Lock()
	n.stopping = true
	if c := n.queuedForwardsLocked(); c > 0 {
		n.log.Warn(fmt.Sprintf("%d forwarded file(s) not yet delivered: kept in %s, sent again at the next start",
			c, ForwardDir(n.cfg.RecvDir)))
	}
	var sessions []*link.Session
	for _, r := range n.remotes {
		if r.retry != nil {
			r.retry.Stop()
		}
		if r.sess != nil {
			r.held = true
			sessions = append(sessions, r.sess)
			r.sess.CloseQuiet("shutdown")
		}
	}
	n.mu.Unlock()
	for _, s := range sessions {
		select {
		case <-s.Done():
		case <-time.After(2 * time.Second):
			s.Kill()
		}
	}
	n.cancel()
	n.lmu.Lock()
	if n.ln != nil {
		_ = n.ln.Close()
	}
	n.lmu.Unlock()
	n.wg.Wait()
}

// SetListen moves the listener to ip:port. The new socket is opened
// before the old one is closed, so a failure leaves the node listening
// where it was. Re-binding the same port on another address can clash
// with the old socket (Linux); then the old one is closed first and
// re-opened if the new bind still fails (rollback).
func (n *Node) SetListen(ip string, port int) error {
	n.mu.Lock()
	stopping := n.stopping
	n.mu.Unlock()
	if stopping {
		return errors.New("node is shutting down")
	}
	n.lmu.Lock()
	defer n.lmu.Unlock()
	newAddr := net.JoinHostPort(ip, strconv.Itoa(port))
	old := n.ln
	ln, err := net.Listen("tcp", newAddr)
	if err != nil && old != nil && port == n.cfg.LocalPort {
		oldAddr := old.Addr().String()
		_ = old.Close()
		old = nil
		if ln, err = net.Listen("tcp", newAddr); err != nil {
			back, berr := net.Listen("tcp", oldAddr)
			if berr != nil {
				n.ln = nil
				return fmt.Errorf("listen on %s: %v; re-opening %s also failed: %v", newAddr, err, oldAddr, berr)
			}
			n.ln = back
			if n.started {
				n.startAccept(back)
			}
			return fmt.Errorf("listen on %s: %v (still listening on %s)", newAddr, err, oldAddr)
		}
	} else if err != nil {
		where := "nowhere"
		if old != nil {
			where = old.Addr().String()
		}
		return fmt.Errorf("listen on %s: %v (still listening on %s)", newAddr, err, where)
	}
	if old != nil {
		_ = old.Close()
	}
	n.ln = ln
	n.cfg.LocalIP, n.cfg.LocalPort = ip, port
	if n.started {
		n.startAccept(ln)
	}
	n.log.Info(fmt.Sprintf("local listener moved to %s", ln.Addr()))
	n.emit(link.EvLink, link.L2, "", "listening on "+ln.Addr().String())
	return nil
}

// SetLocalName renames the local node. Every active link signs off and
// reconnects under the new name (the name is part of OPEN and signon);
// attempts in progress are restarted. It returns how many links were up.
func (n *Node) SetLocalName(name string) (int, error) {
	nn, err := config.NodeName(name)
	if err != nil {
		return 0, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.remotes[nn] != nil {
		return 0, fmt.Errorf("%s is the name of a remote link", nn)
	}
	n.lmu.Lock()
	old := n.cfg.LocalNode
	n.cfg.LocalNode = nn
	n.lmu.Unlock()
	bounced := 0
	for _, r := range n.remotes {
		switch r.state {
		case Up:
			bounced++
			r.bounce = true
			r.sess.CloseQuiet("local node renamed")
		case Connecting, Accepting:
			// Restart the attempt so it uses the new name.
			r.gen++
			r.state = Down
			if r.cancelDial != nil {
				r.cancelDial()
			}
			n.scheduleLocked(r, randDuration(0, n.opts.StartJitter))
		}
	}
	n.log.Info(fmt.Sprintf("local node renamed %s -> %s, %d link(s) re-signing on", old, nn, bounced))
	return bounced, nil
}

// SetTimeout changes the general connect timeout (seconds, 0 = never).
func (n *Node) SetTimeout(sec int) error {
	if sec < 0 || sec > 86400 {
		return fmt.Errorf("timeout %d out of range 0-86400", sec)
	}
	n.mu.Lock()
	n.cfg.Timeout = sec
	n.mu.Unlock()
	n.log.Info(fmt.Sprintf("general timeout set to %d s", sec))
	return nil
}

// RemoteConfig returns a copy of a remote's definition.
func (n *Node) RemoteConfig(name string) (config.Remote, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.lookup(name)
	if err != nil {
		return config.Remote{}, err
	}
	return *r.cfg, nil
}

// describe renders a remote for the log, with the password masked.
func describe(rc config.Remote) string {
	pw := ""
	if rc.Password != "" {
		pw = "***"
	}
	addr := rc.Addr
	if addr == "" {
		addr = "(passive)"
	}
	return fmt.Sprintf("%s line=%d addr=%s port=%d keepalive=%d password=%s strict_bcb=%v natin=%s natout=%s",
		rc.Name, rc.Line, addr, rc.Port, rc.Keepalive, pw, rc.StrictBCB, rc.NatIn, rc.NatOut)
}

// DefineLink adds a new remote at runtime and connects to it (if it has
// an address and the node is running).
func (n *Node) DefineLink(rc config.Remote) error {
	if err := config.ValidateRemote(&rc, n.localName()); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.remotes[rc.Name] != nil {
		return fmt.Errorf("link %s already exists (use link change)", rc.Name)
	}
	if !rc.LineSet {
		rc.Line = n.nextLineLocked()
	} else if other := n.lineOwnerLocked(rc.Line); other != "" {
		return fmt.Errorf("line %d is already used by %s", rc.Line, other)
	}
	rc.LineSet = true // a defined link keeps its number when saved
	cp := rc
	n.setLine(rc.Name, rc.Line, false)
	r := &remote{cfg: &cp, retryDelay: n.opts.RetryMin}
	n.remotes[rc.Name] = r
	n.order = append(n.order, rc.Name)
	n.cfg.Remotes = append(n.cfg.Remotes, &cp)
	n.log.Info("link define " + describe(rc))
	n.lmu.RLock()
	started := n.started
	n.lmu.RUnlock()
	if started && !n.stopping && rc.Addr != "" {
		n.scheduleLocked(r, randDuration(0, n.opts.StartJitter))
	}
	return nil
}

// ChangeLink updates an existing remote. The new values are used from the
// next connect on; an active session keeps running.
func (n *Node) ChangeLink(rc config.Remote) error {
	if err := config.ValidateRemote(&rc, n.localName()); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.lookup(rc.Name)
	if err != nil {
		return err
	}
	// Field by field: Name never changes, and goroutines read it
	// without the lock.
	r.cfg.Addr, r.cfg.Port, r.cfg.Keepalive = rc.Addr, rc.Port, rc.Keepalive
	if rc.Line != r.cfg.Line {
		if other := n.lineOwnerLocked(rc.Line); other != "" && other != r.cfg.Name {
			return fmt.Errorf("line %d is already used by %s", rc.Line, other)
		}
	}
	r.cfg.Password, r.cfg.StrictBCB = rc.Password, rc.StrictBCB
	r.cfg.Turn, r.cfg.MaxStreams, r.cfg.BufSize = rc.Turn, rc.MaxStreams, rc.BufSize
	r.cfg.NatIn, r.cfg.NatOut = rc.NatIn, rc.NatOut
	r.cfg.Line, r.cfg.LineSet = rc.Line, rc.LineSet
	n.setLine(r.cfg.Name, rc.Line, false)
	n.log.Info("link change " + describe(rc))
	return nil
}

// DeleteLink removes a remote, signing it off first if it is up. It
// returns how many queued files were discarded.
func (n *Node) DeleteLink(name string) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.lookup(name)
	if err != nil {
		return 0, err
	}
	// Routes through this link would point nowhere (and a saved config
	// would no longer load): the operator removes them first.
	var users []string
	for _, rt := range n.cfg.Routes {
		if rt.Via == r.cfg.Name || rt.Alt == r.cfg.Name {
			users = append(users, "route to "+rt.Dest)
		}
	}
	if n.cfg.DefaultRoute == r.cfg.Name {
		users = append(users, "the default route")
	}
	if len(users) > 0 {
		return 0, fmt.Errorf("link %s is used by %s: delete or change that first", r.cfg.Name, strings.Join(users, ", "))
	}
	// Forwarded files waiting on this link are held on disk, not lost.
	var held []*link.OutboundFile
	for _, f := range r.queue {
		if f.Spool != "" {
			held = append(held, f)
		}
	}
	// Local files waiting on this link are discarded: tell their senders.
	for _, f := range r.queue {
		if f.Done != nil && f.Spool == "" {
			defer f.Done(errors.New("link deleted"))
		}
	}
	if len(held) > 0 {
		// Runs before the deferred unlock (LIFO); holdForward does not
		// take n.mu.
		defer func() {
			for _, f := range held {
				n.forwardDone(r.cfg.Name, f, errors.New("link deleted"))
			}
		}()
	}
	r.held = true
	r.gen++ // stale attempts give up
	if r.retry != nil {
		r.retry.Stop()
	}
	if r.cancelDial != nil {
		r.cancelDial()
	}
	if r.sess != nil {
		r.sess.Close("link deleted")
	}
	delete(n.remotes, r.cfg.Name)
	n.setLine(r.cfg.Name, 0, true)
	for i, o := range n.order {
		if o == r.cfg.Name {
			n.order = append(n.order[:i], n.order[i+1:]...)
			break
		}
	}
	for i, c := range n.cfg.Remotes {
		if c.Name == r.cfg.Name {
			n.cfg.Remotes = append(n.cfg.Remotes[:i], n.cfg.Remotes[i+1:]...)
			break
		}
	}
	n.log.Info("link delete " + describe(*r.cfg))
	return len(r.queue), nil
}

func randDuration(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(rand.Int64N(int64(hi-lo)))
}

func (n *Node) lookup(name string) (*remote, error) {
	r := n.remotes[strings.ToUpper(name)]
	if r == nil {
		return nil, fmt.Errorf("no remote node %s in the configuration", strings.ToUpper(name))
	}
	return r, nil
}

// paramsLocked builds session parameters from the remote's current
// definition (call with n.mu held: link change may edit it).
func (n *Node) paramsLocked(r *remote) link.Params {
	return link.Params{
		Local: n.localName(), Remote: r.cfg.Name, Password: r.cfg.Password,
		Keepalive:    time.Duration(r.cfg.Keepalive) * time.Second,
		Handshake:    n.opts.Handshake,
		StrictBCB:    r.cfg.StrictBCB,
		StrictTurn:   r.cfg.Turn == "strict",
		MaxStreams:   r.cfg.MaxStreams, // 0 = link default 1
		StreamSettle: n.opts.StreamSettle,
		BufSize:      r.cfg.BufSize, // 0 = link default 8192
		NatOut:       net.ParseIP(r.cfg.NatOut),
		Log:          n.log.With("[" + r.cfg.Name + "]"),
		Handler:      &handler{n: n},
	}
}

// guard is deferred at the top of every node goroutine: a panic in
// protocol code is logged and reported instead of killing the process
// (which would leave the operator's terminal in raw mode). cleanup puts
// the link back into a sane state.
func (n *Node) guard(what string, cleanup func()) {
	if r := recover(); r != nil {
		n.log.Error(fmt.Sprintf("panic in %s: %v\n%s", what, r, debug.Stack()))
		n.emit(link.EvError, link.L2, "", fmt.Sprintf("internal error in %s: %v", what, r))
		if cleanup != nil {
			cleanup()
		}
	}
}

// dumpEvent shows a control record (OPEN/ACK/NAK) at L3; the log gets it
// separately with the connection details.
func (n *Node) dumpEvent(node, title string, raw []byte) {
	n.bus.Publish(link.Event{Time: time.Now(), Kind: link.EvDump, Level: link.L3, Node: node,
		Text: title, Lines: ebcdic.HexDump(raw)})
}

// ---------------------------------------------------------------------
// Outbound connections
// ---------------------------------------------------------------------

// scheduleLocked arranges a connect attempt after d.
func (n *Node) scheduleLocked(r *remote, d time.Duration) {
	if r.retry != nil {
		r.retry.Stop()
	}
	r.retry = time.AfterFunc(d, func() {
		defer n.guard("reconnect timer", nil)
		n.mu.Lock()
		defer n.mu.Unlock()
		n.dialLocked(r)
	})
}

// dialLocked starts an outbound attempt if the link is idle.
func (n *Node) dialLocked(r *remote) {
	if n.stopping || r.state != Down || r.held || r.cfg.Addr == "" {
		return
	}
	if n.opts.OnlyRemote != "" && r.cfg.Name != n.opts.OnlyRemote {
		return
	}
	r.state = Connecting
	r.gen++
	r.dialStart = time.Now()
	ctx, cancel := context.WithCancel(n.ctx)
	r.cancelDial = cancel
	n.wg.Add(1)
	addr := net.JoinHostPort(r.cfg.Addr, strconv.Itoa(r.cfg.Port))
	p := n.paramsLocked(r)
	p.Handshake = n.opts.DialHandshake
	d := net.Dialer{Timeout: 10 * time.Second}
	if r.cfg.NatIn != "" {
		// Behind NAT: dial from our inside address for this link.
		d.LocalAddr = &net.TCPAddr{IP: net.ParseIP(r.cfg.NatIn)}
	}
	go n.dialWorker(r, r.gen, addr, d, p, ctx, cancel)
}

// dialWorker connects, exchanges OPEN/ACK and signs on as primary.
// cancel is always called on return so the attempt's context does not
// stay registered on the node context (review C2); after a successful
// signon the socket is already detached from it.
func (n *Node) dialWorker(r *remote, gen int, addr string, d net.Dialer, p link.Params, ctx context.Context, cancel context.CancelFunc) {
	defer n.wg.Done()
	defer cancel()
	defer n.guard("connect", func() { n.attemptFailed(r, gen, errors.New("internal error"), 0) })
	n.emit(link.EvLink, link.L2, r.cfg.Name, "connecting to "+addr)
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		n.attemptFailed(r, gen, err, 0)
		return
	}
	// Cancelling the attempt (connect collision, drop, shutdown) closes
	// the socket, which aborts any handshake read in progress.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	if err := link.ClientOpen(conn, p); err != nil {
		_ = conn.Close()
		var nak *link.NAKError
		if errors.As(err, &nak) {
			if !tcpip.NAKRetryable(nak.Reason) {
				n.mu.Lock()
				r.held = true
				n.mu.Unlock()
			}
			n.attemptFailed(r, gen, fmt.Errorf("%w (%s)", err, tcpip.NAKReasonText(nak.Reason)),
				randDuration(n.opts.NAKMin, n.opts.NAKMax))
			return
		}
		n.attemptFailed(r, gen, err, 0)
		return
	}
	s := link.New(conn, p)
	if err := s.SignonPrimary(); err != nil {
		_ = conn.Close()
		n.attemptFailed(r, gen, err, 0)
		return
	}
	if !stop() {
		n.attemptFailed(r, gen, errors.New("attempt cancelled"), 0)
		return
	}
	n.established(r, gen, s)
}

// attemptFailed records a failed connect/accept and schedules a retry.
// retryIn = 0 uses the exponential backoff.
func (n *Node) attemptFailed(r *remote, gen int, err error, retryIn time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if gen != r.gen {
		return // superseded by a newer attempt
	}
	r.state = Down
	r.lastErr = err.Error()
	r.cancelDial = nil
	r.failures++
	if n.stopping {
		return
	}
	msg := "connect failed: " + err.Error()
	// General "timeout": stop retrying a remote we could not reach for
	// that long; inbound calls are still accepted, "connect" retries.
	if r.tryStart.IsZero() {
		r.tryStart = time.Now()
	}
	if t := n.cfg.Timeout; t > 0 && !r.held && time.Since(r.tryStart) >= time.Duration(t)*time.Second {
		r.held = true
		r.lastErr = fmt.Sprintf("gave up after %d s, use connect", t)
		n.log.Warn(r.cfg.Name + ": " + r.lastErr)
		n.emit(link.EvLink, link.L2, r.cfg.Name, msg+"; "+r.lastErr)
		return
	}
	if !r.held && r.cfg.Addr != "" {
		if retryIn == 0 {
			retryIn = n.backoffLocked(r)
		}
		n.scheduleLocked(r, retryIn)
		msg += fmt.Sprintf(", retry in %.1fs", retryIn.Seconds())
	}
	n.emit(link.EvLink, link.L2, r.cfg.Name, msg)
}

// backoffLocked returns the next reconnect delay (current delay plus up
// to 25% jitter, never above RetryMax) and doubles the delay.
func (n *Node) backoffLocked(r *remote) time.Duration {
	d := min(randDuration(r.retryDelay, r.retryDelay+r.retryDelay/4), n.opts.RetryMax)
	r.retryDelay = min(r.retryDelay*2, n.opts.RetryMax)
	return d
}

// established makes a signed-on session the active link.
func (n *Node) established(r *remote, gen int, s *link.Session) {
	n.mu.Lock()
	if gen != r.gen || n.stopping {
		n.mu.Unlock()
		s.Kill()
		return
	}
	// The backoff is reset only once the link has stayed up for a while
	// (runSession), so a peer that accepts and drops at once is not
	// redialled every few seconds forever.
	r.state, r.sess, r.since, r.lastErr = Up, s, time.Now(), ""
	r.tryStart = time.Time{}
	r.cancelDial = nil
	r.connects++
	r.connStart, r.base = r.since, link.Stats{}
	if r.retry != nil {
		r.retry.Stop()
	}
	role := "secondary"
	if s.Stats().Primary {
		role = "primary"
	}
	queued := len(r.queue)
	n.mu.Unlock()
	n.emit(link.EvLink, link.L2, r.cfg.Name, "link up ("+role+")")
	if queued > 0 {
		s.Kick()
	}
	n.wg.Add(1)
	go n.runSession(r, s)
}

// runSession runs a session to completion and handles reconnection.
func (n *Node) runSession(r *remote, s *link.Session) {
	defer n.wg.Done()
	defer n.guard("session", func() {
		s.Kill()
		n.mu.Lock()
		if r.sess == s {
			r.sess, r.state = nil, Down
		}
		n.mu.Unlock()
	})
	err := s.Run()
	n.mu.Lock()
	defer n.mu.Unlock()
	r.draining = false
	if r.sess == s {
		r.sess, r.state = nil, Down
		r.lastErr = err.Error()
		r.totals = r.totals.Add(s.Stats().Sub(r.base))
		r.base = link.Stats{}
		r.connected += time.Since(r.connStart)
		if time.Since(r.since) >= n.opts.StableAfter {
			r.retryDelay = n.opts.RetryMin
		}
	}
	n.emit(link.EvLink, link.L2, r.cfg.Name, "link down: "+err.Error())
	if r.bounce {
		// Our own signoff after a local rename: reconnect right away.
		r.bounce = false
		if !n.stopping && !r.held && r.cfg.Addr != "" && r.state == Down && n.remotes[r.cfg.Name] == r {
			n.scheduleLocked(r, randDuration(0, n.opts.StartJitter))
			n.emit(link.EvLink, link.L2, r.cfg.Name, "reconnecting under the new local name")
		}
		return
	}
	// Reconnect after unexpected loss. An orderly signoff by the peer means
	// its operator wants the link down: wait for it (or "connect").
	if !n.stopping && link.IsReconnectable(err) && !r.held && r.cfg.Addr != "" && r.state == Down {
		d := n.backoffLocked(r)
		n.scheduleLocked(r, d)
		n.emit(link.EvLink, link.L2, r.cfg.Name, fmt.Sprintf("reconnect in %.1fs", d.Seconds()))
	}
}

// ---------------------------------------------------------------------
// Inbound connections
// ---------------------------------------------------------------------

func (n *Node) acceptLoop(ln net.Listener) {
	defer n.wg.Done()
	defer n.guard("accept loop", nil)
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // shut down, or moved by SetListen
			}
			select {
			case <-n.ctx.Done():
				return
			default:
			}
			n.log.Warn("accept: " + err.Error())
			time.Sleep(100 * time.Millisecond)
			continue
		}
		n.wg.Add(1)
		go n.handleInbound(c)
	}
}

// handleInbound validates an OPEN and, if acceptable, signs on as
// secondary (spec 01 section 3.1).
func (n *Node) handleInbound(c net.Conn) {
	defer n.wg.Done()
	defer n.guard("inbound connection", func() { _ = c.Close() })
	// Shutdown must not wait for a caller that connects and stalls:
	// cancelling the node context closes the socket (review C1).
	stop := context.AfterFunc(n.ctx, func() { _ = c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(n.opts.Handshake))
	open, raw, err := tcpip.ReadControl(c)
	if raw != nil {
		n.log.Dump(logx.Debug, "<- control record from "+c.RemoteAddr().String(), raw)
		n.dumpEvent("", "<- control record from "+c.RemoteAddr().String(), raw)
	}
	if err != nil || open.Type != tcpip.TypeOpen {
		// Malformed (or IPv6/SSL form we do not support): close, no NAK.
		n.log.Warn(fmt.Sprintf("inbound connection from %s: bad control record (%v)", c.RemoteAddr(), err))
		_ = c.Close()
		return
	}
	n.log.Info(fmt.Sprintf("<- %s from %s", open, c.RemoteAddr()))
	local := n.localName()
	reply := &tcpip.Control{Type: tcpip.TypeNAK, RHost: local, RIP: localIP(c),
		OHost: open.RHost, OIP: open.RIP}
	nak := func(reason byte, why string) {
		reply.Reason = reason
		n.mu.Lock()
		n.rejected++
		n.mu.Unlock()
		_, _ = c.Write(reply.Marshal())
		n.log.Dump(logx.Debug, "-> "+reply.String(), reply.Marshal())
		n.dumpEvent(open.RHost, "-> "+reply.String(), reply.Marshal())
		n.emit(link.EvLink, link.L2, open.RHost, "inbound OPEN rejected: "+why)
		_ = c.Close()
	}
	n.mu.Lock()
	r := n.remotes[strings.ToUpper(open.RHost)]
	n.mu.Unlock()
	if r != nil && n.opts.OnlyRemote != "" && r.cfg.Name != n.opts.OnlyRemote {
		r = nil // batch mode: only the target node may call in
	}
	if r == nil || !strings.EqualFold(open.OHost, local) {
		nak(tcpip.NAKNoSuchLink, "unknown link "+open.RHost+" -> "+open.OHost)
		return
	}
	n.mu.Lock()
	natIn, natOut := r.cfg.NatIn, r.cfg.NatOut
	n.mu.Unlock()
	if natOut != "" {
		reply.RIP = net.ParseIP(natOut) // behind NAT: our public address
	}
	// OIP is not enforced (never was: NAT and multi-homed hosts make it
	// unreliable). It is logged when it is none of our known addresses.
	if oip := open.RIP; open.OIP != nil && !open.OIP.IsUnspecified() {
		known := open.OIP.Equal(localIP(c)) || (natOut != "" && open.OIP.Equal(net.ParseIP(natOut))) ||
			(natIn != "" && open.OIP.Equal(net.ParseIP(natIn)))
		if !known {
			n.log.Warn(fmt.Sprintf("NONSTANDARD OPEN from %s (%s) addressed to OIP %s, which is not one of our addresses",
				open.RHost, oip, open.OIP))
		}
	}

	n.mu.Lock()
	if n.stopping {
		n.mu.Unlock()
		nak(tcpip.NAKTemporary, "shutting down")
		return
	}
	switch r.state {
	case Up:
		// We think the link is up, so the old connection must be dead
		// (silent severance): NAK 02 and drop ours; the peer retries.
		old := r.sess
		n.mu.Unlock()
		nak(tcpip.NAKActive, "link already active; dropping the old connection")
		old.Kill()
		return
	case Accepting:
		n.mu.Unlock()
		nak(tcpip.NAKTemporary, "signon already in progress")
		return
	case Connecting:
		// Connect collision: both sides dialled at once. The manual
		// answers NAK 03 and lets both retry after a random delay. To
		// converge at once between two gonje nodes we break the tie
		// deterministically: the connection initiated by the node with
		// the HIGHER name wins. So if the caller's name is higher we drop
		// our own attempt and accept; otherwise we NAK 03 (and the peer,
		// applying the same rule, accepts ours).
		//
		// Two exceptions accept the caller anyway: batch mode
		// (PreferInbound), and an own attempt that has been hanging for
		// more than collisionGrace. A real gonje-gonje race is over in
		// milliseconds; an attempt stuck that long is one the peer will
		// never complete (HOUCICS ACKs our OPEN, then waits to call us).
		stuck := time.Since(r.dialStart) > collisionGrace
		if r.cfg.Name <= local && !n.opts.PreferInbound && !stuck {
			n.mu.Unlock()
			nak(tcpip.NAKCollision, "connect collision, keeping our own connection")
			return
		}
		if r.cancelDial != nil {
			r.cancelDial()
		}
		n.log.Info("connect collision with " + r.cfg.Name + ": accepting its connection")
	}
	r.state = Accepting
	r.gen++
	gen := r.gen
	params := n.paramsLocked(r)
	if r.retry != nil {
		r.retry.Stop()
	}
	n.mu.Unlock()

	reply.Type = tcpip.TypeACK
	b := reply.Marshal()
	n.log.Dump(logx.Debug, "-> "+reply.String(), b)
	n.dumpEvent(r.cfg.Name, "-> "+reply.String(), b)
	if _, err := c.Write(b); err != nil {
		_ = c.Close()
		n.attemptFailed(r, gen, err, 0)
		return
	}
	n.mu.Lock()
	n.accepted++
	n.mu.Unlock()
	n.emit(link.EvLink, link.L2, r.cfg.Name, "inbound connection from "+c.RemoteAddr().String())
	_ = c.SetDeadline(time.Time{})
	s := link.New(c, params)
	if err := s.SignonSecondary(); err != nil {
		_ = c.Close()
		n.attemptFailed(r, gen, err, 0)
		return
	}
	n.established(r, gen, s)
}

func localIP(c net.Conn) net.IP {
	if a, ok := c.LocalAddr().(*net.TCPAddr); ok {
		return a.IP
	}
	return nil
}

// ---------------------------------------------------------------------
// Operator actions
// ---------------------------------------------------------------------

// Connect starts a connection to a remote now.
func (n *Node) Connect(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.lookup(name)
	if err != nil {
		return err
	}
	if r.cfg.Addr == "" {
		return fmt.Errorf("%s has no remote_host_addr; waiting for it to connect", r.cfg.Name)
	}
	if r.state == Up && r.draining {
		// connect while draining cancels the drain. Session.Drain waits
		// for the session goroutine, which may need n.mu: unlock first.
		r.draining, r.held = false, false
		s := r.sess
		n.mu.Unlock()
		s.Drain(false)
		n.mu.Lock()
		n.log.Info("drain of " + r.cfg.Name + " cancelled")
		n.emit(link.EvLink, link.L2, r.cfg.Name, "drain cancelled, link stays up")
		return nil
	}
	if r.state != Down {
		return fmt.Errorf("%s is already %s", r.cfg.Name, r.state)
	}
	r.held = false
	r.tryStart = time.Time{} // a fresh timeout window
	r.retryDelay = n.opts.RetryMin
	if r.retry != nil {
		r.retry.Stop()
	}
	n.dialLocked(r)
	return nil
}

// bufOrDefault is the configured buffer size or the link default.
func bufOrDefault(n int) int {
	if n == 0 {
		return link.DefaultBufSize
	}
	return n
}

// nextLineLocked is the lowest LINE number not in use.
func (n *Node) nextLineLocked() int {
	for l := 0; ; l++ {
		if n.lineOwnerLocked(l) == "" {
			return l
		}
	}
}

// lineOwnerLocked names the remote using LINE l ("" = free).
func (n *Node) lineOwnerLocked(l int) string {
	for _, r := range n.remotes {
		if r.cfg.Line == l {
			return r.cfg.Name
		}
	}
	return ""
}

// visibleState is the state shown to the operator (Up while draining is
// reported as Draining).
func (r *remote) visibleState() State {
	if r.state == Up && r.draining {
		return Draining
	}
	return r.state
}

// Drain drops a link gracefully: transfers in progress finish, no new
// ones start (queued files stay queued, the peer's requests are
// refused), then the link signs off like drop (no auto-reconnect).
// Draining again forces the drop at once. It returns a progress text.
func (n *Node) Drain(name string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.lookup(name)
	if err != nil {
		return "", err
	}
	if r.state != Up {
		return "", fmt.Errorf("%s is not connected (%s)", r.cfg.Name, r.state)
	}
	if r.draining {
		r.sess.Close("forced drop while draining")
		n.log.Info("drain of " + r.cfg.Name + " forced: dropping now")
		return r.cfg.Name + ": drain forced, dropping now", nil
	}
	r.draining, r.held = true, true
	if r.retry != nil {
		r.retry.Stop()
	}
	s, queued := r.sess, len(r.queue)
	// Session.Drain waits until the session applied it (so no request
	// slips in afterwards); the session goroutine may need n.mu.
	n.mu.Unlock()
	s.Drain(true)
	st := s.Stats()
	n.mu.Lock()
	msg := fmt.Sprintf("%s draining: waiting for %d inbound, %d outbound transfer(s)", r.cfg.Name, st.ActiveIn, st.ActiveOut)
	if st.ActiveIn+st.ActiveOut == 0 {
		msg = r.cfg.Name + " draining: no transfer in progress, signing off"
	}
	if queued > 0 {
		msg += fmt.Sprintf("; %d queued file(s) stay queued for the next connect", queued)
	}
	n.log.Info(msg)
	n.emit(link.EvLink, link.L2, r.cfg.Name, msg)
	return msg, nil
}

// Drop signs off a link (or cancels an attempt) and stops reconnecting.
func (n *Node) Drop(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.lookup(name)
	if err != nil {
		return err
	}
	r.held = true
	if r.retry != nil {
		r.retry.Stop()
	}
	switch r.state {
	case Up:
		r.sess.Close("operator drop")
	case Connecting:
		r.gen++
		r.state = Down
		if r.cancelDial != nil {
			r.cancelDial()
		}
	case Accepting:
		r.gen++
		r.state = Down
	default:
		return fmt.Errorf("%s is not connected (automatic reconnect stopped)", r.cfg.Name)
	}
	return nil
}

// Kill breaks a link's TCP connection without signoff (for testing the
// reconnect logic).
func (n *Node) Kill(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.lookup(name)
	if err != nil {
		return err
	}
	if r.sess == nil {
		return fmt.Errorf("%s is not connected", r.cfg.Name)
	}
	r.sess.Kill()
	return nil
}

// session returns the active session to a remote.
func (n *Node) session(name string) (*remote, *link.Session, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.lookup(name)
	if err != nil {
		return nil, nil, err
	}
	if r.state != Up {
		return r, nil, fmt.Errorf("link to %s is %s", r.cfg.Name, r.state)
	}
	return r, r.sess, nil
}

// SendMessage sends a message to user at node ("*" = operator console).
func (n *Node) SendMessage(node, user, text string) error {
	return n.SendMessageNotify(node, user, text, nil)
}

// SendMessageNotify is SendMessage with a callback: done gets nil once
// every NMR of the message has been written to the link, or the error
// that stopped it (called at most once, never under node locks; it must
// not block).
func (n *Node) SendMessageNotify(node, user, text string, done func(error)) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("empty message")
	}
	// Addressed to the final node; sent over its link or route.
	dest := strings.ToUpper(node)
	r, s, err := n.routeSession(dest, "")
	if err != nil {
		return err
	}
	msgs := records.NewMessages(n.localName(), n.opts.User, dest, strings.ToUpper(user), text)
	cb := joinDone(len(msgs), done)
	for _, m := range msgs {
		if err := s.SendNMRNotify(m, cb); err != nil {
			return err
		}
	}
	n.emit(link.EvMsgOut, link.L2, r.cfg.Name, fmt.Sprintf("to %s/%s%s: %s", dest, strings.ToUpper(user), via(r, dest), text))
	return nil
}

// SendCommand sends an unformatted command to node.
func (n *Node) SendCommand(node, cmd string) error { return n.SendCommandNotify(node, cmd, nil) }

// SendCommandNotify is SendCommand with a write callback (see
// SendMessageNotify).
func (n *Node) SendCommandNotify(node, cmd string, done func(error)) error {
	dest := strings.ToUpper(node)
	r, s, err := n.routeSession(dest, "")
	if err != nil {
		return err
	}
	m, err := records.NewCommand(n.localName(), n.opts.User, dest, cmd)
	if err != nil {
		return err
	}
	if err := s.SendNMRNotify(m, joinDone(1, done)); err != nil {
		return err
	}
	n.mu.Lock()
	r.lastCmd = time.Now()
	r.pendingCmds++
	n.mu.Unlock()
	n.emit(link.EvCmdOut, link.L2, r.cfg.Name, "command for "+dest+via(r, dest)+": "+cmd)
	return nil
}

// SendFile queues a file for user at node. It is sent as soon as the
// link is up. path is tried as given, then relative to send_dir.
func (n *Node) SendFile(node, user, path string) (*link.OutboundFile, error) {
	return n.SendFileNotify(node, user, path, nil)
}

// SendFileNotify is SendFile with a completion callback (see
// link.OutboundFile.Done).
func (n *Node) SendFileNotify(node, user, path string, done func(error)) (*link.OutboundFile, error) {
	// Files for a routed node queue on the via link even if it is down.
	dest := strings.ToUpper(node)
	n.mu.Lock()
	r, _, err := n.linkForLocked(dest, "")
	n.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		alt := strings.TrimRight(n.cfg.SendDir, "/") + "/" + path
		if _, err2 := os.Stat(alt); err2 == nil {
			path = alt
		}
	}
	f, err := PrepareFile(path, dest, strings.ToUpper(user), n.opts.User)
	if err != nil {
		return nil, err
	}
	f.Done = joinDone(1, done)
	n.mu.Lock()
	n.jobID = n.jobID%9999 + 1
	n.fileID++
	f.JobID, f.ID = uint16(n.jobID), n.fileID
	r.queue = append(r.queue, f)
	s, up := r.sess, r.state == Up
	n.mu.Unlock()
	how := "queued"
	if up {
		s.Kick()
	} else {
		how += " (link down, will send when it is up)"
	}
	kind := "text"
	if f.Binary {
		kind = "binary"
	}
	n.emit(link.EvFileOut, link.L2, r.cfg.Name, fmt.Sprintf("%s for %s/%s%s %s, %d records, %s",
		f.Name, dest, f.ToUser, via(r, dest), how, len(f.Records), kind))
	return f, nil
}

// LinkStatus describes one remote for "nodes" and Q LINKS.
type LinkStatus struct {
	Line       int    // LINE number
	NatIn      string // NAT inside address ("" = none)
	NatOut     string // NAT outside address ("" = none)
	BufSize    int    // buffer size we advertise
	MaxStreams int    // concurrent transfers each way
	Name       string
	Addr       string
	State      State
	Since      time.Time
	Held       bool
	Queued     int
	LastErr    string
	Stats      link.Stats
}

// Status returns the state of every remote, in configuration order.
func (n *Node) Status() []LinkStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []LinkStatus
	for _, name := range n.order {
		r := n.remotes[name]
		st := LinkStatus{Name: name, Line: r.cfg.Line, NatIn: r.cfg.NatIn, NatOut: r.cfg.NatOut, BufSize: bufOrDefault(r.cfg.BufSize), MaxStreams: max(r.cfg.MaxStreams, 1), State: r.visibleState(), Since: r.since, Held: r.held,
			Queued: len(r.queue), LastErr: r.lastErr}
		if r.cfg.Addr != "" {
			st.Addr = net.JoinHostPort(r.cfg.Addr, strconv.Itoa(r.cfg.Port))
		}
		if r.sess != nil {
			st.Stats = r.sess.Stats()
		}
		out = append(out, st)
	}
	return out
}

// ---------------------------------------------------------------------
// link.Handler implementation
// ---------------------------------------------------------------------

// handler adapts Node to link.Handler without exporting the methods.
type handler struct{ n *Node }

func (h *handler) Event(ev link.Event) { h.n.bus.Publish(ev) }

func (h *handler) NextFile(remote string, busy []*link.OutboundFile) *link.OutboundFile {
	h.n.mu.Lock()
	defer h.n.mu.Unlock()
	r := h.n.remotes[remote]
	if r == nil {
		return nil
	}
	// First queued file not already being sent on another stream.
	for _, f := range r.queue {
		inFlight := false
		for _, b := range busy {
			inFlight = inFlight || b == f
		}
		if !inFlight {
			return f
		}
	}
	return nil
}

func (h *handler) FileDone(remote string, f *link.OutboundFile, err error) {
	defer h.n.forwardDone(remote, f, err) // after the unlock: file I/O
	if f.Done != nil {
		defer f.Done(err)
	}
	h.n.mu.Lock()
	defer h.n.mu.Unlock()
	r := h.n.remotes[remote]
	if r == nil {
		return
	}
	for i, q := range r.queue {
		if q == f {
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			break
		}
	}
}

func (h *handler) Dataset(remote string, ds *link.InboundDataset) {
	n := h.n
	if dest := datasetDest(ds); dest != "" && !strings.EqualFold(dest, n.localName()) {
		n.forwardDataset(remote, ds, strings.ToUpper(dest)) // store-and-forward
		return
	}
	path, text, warn, err := SaveDatasetInfo(n.cfg.RecvDir, remote, ds, time.Now())
	if warn != "" {
		n.log.Warn(warn)
		n.emit(link.EvError, link.L2, remote, warn)
	}
	if err != nil {
		n.emit(link.EvError, link.L2, remote, "cannot save received file: "+err.Error())
		return
	}
	from := remote
	if ds.Job != nil && ds.Job.OrigNode != "" {
		from = clean(ds.Job.OrigNode + "/" + ds.Job.OrigUser)
	}
	kind := "binary"
	if text {
		kind = "text"
	}
	n.emit(link.EvFileIn, link.L1, remote, fmt.Sprintf("from %s: %s (%d records, %s)",
		from, path, len(ds.Records), kind))
}

func (h *handler) NMR(remote string, m *records.NMR) {
	n := h.n
	if !strings.EqualFold(m.ToNode, n.localName()) {
		n.forwardNMR(remote, m) // store-and-forward
		return
	}
	who := clean(m.FromNode)
	if u := m.Sender(); u != "" {
		who += "/" + clean(u)
	}
	text := clean(m.Text())
	if !m.IsCommand() {
		// gonje has no local users: every message for our node is shown
		// and logged, whatever user name it is addressed to.
		to := " to " + clean(m.OutUser())
		if m.OutUser() == "" {
			to = " to (operator)"
		}
		// Level X'3x' is what RSCS uses for command responses, but JES2
		// also uses it for normal messages; so a message only counts as a
		// response if we sent that node a command recently (review N7).
		k := link.EvMsgIn
		n.mu.Lock()
		recent := false
		if r := n.remotes[remote]; r != nil {
			recent = time.Since(r.lastCmd) < responseWindow
			r.pendingCmds = 0 // an answer has started to arrive
		}
		n.mu.Unlock()
		// A message from a node we sent a command to (within the
		// window) counts as a response if it has the RSCS response level
		// or is addressed to the user who issued the command: HOUCICS
		// answers "D T" at another level, to MOSHE.
		// Operator/no-user messages (e.g. HOUCICS's "Signon of link ...
		// complete") are never responses.
		toUs := m.OutUser() != "" && strings.EqualFold(m.OutUser(), n.opts.User)
		if recent && toUs {
			k = link.EvRspIn
			n.mu.Lock()
			if r := n.remotes[remote]; r != nil {
				r.rspIn++
			}
			n.mu.Unlock()
		}
		// L1: a person (or a command) sent it, so it is always shown.
		n.emit(k, link.L1, remote, fmt.Sprintf("from %s%s: %s", who, to, text))
		return
	}
	n.emit(link.EvCmdIn, link.L1, remote, fmt.Sprintf("from %s: %s", who, text))
	// Responses go back to the command's origin node: over its own link
	// or route, else over the link the command arrived on.
	_, s, err := n.routeSession(m.FromNode, "")
	if err != nil {
		if _, s, err = n.session(remote); err != nil {
			return
		}
	}
	// SendNMR only queues (it never blocks), so it is safe to call here
	// on the session goroutine; the responses go out on our next turns.
	for _, l := range n.answer(m) {
		if s.SendNMR(records.NewResponse(m, n.localName(), l)) != nil {
			return
		}
		n.mu.Lock()
		if r := n.remotes[remote]; r != nil {
			r.rspOut++
		}
		n.mu.Unlock()
		n.emit(link.EvRspOut, link.L2, remote, "response: "+l)
	}
}

// collisionGrace: an own dial older than this yields to an inbound call.
const collisionGrace = 2 * time.Second

// responseWindow is how long after sending a command an incoming
// level-3 message is shown as its response.
const responseWindow = 5 * time.Minute

// clean replaces control characters in text that came from a remote
// node (EBCDIC X'25' decodes to LF, X'27' to ESC, ...) with '.', so it
// can neither break lines nor drive a terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7F && r < 0xA0) {
			return '.'
		}
		return r
	}, s)
}

// joinDone returns a callback to hand to k sends that calls done once:
// with the first error, or with nil when all k succeeded. nil done gives
// nil.
func joinDone(k int, done func(error)) func(error) {
	if done == nil {
		return nil
	}
	var mu sync.Mutex
	left, fired := k, false
	return func(err error) {
		mu.Lock()
		left--
		fire := !fired && (err != nil || left <= 0)
		if fire {
			fired = true
		}
		mu.Unlock()
		if fire {
			done(err)
		}
	}
}
