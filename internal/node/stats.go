// Copyright 2026 by moshix

package node

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"gonje/internal/nje/link"
)

// LinkCounters are the "stats" figures of one link. Totals cover every
// session since start (or the last reset), including the live one.
type LinkCounters struct {
	Name         string
	Addr         string
	State        State
	Held         bool
	LastErr      string
	SessionSince time.Time     // start of the live session (zero if down)
	Connected    time.Duration // total connected time
	Connects     int
	Failures     int
	Total        link.Stats
	RspsIn       int
	RspsOut      int
	BufSize      int // buffer size we advertise
	MaxStreams   int // concurrent transfers each way
}

// Counters is the node-wide "stats" snapshot.
type Counters struct {
	Start    time.Time // process start
	Since    time.Time // counters since (start or last global reset)
	Accepted int       // inbound connections accepted
	Rejected int       // inbound OPENs answered with NAK
	Links    []LinkCounters
}

// Counters returns a consistent snapshot of every counter.
func (n *Node) Counters() Counters {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := Counters{Start: n.start, Since: n.since, Accepted: n.accepted, Rejected: n.rejected}
	now := time.Now()
	for _, name := range n.order {
		r := n.remotes[name]
		lc := LinkCounters{Name: name, State: r.visibleState(), Held: r.held, LastErr: r.lastErr,
			Connected: r.connected, Connects: r.connects, Failures: r.failures,
			Total: r.totals, RspsIn: r.rspIn, RspsOut: r.rspOut, BufSize: bufOrDefault(r.cfg.BufSize),
			MaxStreams: max(r.cfg.MaxStreams, 1)}
		if r.cfg.Addr != "" {
			lc.Addr = net.JoinHostPort(r.cfg.Addr, strconv.Itoa(r.cfg.Port))
		}
		if r.sess != nil {
			live := r.sess.Stats()
			lc.SessionSince = r.since
			lc.Connected += now.Sub(r.connStart)
			lc.Total = lc.Total.Add(live.Sub(r.base))
			lc.Total.LastActivity, lc.Total.IdleDelay = live.LastActivity, live.IdleDelay
			lc.Total.Primary, lc.Total.PeerBufSize = live.Primary, live.PeerBufSize
		}
		c.Links = append(c.Links, lc)
	}
	return c
}

// ResetCounters zeroes the counters of one link, or of everything when
// name is "". A live session keeps running; its counts restart from now.
func (n *Node) ResetCounters(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	var targets []*remote
	if name == "" {
		n.accepted, n.rejected, n.since = 0, 0, time.Now()
		for _, r := range n.remotes {
			targets = append(targets, r)
		}
	} else {
		r, err := n.lookup(name)
		if err != nil {
			return err
		}
		targets = append(targets, r)
	}
	now := time.Now()
	for _, r := range targets {
		r.totals, r.connects, r.failures, r.connected, r.rspIn, r.rspOut = link.Stats{}, 0, 0, 0, 0, 0
		r.base = link.Stats{}
		if r.sess != nil {
			r.base = r.sess.Stats()
			r.connStart = now
		}
	}
	what := "all links"
	if name != "" {
		what = strings.ToUpper(name)
	}
	n.log.Info(fmt.Sprintf("stats reset for %s", what))
	return nil
}
