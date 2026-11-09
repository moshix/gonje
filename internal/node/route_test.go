// Copyright 2026 by moshix

package node

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
)

// meshNode builds a node with direct links to peers (ports set later).
func meshNode(t *testing.T, name, user string, peers ...string) *testNode {
	t.Helper()
	tn := newTestNode(t, name, peers[0], user, 5)
	tn.cfg.Remotes = nil
	for _, p := range peers {
		tn.cfg.Remotes = append(tn.cfg.Remotes, &config.Remote{Name: p, Addr: "127.0.0.1", Keepalive: 5})
	}
	n := New(tn.cfg, tn.opts) // rebuild with all remotes, same listener config
	n.ln = tn.Node.ln
	_, ch := n.Bus().Subscribe(100000)
	tn.Node, tn.events = n, ch
	return tn
}

func setPort(n *testNode, peer string, port int) { n.cfg.Remote(peer).Port = port }

// TestThreeNodeRouting: A has a route to C via B; C answers through its
// default route (B). Message, command (+ response) and a binary file
// from A reach C through B; origins stay intact.
func TestThreeNodeRouting(t *testing.T) {
	a := meshNode(t, "NODEA", "ALICE", "NODEB")
	b := meshNode(t, "NODEB", "BOB", "NODEA", "NODEC")
	c := meshNode(t, "NODEC", "CARL", "NODEB")
	setPort(a, "NODEB", b.port())
	setPort(b, "NODEA", a.port())
	setPort(b, "NODEC", c.port())
	setPort(c, "NODEB", b.port())
	a.cfg.Routes = []config.Route{{Dest: "NODEC", Via: "NODEB"}}
	c.cfg.DefaultRoute = "NODEB"
	for _, n := range []*testNode{a, b, c} {
		n.Start()
		defer n.Stop()
	}
	a.waitUp(t, "NODEB")
	c.waitUp(t, "NODEB")
	b.waitUp(t, "NODEA")
	b.waitUp(t, "NODEC")

	if err := a.SendMessage("nodec", "carl", "hello via B"); err != nil {
		t.Fatal(err)
	}
	ev := c.wait(t, "routed message", 5*time.Second, kindText(link.EvMsgIn, "hello via B"))
	if !strings.Contains(ev.Text, "from NODEA/ALICE to CARL") {
		t.Fatalf("%+v", ev)
	}
	b.wait(t, "forward logged", 5*time.Second, kindText(link.EvStream, "forwarded NMR from NODEA to NODEC via NODEC"))

	if err := a.SendCommand("NODEC", "Q SYS"); err != nil {
		t.Fatal(err)
	}
	c.wait(t, "routed command", 5*time.Second, kindText(link.EvCmdIn, "Q SYS"))
	a.wait(t, "routed response", 5*time.Second, kindText(link.EvRspIn, "NODEC: gonje"))

	data := make([]byte, 20000)
	rand.New(rand.NewSource(9)).Read(data)
	p := filepath.Join(a.dir, "route.bin")
	os.WriteFile(p, data, 0o644)
	f, err := a.SendFile("NODEC", "CARL", p)
	if err != nil || f.ToNode != "NODEC" {
		t.Fatalf("%+v %v", f, err)
	}
	b.wait(t, "file forwarded", 10*time.Second, kindText(link.EvFileOut, "forwarding file ROUTE from NODEA/ALICE to NODEC/CARL via NODEC (hop 1)"))
	c.wait(t, "routed file", 10*time.Second, kindText(link.EvFileIn, "NODEA.ALICE.ROUTE"))
	got := readSingle(t, filepath.Join(c.dir, "inbox"), "NODEA.ALICE.ROUTE.", ".bin")
	if !bytes.Equal(got, data) {
		t.Fatal("routed file not byte-exact")
	}
	if ents, _ := os.ReadDir(filepath.Join(b.dir, "inbox")); len(ents) != 0 {
		t.Fatal("intermediate node kept a copy")
	}
	if rs := a.Routes(); len(rs) != 1 || rs[0].Using != "NODEB" {
		t.Fatalf("routes %+v", rs)
	}
}

// routeNode is a node with links B and D and fake link states.
func routeNode(t *testing.T) *Node {
	cfg := config.Defaults()
	cfg.LocalNode = "NODEA"
	cfg.Remotes = []*config.Remote{{Name: "NODEB", Addr: "h", Port: 1, Keepalive: 5}, {Name: "NODED", Addr: "h", Port: 1, Keepalive: 5}}
	return New(cfg, Options{User: "ALICE", Log: logx.Discard()})
}

func (n *Node) setUp(name string, up bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.remotes[name].state = Down
	if up {
		n.remotes[name].state = Up
	}
}

