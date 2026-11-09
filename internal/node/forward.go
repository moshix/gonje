// Copyright 2026 by moshix

package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gonje/internal/nje/link"
)

// Store-and-forward files are written to recv_dir/.forward/ before the
// sender gets its C0 (the Dataset handler runs before the link sends the
// queued C0), so gonje never holds the only copy of a file in memory.
// The spool file is deleted when the next hop confirms the file (C0 or
// the EOF grace). A file that cannot be forwarded (no route, hop limit,
// rejected by the next node, link deleted) stays there as "held" and is
// shown by the shell's spool command. At start every spool file is
// routed again.

// ForwardDirName is the forward spool directory inside recv_dir.
const ForwardDirName = ".forward"

// Forward spool states.
const (
	FwdQueued = "queued" // waiting for (or being sent over) the next hop
	FwdHeld   = "held"   // could not be forwarded; see Reason
)

// ForwardEntry is one forward spool file.
type ForwardEntry struct {
	ID      int               `json:"id"` // spool id (shared with received files)
	Dest    string            `json:"dest"`
	Arrived string            `json:"arrived"` // link it came in on
	Via     string            `json:"via,omitempty"`
	State   string            `json:"state"`
	Reason  string            `json:"reason,omitempty"`
	Stored  time.Time         `json:"stored"`
	File    link.OutboundFile `json:"file"`
	Path    string            `json:"-"`
}

// ForwardDir is the forward spool directory of recvDir.
func ForwardDir(recvDir string) string { return filepath.Join(recvDir, ForwardDirName) }

