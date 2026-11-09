// Copyright 2026 by moshix

package node

import (
	"fmt"
	"strings"
	"time"

	"gonje/internal/config"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
)

// maxHops drops forwarded files that have passed more nodes than this
// (routing loop protection, NJHGHOPS).
const maxHops = 16

// RouteStatus is one route for "route list".
type RouteStatus struct {
	config.Route
	Default bool   // the default route ("*")
	Using   string // link currently used ("" = none up)
}

// pick returns which of via/alt is usable (via if up, else alt if up).
func (n *Node) pickLocked(via, alt string) (*remote, bool) {
	v := n.remotes[via]
	if v != nil && v.state == Up {
		return v, true
	}
	if a := n.remotes[alt]; a != nil && a.state == Up {
		return a, true
	}
	return v, false // not up: files may still queue on via
}

// linkForLocked chooses the link that reaches node dest. Precedence:
// direct link > explicit route (via, else alt when via is down) >
// default route > error. It returns the link and whether it is up.
// exclude is the link the traffic arrived on: forwarding back over it
// would only bounce it, so that is an error.
func (n *Node) linkForLocked(dest, exclude string) (*remote, bool, error) {
	dest = strings.ToUpper(dest)
	var r *remote
	up := false
	if d := n.remotes[dest]; d != nil {
		r, up = d, d.state == Up
	} else if rt, ok := n.findRouteLocked(dest); ok {
		r, up = n.pickLocked(rt.Via, rt.Alt)
	} else if n.cfg.DefaultRoute != "" {
		d := n.remotes[n.cfg.DefaultRoute]
		if d == nil {
			return nil, false, fmt.Errorf("default route %s is not a defined link", n.cfg.DefaultRoute)
		}
		r, up = d, d.state == Up
	} else {
		return nil, false, fmt.Errorf("no link or route to %s", dest)
	}
	if r == nil {
		return nil, false, fmt.Errorf("route to %s uses an undefined link", dest)
	}
	if exclude != "" && r.cfg.Name == exclude {
		return nil, false, fmt.Errorf("route to %s leads back to %s (not forwarded)", dest, exclude)
	}
	return r, up, nil
}

func (n *Node) findRouteLocked(dest string) (config.Route, bool) {
	for _, rt := range n.cfg.Routes {
		if rt.Dest == dest {
			return rt, true
		}
	}
	return config.Route{}, false
}

// routeSession returns an up session that reaches dest.
func (n *Node) routeSession(dest, exclude string) (*remote, *link.Session, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, up, err := n.linkForLocked(dest, exclude)
	if err != nil {
		return nil, nil, err
	}
	if !up {
		if rt, ok := n.findRouteLocked(strings.ToUpper(dest)); ok && rt.Alt != "" {
			return r, nil, fmt.Errorf("neither %s nor %s (route to %s) is up", rt.Via, rt.Alt, strings.ToUpper(dest))
		}
		return r, nil, fmt.Errorf("link to %s is %s", r.cfg.Name, r.visibleState())
	}
	return r, r.sess, nil
}

// via describes the path for event texts: "" for a direct link.
func via(r *remote, dest string) string {
	if r.cfg.Name == strings.ToUpper(dest) {
		return ""
	}
	return " via " + r.cfg.Name
}

// Routes lists the routes and the default route with the link in use.
func (n *Node) Routes() []RouteStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []RouteStatus
	for _, rt := range n.cfg.Routes {
		st := RouteStatus{Route: rt}
		if r, up := n.pickLocked(rt.Via, rt.Alt); up {
			st.Using = r.cfg.Name
		}
		out = append(out, st)
	}
	if d := n.cfg.DefaultRoute; d != "" {
		st := RouteStatus{Route: config.Route{Dest: "*", Via: d}, Default: true}
		if r := n.remotes[d]; r != nil && r.state == Up {
			st.Using = d
		}
		out = append(out, st)
	}
	return out
}

// SetRoute adds or replaces the route to rt.Dest (validated like the
// config loader).
func (n *Node) SetRoute(rt config.Route) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	view := &config.Config{LocalNode: n.localName(), Remotes: n.cfg.Remotes}
	if err := config.ValidateRoute(rt, view); err != nil {
		return err
	}
	for i, old := range n.cfg.Routes {
		if old.Dest == rt.Dest {
			n.cfg.Routes[i] = rt
			n.log.Info("route changed: " + rt.String())
			return nil
		}
	}
	n.cfg.Routes = append(n.cfg.Routes, rt)
	n.log.Info("route added: " + rt.String())
	return nil
}

