// Copyright 2026 by moshix

package node

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
)

// fwdDataset is a received file for another node.
func fwdDataset(dest string, hops uint16) *link.InboundDataset {
	return &link.InboundDataset{Job: &records.JobHeader{OrigNode: "FAR", OrigUser: "U", JobName: "J", Hops: hops},
		Header:  &records.DatasetHeader{Node: dest, Remote: "U", Name: "FWD.DATA"},
		Records: []link.DataRecord{{Data: []byte{1, 2, 3}}, {Data: []byte{4}}}}
}

func oneForward(t *testing.T, n *Node) *ForwardEntry {
	t.Helper()
	list, err := ForwardEntries(n.cfg.RecvDir)
	if err != nil || len(list) != 1 {
		t.Fatalf("forward spool: %d entries, %v", len(list), err)
	}
	return list[0]
}

// TestForwardSpoolNoRoute: a file without a route is kept on disk as
// held, with the reason, and an ERROR is logged (second review M3).
func TestForwardSpoolNoRoute(t *testing.T) {
	n := routeNode(t)
	var logBuf syncBuffer
	n.log = logx.New(&logBuf, logx.Debug)
	n.forwardDataset("NODED", fwdDataset("NOWHERE", 0), "NOWHERE")
	e := oneForward(t, n)
	if e.State != FwdHeld || !strings.Contains(e.Reason, "NOWHERE") || e.Dest != "NOWHERE" || len(e.File.Records) != 2 {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(logBuf.String(), "ERROR") || !strings.Contains(logBuf.String(), "held: forward failed") {
		t.Fatalf("log:\n%s", logBuf.String())
	}
	// Hop limit: held too.
	n2 := routeNode(t)
	n2.SetDefaultRoute("NODEB")
	n2.forwardDataset("NODED", fwdDataset("ELSEWHERE", maxHops), "ELSEWHERE")
	if e := oneForward(t, n2); e.State != FwdHeld || !strings.Contains(e.Reason, "hops") {
		t.Fatalf("%+v", e)
	}
}

// TestForwardSpoolLifecycle: queued on disk before C0, removed when the
// next hop confirms; a reject (B0) or link delete holds it; a restart
// queues spooled files again.
func TestForwardSpoolLifecycle(t *testing.T) {
	n := routeNode(t)
	n.SetDefaultRoute("NODEB")
	h := &handler{n: n}
	h.Dataset("NODED", fwdDataset("ELSEWHERE", 1))
	e := oneForward(t, n)
	q := n.remotes["NODEB"].queue
	if e.State != FwdQueued || e.Via != "NODEB" || len(q) != 1 || q[0].Spool != e.Path || q[0].Hops != 2 {
		t.Fatalf("%+v %+v", e, q)
	}
	// Confirmed by the next hop: gone.
	h.FileDone("NODEB", q[0], nil)
	if list, _ := ForwardEntries(n.cfg.RecvDir); len(list) != 0 {
		t.Fatal("spool file kept after confirmation")
	}
	// Rejected mid-transfer: held.
	h.Dataset("NODED", fwdDataset("ELSEWHERE", 1))
	q = n.remotes["NODEB"].queue
	h.FileDone("NODEB", q[0], errors.New("receiver cancel"))
	if e := oneForward(t, n); e.State != FwdHeld || !strings.Contains(e.Reason, "rejected by NODEB: receiver cancel") {
		t.Fatalf("%+v", e)
	}
	os.Remove(oneForward(t, n).Path)

	// Queued when the node stops: a new node on the same recv_dir
	// queues it again at start.
	h.Dataset("NODED", fwdDataset("ELSEWHERE", 1))
	n.Stop()
	n2 := New(n.cfg, Options{User: "ALICE", Log: logx.Discard()})
	n2.recoverForwards()
	if q := n2.remotes["NODEB"].queue; len(q) != 1 || q[0].Spool == "" || len(q[0].Records) != 2 || q[0].OrigNode != "FAR" {
		t.Fatalf("not recovered: %+v", q)
	}
	// link delete is refused while the default route uses the link;
	// once that is gone the queued forward is held, not lost.
	if _, err := n2.DeleteLink("NODEB"); err == nil || !strings.Contains(err.Error(), "default route") {
		t.Fatalf("delete with route: %v", err)
	}
	n2.SetDefaultRoute("")
	if _, err := n2.DeleteLink("NODEB"); err != nil {
		t.Fatal(err)
	}
	if e := oneForward(t, n2); e.State != FwdHeld || !strings.Contains(e.Reason, "link deleted") {
		t.Fatalf("%+v", e)
	}
	// Spool ids only grow.
	a, _ := NextSpoolID(n.cfg.RecvDir)
	b, _ := NextSpoolID(n.cfg.RecvDir)
	if b != a+1 {
		t.Fatalf("ids %d %d", a, b)
	}
}

// TestForwardNMRLoop: an NMR of our own node that comes back to us for
// another node is dropped (second review m2).
func TestForwardNMRLoop(t *testing.T) {
	n := routeNode(t)
	var logBuf syncBuffer
	n.log = logx.New(&logBuf, logx.Debug)
	n.SetDefaultRoute("NODEB")
	m := records.NewMessages("NODEA", "ALICE", "FAR", "U", "hello")[0]
	n.forwardNMR("NODED", m)
	if !strings.Contains(logBuf.String(), "routing loop") {
		t.Fatalf("log:\n%s", logBuf.String())
	}
}

// TestLongRecordsSpanned: records of 256 bytes or more (a forwarded file
// carries whatever was received) are sent as spanned segments and arrive
// byte-exact, with their record boundaries (second review m1).
func TestLongRecordsSpanned(t *testing.T) {
	a, b := startPair(t, 5)
	long := func(n int, seed byte) []byte {
		r := make([]byte, n)
		for i := range r {
			r[i] = seed + byte(i*7)
		}
		return r
	}
	recs := [][]byte{long(300, 1), long(80, 2), long(1500, 3), long(256, 4)}
	f := &link.OutboundFile{ToNode: "NODEB", ToUser: "BOB", FromUser: "ALICE", JobName: "LONG", Name: "LONG",
		DSName: "LONG", Binary: true, Punch: true, RecFM: records.RecFmVariable, LRECL: 1500, Records: recs}
	a.mu.Lock()
	r := a.remotes["NODEB"]
	a.fileID++
	f.ID = a.fileID
	r.queue = append(r.queue, f)
	s := r.sess
	a.mu.Unlock()
	s.Kick()
	b.wait(t, "long records", 10*time.Second, kindText(link.EvFileIn, "LONG"))
	got := readSingle(t, b.dir+"/inbox", "NODEA.LONG.LONG.", ".bin")
	want := []byte{}
	for _, r := range recs {
		want = append(want, r...)
	}
	if string(got) != string(want) {
		t.Fatalf("got %d bytes, want %d", len(got), len(want))
	}
}

// TestSendNotify: the send callbacks fire once the NMR is written (msg,
// cmd) or the file is delivered (send), and with an error for a file
// discarded by link delete.
func TestSendNotify(t *testing.T) {
	a, _ := startPair(t, 5)
	got := make(chan error, 3)
	if err := a.SendMessageNotify("NODEB", "BOB", strings.Repeat("long text ", 40), func(err error) { got <- err }); err != nil {
		t.Fatal(err)
	}
	if err := a.SendCommandNotify("NODEB", "D T", func(err error) { got <- err }); err != nil {
		t.Fatal(err)
	}
	p := a.dir + "/n.txt"
	os.WriteFile(p, []byte("one\ntwo\n"), 0o644)
	if _, err := a.SendFileNotify("NODEB", "BOB", p, func(err error) { got <- err }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case err := <-got:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("callback %d never fired", i)
		}
	}
	select {
	case err := <-got:
		t.Fatalf("extra callback %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	// joinDone: first error wins, once.
	var calls []error
	cb := joinDone(2, func(err error) { calls = append(calls, err) })
	cb(errors.New("x"))
	cb(nil)
	if len(calls) != 1 || calls[0] == nil || joinDone(1, nil) != nil {
		t.Fatalf("%v", calls)
	}
}