// ForwardEntries reads the forward spool, oldest first. A missing
// directory is an empty spool; unreadable files are skipped.
func ForwardEntries(recvDir string) ([]*ForwardEntry, error) {
	dir := ForwardDir(recvDir)
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*ForwardEntry
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fe, err := readForward(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func readForward(path string) (*ForwardEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fe ForwardEntry
	if err := json.Unmarshal(b, &fe); err != nil {
		return nil, err
	}
	fe.Path = path
	fe.File.Spool = path
	return &fe, nil
}

// writeForward stores e durably: temp file, fsync, rename. It sets
// e.Path on first write (fwd-<id>.json).
func writeForward(recvDir string, e *ForwardEntry) error {
	dir := ForwardDir(recvDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if e.Path == "" {
		e.Path = filepath.Join(dir, fmt.Sprintf("fwd-%06d.json", e.ID))
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return writeDurable(e.Path, b, 0o644)
}

// writeDurable replaces path with data: temp file in the same directory,
// fsync, rename (a crash leaves the old or the new content, never half).
func writeDurable(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	_ = tmp.Chmod(mode)
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return os.Rename(tmp.Name(), path)
}

// spoolMu serializes the spool id counter of all nodes in this process.
var spoolMu sync.Mutex

// SpoolSeqFile holds the last spool id given out in recv_dir.
const SpoolSeqFile = ".spoolseq"

// NextSpoolID returns a new spool id for recvDir: ids only grow, so the
// number the operator sees for a file never changes when other files
// arrive or are deleted.
func NextSpoolID(recvDir string) (int, error) {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	if err := os.MkdirAll(recvDir, 0o755); err != nil {
		return 0, err
	}
	path := filepath.Join(recvDir, SpoolSeqFile)
	last := 0
	if b, err := os.ReadFile(path); err == nil {
		last, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	id := last + 1
	if err := writeDurable(path, []byte(strconv.Itoa(id)+"\n"), 0o644); err != nil {
		return 0, err
	}
	return id, nil
}

// AssignSpoolID gives a received file without an id (saved by an older
// gonje) one, appended to its sidecar.
func AssignSpoolID(recvDir, path string) (int, error) {
	id, err := NextSpoolID(recvDir)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path+MetaSuffix, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "id=%d\n", id)
	return id, err
}

// spoolForward stores a newly received file for another node. Called
// from the Dataset handler, i.e. before the sender gets C0.
func (n *Node) spoolForward(arrived, dest string, f *link.OutboundFile) *ForwardEntry {
	e := &ForwardEntry{Dest: dest, Arrived: arrived, State: FwdQueued, Stored: time.Now(), File: *f}
	id, err := NextSpoolID(n.cfg.RecvDir)
	if err == nil {
		e.ID = id
		err = writeForward(n.cfg.RecvDir, e)
	}
	if err != nil {
		// Not on disk: forward from memory as before, but say so.
		n.log.Error(fmt.Sprintf("forward spool for file %s to %s: %v (kept in memory only)", f.Name, dest, err))
		e.Path = ""
	}
	return e
}

// queueForward routes a spooled file and queues it on the next hop, or
// holds it (on disk) when that is impossible. It reports the link used.
func (n *Node) queueForward(e *ForwardEntry) (string, error) {
	f := e.File // a copy: the queue owns its own OutboundFile
	f.Spool = e.Path
	var err error
	if f.Hops > maxHops {
		err = fmt.Errorf("more than %d hops (routing loop?)", maxHops)
	}
	var r *remote
	up := false
	n.mu.Lock()
	if err == nil {
		r, up, err = n.linkForLocked(e.Dest, e.Arrived)
	}
	if err != nil {
		n.mu.Unlock()
		n.holdForward(e, err.Error())
		return "", err
	}
	n.jobID = n.jobID%9999 + 1
	n.fileID++
	f.ID = n.fileID
	if f.JobID == 0 {
		f.JobID = uint16(n.jobID)
	}
	r.queue = append(r.queue, &f)
	s := r.sess
	via := r.cfg.Name
	n.mu.Unlock()
	if e.State != FwdQueued || e.Via != via {
		e.State, e.Reason, e.Via = FwdQueued, "", via
		n.rewriteForward(e)
	}
	if up {
		s.Kick()
	}
	return via, nil
}

// holdForward marks a forward spool file held with the reason, logs an
// ERROR and tells the operator.
func (n *Node) holdForward(e *ForwardEntry, reason string) {
	e.State, e.Reason = FwdHeld, reason
	n.rewriteForward(e)
	where := "in " + ForwardDir(n.cfg.RecvDir)
	if e.Path == "" {
		where = "LOST (no spool file)"
	}
	text := fmt.Sprintf("file %s from %s/%s for %s/%s held: forward failed: %s (%s)",
		e.File.Name, e.File.OrigNode, e.File.FromUser, e.Dest, e.File.ToUser, reason, where)
	n.log.Error(text)
	n.emit(link.EvError, link.L1, e.Arrived, text)
}

func (n *Node) rewriteForward(e *ForwardEntry) {
	if e.Path == "" {
		return
	}
	if err := writeForward(n.cfg.RecvDir, e); err != nil {
		n.log.Error("forward spool: " + err.Error())
	}
}

// forwardDone is called when the next hop answered for a spooled file:
// confirmed -> the spool file goes; rejected -> it is held.
func (n *Node) forwardDone(remote string, f *link.OutboundFile, err error) {
	if f.Spool == "" {
		return
	}
	if err == nil {
		if rerr := os.Remove(f.Spool); rerr != nil && !os.IsNotExist(rerr) {
			n.log.Error("forward spool: " + rerr.Error())
		}
		return
	}
	e, rerr := readForward(f.Spool)
	if rerr != nil {
		n.log.Error(fmt.Sprintf("forward spool %s: %v", f.Spool, rerr))
		return
	}
	n.holdForward(e, fmt.Sprintf("rejected by %s: %v", remote, err))
}

// recoverForwards routes every forward spool file again (at start):
// files queued when gonje stopped, and held ones (a route may exist
// now).
func (n *Node) recoverForwards() {
	list, err := ForwardEntries(n.cfg.RecvDir)
	if err != nil {
		n.log.Error("forward spool: " + err.Error())
		return
	}
	for _, e := range list {
		if via, err := n.queueForward(e); err == nil {
			n.log.Info(fmt.Sprintf("forward spool: file %s for %s queued again via %s", e.File.Name, e.Dest, via))
		}
	}
}

// queuedForwards counts store-and-forward files still waiting on links.
func (n *Node) queuedForwardsLocked() int {
	c := 0
	for _, r := range n.remotes {
		for _, f := range r.queue {
			if f.Spool != "" {
				c++
			}
		}
	}
	return c
}