func TestRouteSelection(t *testing.T) {
	n := routeNode(t)
	if err := n.SetRoute(config.Route{Dest: "MVSF", Via: "NODEB", Alt: "NODED"}); err != nil {
		t.Fatal(err)
	}
	pick := func(dest, exclude string) (string, bool, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		r, up, err := n.linkForLocked(dest, exclude)
		if err != nil {
			return "", false, err
		}
		return r.cfg.Name, up, nil
	}
	// Via up -> via; via down, alt up -> alt; both down -> via, not up.
	n.setUp("NODEB", true)
	if l, up, _ := pick("mvsf", ""); l != "NODEB" || !up {
		t.Fatalf("via: %s %v", l, up)
	}
	n.setUp("NODEB", false)
	n.setUp("NODED", true)
	if l, up, _ := pick("MVSF", ""); l != "NODED" || !up {
		t.Fatalf("alt: %s %v", l, up)
	}
	n.setUp("NODED", false)
	if l, up, _ := pick("MVSF", ""); l != "NODEB" || up {
		t.Fatalf("both down: %s %v", l, up)
	}
	if _, _, err := n.routeSession("MVSF", ""); err == nil || !strings.Contains(err.Error(), "neither NODEB nor NODED") {
		t.Fatalf("%v", err)
	}
	// Direct link wins; no route -> error; then the default route.
	if l, _, _ := pick("NODED", ""); l != "NODED" {
		t.Fatal("direct link")
	}
	if _, _, err := pick("OTHER", ""); err == nil {
		t.Fatal("unknown node accepted without a default route")
	}
	if err := n.SetDefaultRoute("NOPE"); err == nil {
		t.Fatal("undefined default route accepted")
	}
	n.SetDefaultRoute("noded")
	if l, _, _ := pick("OTHER", ""); l != "NODED" {
		t.Fatal("default route not used")
	}
	if l, _, _ := pick("MVSF", ""); l != "NODEB" {
		t.Fatal("explicit route must win over the default route")
	}
	// No reflection back to the arriving link.
	if _, _, err := pick("OTHER", "NODED"); err == nil || !strings.Contains(err.Error(), "leads back") {
		t.Fatalf("reflection: %v", err)
	}
	if rs := n.Routes(); len(rs) != 2 || rs[1].Dest != "*" || !rs[1].Default {
		t.Fatalf("%+v", rs)
	}
	// Validation and delete.
	for _, bad := range []config.Route{{Dest: "NODEA", Via: "NODEB"}, {Dest: "X", Via: "NOPE"}, {Dest: "NODEB", Via: "NODEB"}} {
		if err := n.SetRoute(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if n.DeleteRoute("mvsf") != nil || n.DeleteRoute("MVSF") == nil {
		t.Fatal("delete")
	}
	n.SetDefaultRoute("")
	if len(n.Routes()) != 0 {
		t.Fatal("default route not removed")
	}
}

func TestForwardHopLimitAndReflection(t *testing.T) {
	n := routeNode(t)
	var logBuf syncBuffer
	n.log = logx.New(&logBuf, logx.Debug)
	n.SetDefaultRoute("NODEB")
	ds := &link.InboundDataset{Job: &records.JobHeader{OrigNode: "FAR", OrigUser: "U", Hops: maxHops},
		Header:  &records.DatasetHeader{Node: "ELSEWHERE", Remote: "U"},
		Records: []link.DataRecord{{Data: []byte{1}}}}
	n.forwardDataset("NODED", ds, "ELSEWHERE")
	if !strings.Contains(logBuf.String(), "more than 16 hops") || len(n.remotes["NODEB"].queue) != 0 {
		t.Fatal("hop limit not enforced")
	}
	ds.Job.Hops = 3
	n.forwardDataset("NODEB", ds, "ELSEWHERE") // would go back out over NODEB
	if !strings.Contains(logBuf.String(), "leads back to NODEB") || len(n.remotes["NODEB"].queue) != 0 {
		t.Fatal("reflected a file back to its arriving link")
	}
	n.forwardDataset("NODED", ds, "ELSEWHERE")
	if q := n.remotes["NODEB"].queue; len(q) != 1 || q[0].Hops != 4 || q[0].OrigNode != "FAR" {
		t.Fatalf("forwarded file %+v", q)
	}
}

// TestSpoolSidecars: received text and binary files get a .meta sidecar
// with their NJE metadata.
func TestSpoolSidecars(t *testing.T) {
	a, b := startPair(t, 5)
	txt := filepath.Join(a.dir, "notes.txt")
	os.WriteFile(txt, []byte("one\ntwo\n"), 0o644)
	bin := filepath.Join(a.dir, "data.bin")
	os.WriteFile(bin, bytes.Repeat([]byte{0, 1, 2, 0xFF}, 50), 0o644)
	a.SendFile("NODEB", "BOB", txt)
	b.wait(t, "text", 10*time.Second, kindText(link.EvFileIn, "NOTES"))
	a.SendFile("NODEB", "BOB", bin)
	b.wait(t, "binary", 10*time.Second, kindText(link.EvFileIn, "DATA"))
	read := func(prefix, suffix string) string {
		return string(readSingle(t, filepath.Join(b.dir, "inbox"), prefix, suffix))
	}
	tm := read("NODEA.ALICE.NOTES.", ".txt.meta")
	for _, want := range []string{"from_node=NODEA", "from_user=ALICE", "to_node=NODEB", "to_user=BOB", "jobname=ALICE",
		"dsname=NOTES", "class=A", "records=2", "bytes=8", "kind=text", "recfm=V", "lrecl=3", "cc=none", "link=NODEA",
		"stream=SYSOUT1", "received="} {
		if !strings.Contains(tm, want+"\n") && !strings.Contains(tm, want) {
			t.Errorf("text meta lacks %q:\n%s", want, tm)
		}
	}
	bm := read("NODEA.ALICE.DATA.", ".bin.meta")
	for _, want := range []string{"kind=binary", "recfm=F", "lrecl=80", "bytes=200", "records=3"} {
		if !strings.Contains(bm, want) {
			t.Errorf("binary meta lacks %q:\n%s", want, bm)
		}
	}
}