// DeleteRoute removes the route to dest.
func (n *Node) DeleteRoute(dest string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	dest = strings.ToUpper(dest)
	for i, rt := range n.cfg.Routes {
		if rt.Dest == dest {
			n.cfg.Routes = append(n.cfg.Routes[:i], n.cfg.Routes[i+1:]...)
			n.log.Info("route deleted: " + rt.String())
			return nil
		}
	}
	return fmt.Errorf("no route to %s", dest)
}

// SetDefaultRoute sets the default route ("" removes it); the node must
// be a defined link.
func (n *Node) SetDefaultRoute(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	name = strings.ToUpper(name)
	if name != "" && n.remotes[name] == nil {
		return fmt.Errorf("default route %s is not a configured remote node", name)
	}
	n.cfg.DefaultRoute = name
	if name == "" {
		n.log.Info("default route removed")
	} else {
		n.log.Info("default route set to " + name)
	}
	return nil
}

// forwardNMR passes an NMR for another node on (store-and-forward),
// unchanged so its origin stays intact.
func (n *Node) forwardNMR(arrived string, m *records.NMR) {
	_, s, err := n.routeSession(m.ToNode, arrived)
	if err != nil {
		n.log.Error(fmt.Sprintf("NMR from %s for %s dropped: %v", clean(m.FromNode), clean(m.ToNode), err))
		n.emit(link.EvError, link.L2, arrived, fmt.Sprintf("NMR for %s not forwarded: %v", clean(m.ToNode), err))
		return
	}
	if err := s.SendNMR(m); err != nil {
		n.log.Error("forwarding NMR: " + err.Error())
		return
	}
	text := fmt.Sprintf("forwarded NMR from %s to %s via %s", clean(m.FromNode), clean(m.ToNode), s.Remote())
	n.log.Info(text)
	n.emit(link.EvStream, link.L2, arrived, text)
}

// datasetDest is the destination node of a received data set.
func datasetDest(ds *link.InboundDataset) string {
	if ds.Header != nil && ds.Header.Node != "" {
		return ds.Header.Node
	}
	if ds.Job != nil {
		return ds.Job.PrintNode
	}
	return ""
}

// forwardDataset passes a received file for another node on: same
// origin, records and carriage control; NJHGHOPS + 1.
func (n *Node) forwardDataset(arrived string, ds *link.InboundDataset, dest string) {
	hops := uint16(0)
	f := &link.OutboundFile{ToNode: dest, Queued: time.Now(), Name: "forwarded file", JobName: "FWD"}
	if j := ds.Job; j != nil {
		hops = j.Hops
		f.OrigNode, f.FromUser, f.JobName, f.JobID, f.ToUser = j.OrigNode, j.OrigUser, j.JobName, j.JobID, j.PrintUser
	}
	f.Hops = hops + 1
	if f.Hops > maxHops {
		n.log.Error(fmt.Sprintf("file for %s from %s dropped: more than %d hops (routing loop?)", dest, f.OrigNode, maxHops))
		n.emit(link.EvError, link.L2, arrived, fmt.Sprintf("file for %s dropped after %d hops", dest, maxHops))
		return
	}
	if h := ds.Header; h != nil {
		f.ToUser, f.DSName, f.Proc, f.Step = h.Remote, h.Name, h.Proc, h.Step
		f.RecFM, f.LRECL, f.Punch = h.RecFM, int(h.LRECL), h.Flag2&records.NDHFlag2Punch != 0
	}
	if f.DSName != "" {
		f.Name = f.DSName
	}
	for _, r := range ds.Records {
		f.Records = append(f.Records, r.Data)
		f.CC = r.CC
	}
	n.mu.Lock()
	r, up, err := n.linkForLocked(dest, arrived)
	if err != nil {
		n.mu.Unlock()
		n.log.Error(fmt.Sprintf("file for %s from %s dropped: %v", dest, f.OrigNode, err))
		n.emit(link.EvError, link.L2, arrived, fmt.Sprintf("file for %s not forwarded: %v", dest, err))
		return
	}
	n.jobID = n.jobID%9999 + 1
	n.fileID++
	f.ID = n.fileID
	if f.JobID == 0 {
		f.JobID = uint16(n.jobID)
	}
	r.queue = append(r.queue, f)
	s := r.sess
	n.mu.Unlock()
	if up {
		s.Kick()
	}
	text := fmt.Sprintf("forwarding file %s from %s/%s to %s/%s via %s (hop %d)",
		f.Name, f.OrigNode, f.FromUser, dest, f.ToUser, r.cfg.Name, f.Hops)
	n.log.Info(text)
	n.emit(link.EvFileOut, link.L2, arrived, text)
}
