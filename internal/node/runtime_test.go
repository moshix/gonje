// Copyright 2026 by moshix

package node

import (
	"bytes"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/ebcdic"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
	"gonje/internal/nje/tcpip"
)

func TestDefineChangeDeleteLink(t *testing.T) {
	a, b := startPair(t, 5)
	rc := config.Remote{Name: "nodec", Addr: "", Port: 2999, Keepalive: 7, Password: "sekret"}
	if err := a.DefineLink(rc); err != nil {
		t.Fatal(err)
	}
	if err := a.DefineLink(rc); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate define: %v", err)
	}
	for _, bad := range []config.Remote{
		{Name: "TOOLONGNAME", Port: 1, Keepalive: 1},
		{Name: "NODEA", Port: 1, Keepalive: 1},
		{Name: "NODEQ", Port: 70000, Keepalive: 1},
		{Name: "NODEQ", Port: 1, Keepalive: 0},
	} {
		if err := a.DefineLink(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	got, err := a.RemoteConfig("NODEC")
	if err != nil || got.Port != 2999 || got.Password != "SEKRET" {
		t.Fatalf("%+v %v", got, err)
	}
	got.Keepalive, got.Addr = 30, "127.0.0.1"
	if err := a.ChangeLink(got); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.RemoteConfig("nodec"); got.Keepalive != 30 || got.Addr != "127.0.0.1" {
		t.Fatalf("change not applied: %+v", got)
	}
	if err := a.ChangeLink(config.Remote{Name: "NOPE", Port: 1, Keepalive: 1}); err == nil {
		t.Fatal("change of unknown link accepted")
	}
	if len(a.Status()) != 2 || len(a.cfg.Remotes) != 2 {
		t.Fatal("status after define")
	}
	// Deleting an active link signs it off first.
	if _, err := a.DeleteLink("NODEB"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.state("NODEA") != Down && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if b.state("NODEA") != Down {
		t.Fatal("B still up after A deleted the link")
	}
	if len(a.Status()) != 1 || a.Status()[0].Name != "NODEC" {
		t.Fatalf("status after delete: %+v", a.Status())
	}
	if _, err := a.DeleteLink("NODEB"); err == nil {
		t.Fatal("second delete accepted")
	}
	log := a.logBuf.String()
	if !strings.Contains(log, "link define NODEC") || !strings.Contains(log, "password=***") ||
		strings.Contains(log, "SEKRET") || !strings.Contains(log, "link delete NODEB") {
		t.Fatalf("define/delete not logged correctly:\n%s", log)
	}
}

func TestSetLocalNameBounces(t *testing.T) {
	a, b := startPair(t, 5)
	// B learns the new name first (passive: it waits for A to call).
	if err := b.DefineLink(config.Remote{Name: "NODEX", Port: 1, Keepalive: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetLocalName("NODEB"); err == nil {
		t.Fatal("rename to a remote's name accepted")
	}
	n, err := a.SetLocalName("nodex")
	if err != nil || n != 1 {
		t.Fatalf("bounced %d, %v", n, err)
	}
	if a.Local().Name != "NODEX" {
		t.Fatal("name not changed")
	}
	deadline := time.Now().Add(10 * time.Second)
	for b.state("NODEX") != Up && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if b.state("NODEX") != Up {
		t.Fatalf("link did not come back under the new name: %+v", b.Status())
	}
	if err := a.SendMessage("NODEB", "BOB", "renamed"); err != nil {
		t.Fatal(err)
	}
	ev := b.wait(t, "message from renamed node", 5*time.Second, kindText(link.EvMsgIn, "renamed"))
	if !strings.Contains(ev.Text, "NODEX/ALICE") {
		t.Fatalf("%+v", ev)
	}
}

func TestSetListen(t *testing.T) {
	a := newTestNode(t, "NODEA", "NODEB", "ALICE", 5)
	a.cfg.Remotes[0].Addr = ""
	a.Start()
	defer a.Stop()
	oldAddr := a.Addr().String()
	// Port in use elsewhere: error, old listener kept.
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	bp := busy.Addr().(*net.TCPAddr).Port
	if err := a.SetListen("127.0.0.1", bp); err == nil {
		t.Fatal("listen on a busy port succeeded")
	}
	if a.Addr().String() != oldAddr {
		t.Fatal("listener not kept after failure")
	}
	if c, err := net.Dial("tcp", oldAddr); err != nil {
		t.Fatal("old listener gone after a failed move")
	} else {
		c.Close()
	}
	// Move to a new ephemeral port: the new one answers OPEN, the old is closed.
	if err := a.SetListen("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	newAddr := a.Addr().String()
	if newAddr == oldAddr {
		t.Fatal("address unchanged")
	}
	c, err := net.Dial("tcp", newAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write((&tcpip.Control{Type: tcpip.TypeOpen, RHost: "NODEB", OHost: "NODEA"}).Marshal())
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if r, _, err := tcpip.ReadControl(c); err != nil || r.Type != tcpip.TypeACK {
		t.Fatalf("new listener: %+v %v", r, err)
	}
	if c2, err := net.DialTimeout("tcp", oldAddr, time.Second); err == nil {
		c2.Close()
		t.Fatal("old listener still accepting")
	}
	li := a.Local()
	if li.IP != "127.0.0.1" || li.Listen != newAddr || li.Links != 1 {
		t.Fatalf("%+v", li)
	}
}

func linkCounters(n *testNode, name string) LinkCounters {
	for _, l := range n.Counters().Links {
		if l.Name == name {
			return l
		}
	}
	return LinkCounters{}
}

// TestCounters: message, command (+2 auto-answer lines) and a file are
// counted on both sides, survive a reconnect, and reset to zero.
func TestCounters(t *testing.T) {
	a, b := startPair(t, 5)
	if err := a.SendMessage("NODEB", "BOB", "count me"); err != nil {
		t.Fatal(err)
	}
	b.wait(t, "message", 5*time.Second, kindText(link.EvMsgIn, "count me"))
	a.SendCommand("NODEB", "Q SYS")
	a.wait(t, "response 1", 5*time.Second, kindText(link.EvRspIn, "NJE node"))
	a.wait(t, "response 2", 5*time.Second, kindText(link.EvRspIn, "listening"))
	p := a.dir + "/c.txt"
	os.WriteFile(p, []byte("one\ntwo\nthree\n"), 0o644)
	a.SendFile("NODEB", "BOB", p)
	a.wait(t, "delivered", 5*time.Second, kindText(link.EvFileOut, "delivered"))

	ca, cb := linkCounters(a, "NODEB"), linkCounters(b, "NODEA")
	if ca.Total.MsgsOut != 1 || ca.Total.CmdsOut != 1 || ca.Total.FilesOut != 1 || ca.RspsIn != 2 ||
		ca.Total.MsgsIn != 2 || ca.Total.RecordsOut != 3 || ca.Total.FileBytesOut != 11 || ca.Connects != 1 {
		t.Fatalf("A counters %+v", ca)
	}
	if cb.Total.MsgsIn != 1 || cb.Total.CmdsIn != 1 || cb.Total.FilesIn != 1 || cb.RspsOut != 2 ||
		cb.Total.MsgsOut != 2 || cb.Total.RecordsIn != 3 || cb.Total.FileBytesIn != 11 {
		t.Fatalf("B counters %+v", cb)
	}
	if ca.Total.BytesOut == 0 || ca.Total.BytesIn == 0 || ca.Total.BlocksOut == 0 ||
		ca.SessionSince.IsZero() || ca.Total.LastActivity.IsZero() {
		t.Fatalf("A wire counters %+v", ca.Total)
	}
	if cb.Total.BytesIn != ca.Total.BytesOut && cb.Total.BytesIn < ca.Total.BytesOut-200 {
		t.Fatalf("bytes mismatch: A out %d, B in %d", ca.Total.BytesOut, cb.Total.BytesIn)
	}
	// Totals survive a reconnect.
	b.Kill("NODEA")
	a.wait(t, "link down", 5*time.Second, kindText(link.EvLink, "link down"))
	a.waitUp(t, "NODEB")
	ca = linkCounters(a, "NODEB")
	if ca.Total.MsgsOut != 1 || ca.Total.FilesOut != 1 || ca.Connects != 2 || ca.Connected <= 0 {
		t.Fatalf("after reconnect %+v", ca)
	}
	if c := a.Counters(); c.Accepted+b.Counters().Accepted < 1 {
		t.Fatal("no accepted connections counted")
	}
	// Reset one link, then everything.
	if err := a.ResetCounters("nodeb"); err != nil {
		t.Fatal(err)
	}
	ca = linkCounters(a, "NODEB")
	if ca.Total.MsgsOut != 0 || ca.Total.FilesOut != 0 || ca.Connects != 0 || ca.RspsIn != 0 {
		t.Fatalf("after reset %+v", ca)
	}
	a.SendMessage("NODEB", "BOB", "after reset")
	b.wait(t, "message after reset", 5*time.Second, kindText(link.EvMsgIn, "after reset"))
	if ca = linkCounters(a, "NODEB"); ca.Total.MsgsOut != 1 {
		t.Fatalf("counting after reset: %+v", ca.Total)
	}
	if err := a.ResetCounters(""); err != nil || a.Counters().Accepted != 0 {
		t.Fatal("global reset")
	}
	if err := a.ResetCounters("NOPE"); err == nil {
		t.Fatal("reset of unknown link accepted")
	}
}

// TestMessagesToAnyUser: gonje has no local users, so messages to any
// name (or to the operator) are shown at L1, with the target user.
func TestMessagesToAnyUser(t *testing.T) {
	a, b := startPair(t, 5)
	for _, user := range []string{"WHOEVER", "OPER", "*"} {
		if err := a.SendMessage("NODEB", user, "hi "+user); err != nil {
			t.Fatal(err)
		}
		want := "to " + user + ": hi " + user
		if user == "*" {
			want = "to (operator): hi *"
		}
		ev := b.wait(t, "message to "+user, 5*time.Second, kindText(link.EvMsgIn, want))
		if ev.Level != link.L1 || !strings.Contains(ev.Text, "from NODEA/ALICE") {
			t.Fatalf("%+v", ev)
		}
	}
	if !strings.Contains(b.logBuf.String(), "to WHOEVER: hi WHOEVER") {
		t.Fatal("message not logged")
	}
}

// TestPendingCommands: a command counts as pending until the first
// response arrives (or the 5-minute response window passes); slow
// responses are still classified as responses within the window.
func TestPendingCommands(t *testing.T) {
	a, _ := startPair(t, 5)
	a.mu.Lock()
	r := a.remotes["NODEB"]
	r.pendingCmds, r.lastCmd = 1, time.Now().Add(-30*time.Second) // a slow peer
	a.mu.Unlock()
	if a.Local().Pending != 1 {
		t.Fatal("pending not counted")
	}
	req := &records.NMR{Flag: 0xA0, FromNode: "NODEA"}
	copy(req.Out[:], ebcdic.Field("ALICE", 8))
	resp := records.NewResponse(req, "NODEB", "SLOW ANSWER")
	(&handler{n: a.Node}).NMR("NODEB", resp)
	ev := a.wait(t, "slow response", 5*time.Second, kindText(link.EvRspIn, "SLOW ANSWER"))
	if ev.Level != link.L1 || a.Local().Pending != 0 {
		t.Fatalf("%+v pending %d", ev, a.Local().Pending)
	}
	a.mu.Lock()
	r.pendingCmds, r.lastCmd = 2, time.Now().Add(-6*time.Minute)
	a.mu.Unlock()
	if a.Local().Pending != 0 {
		t.Fatal("pending shown after the response window")
	}
	if responseWindow < 5*time.Minute {
		t.Fatal("response window shorter than 5 minutes")
	}
}

// TestResponseToCommandingUser: a message at a non-response level,
// addressed to the user who sent a command (HOUCICS answers "D T" this
// way), counts as a response.
func TestResponseToCommandingUser(t *testing.T) {
	a, _ := startPair(t, 5)
	a.mu.Lock()
	a.remotes["NODEB"].lastCmd = time.Now()
	a.mu.Unlock()
	m := records.NewMessages("NODEB", "SYSTEM", "NODEA", "ALICE", "NJE163I TIME=10.55.00 DATE=2026.313")[0]
	(&handler{n: a.Node}).NMR("NODEB", m)
	a.wait(t, "response", 5*time.Second, kindText(link.EvRspIn, "NJE163I"))
	if c := linkCounters(a, "NODEB"); c.RspsIn != 1 {
		t.Fatalf("responses in %d", c.RspsIn)
	}
	// An operator (no user) message, like HOUCICS's "Signon of link ...
	// complete", stays a message even with a command pending.
	m = records.NewMessages("NODEB", "SYSTEM", "NODEA", "*", "Signon of link complete")[0]
	(&handler{n: a.Node}).NMR("NODEB", m)
	a.wait(t, "operator message", 5*time.Second, kindText(link.EvMsgIn, "Signon of link complete"))
	// To another user it stays a message.
	m = records.NewMessages("NODEB", "SYSTEM", "NODEA", "OTHER", "just a message")[0]
	(&handler{n: a.Node}).NMR("NODEB", m)
	a.wait(t, "message", 5*time.Second, kindText(link.EvMsgIn, "just a message"))
}

// TestStuckDialYieldsToInbound: an own dial that hangs (peer ACKed but
// never signs on) gives way to the peer's inbound call, even when our
// name is the higher one.
func TestStuckDialYieldsToInbound(t *testing.T) {
	b := newTestNode(t, "NODEZ", "NODEA", "BOB", 5) // higher name: normally NAKs 03
	b.cfg.Remotes[0].Addr = ""
	b.Start()
	defer b.Stop()
	b.mu.Lock()
	r := b.remotes["NODEA"]
	r.state, r.dialStart = Connecting, time.Now().Add(-10*time.Second)
	b.mu.Unlock()
	c, err := net.Dial("tcp", b.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write((&tcpip.Control{Type: tcpip.TypeOpen, RHost: "NODEA", OHost: "NODEZ"}).Marshal())
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if rep, _, err := tcpip.ReadControl(c); err != nil || rep.Type != tcpip.TypeACK {
		t.Fatalf("stuck dial did not yield: %+v %v", rep, err)
	}
}

// TestGeneralTimeout: an unreachable remote is given up after timeout
// seconds; connect starts a fresh window.
func TestGeneralTimeout(t *testing.T) {
	a := newTestNode(t, "NODEA", "NODEB", "ALICE", 5)
	a.cfg.Remotes[0].Port = 1 // nobody there
	a.cfg.Timeout = 1
	a.opts.RetryMin, a.opts.RetryMax = 100*time.Millisecond, 200*time.Millisecond
	a.Start()
	defer a.Stop()
	a.wait(t, "give up", 5*time.Second, kindText(link.EvLink, "gave up after 1 s, use connect"))
	st := statusOf(a, "NODEB")
	if st.State != Down || !st.Held || st.LastErr != "gave up after 1 s, use connect" {
		t.Fatalf("%+v", st)
	}
	if err := a.Connect("NODEB"); err != nil {
		t.Fatal(err)
	}
	if statusOf(a, "NODEB").Held {
		t.Fatal("connect did not start a fresh window")
	}
	a.wait(t, "give up again", 5*time.Second, kindText(link.EvLink, "gave up after 1 s"))
	if err := a.SetTimeout(-1); err == nil || a.Local().Timeout != 1 {
		t.Fatal("SetTimeout validation")
	}
}

// startStreams starts a pair with max_streams n on both sides.
func startStreams(t *testing.T, n int) (a, b *testNode) {
	a = newTestNode(t, "NODEA", "NODEB", "ALICE", 5)
	b = newTestNode(t, "NODEB", "NODEA", "BOB", 5)
	a.cfg.Remotes[0].Port, b.cfg.Remotes[0].Port = b.port(), a.port()
	a.cfg.Remotes[0].MaxStreams, b.cfg.Remotes[0].MaxStreams = n, n
	a.Start()
	b.Start()
	t.Cleanup(func() { a.Stop(); b.Stop() })
	a.waitUp(t, "NODEB")
	b.waitUp(t, "NODEA")
	return a, b
}

// sendFiles queues k random binary files and returns their contents by
// name; it also records the most transfers seen running at once.
func sendFiles(t *testing.T, a *testNode, k, size int) (map[string][]byte, int) {
	files := map[string][]byte{}
	for i := 0; i < k; i++ {
		data := make([]byte, size)
		rand.New(rand.NewSource(int64(i + 10))).Read(data)
		name := fmt.Sprintf("f%d.bin", i)
		p := filepath.Join(a.dir, name)
		os.WriteFile(p, data, 0o644)
		files[strings.ToUpper(strings.TrimSuffix(name, ".bin"))] = data
		if _, err := a.SendFile("NODEB", "BOB", p); err != nil {
			t.Fatal(err)
		}
	}
	peak := 0
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		st := statusOf(a, "NODEB")
		peak = max(peak, st.Stats.ActiveOut)
		if st.Queued == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return files, peak
}

func checkReceived(t *testing.T, b *testNode, files map[string][]byte) {
	t.Helper()
	dir := filepath.Join(b.dir, "inbox")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ents, _ := os.ReadDir(dir); len(ents) >= 2*len(files) { // files + .meta
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var ents []os.DirEntry
	all, _ := os.ReadDir(dir)
	for _, e := range all {
		if !strings.HasSuffix(e.Name(), MetaSuffix) {
			ents = append(ents, e)
		}
	}
	if len(ents) != len(files) {
		t.Fatalf("%d files received, want %d", len(ents), len(files))
	}
	for _, e := range ents {
		name := strings.Split(e.Name(), ".")[2] // NODEA.ALICE.<NAME>.<ts>.bin
		got, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if !bytes.Equal(got, files[name]) {
			t.Fatalf("%s differs", e.Name())
		}
	}
}

func TestMaxStreamsConcurrent(t *testing.T) {
	a, b := startStreams(t, 3)
	files, peak := sendFiles(t, a, 3, 300<<10)
	if peak != 3 {
		t.Fatalf("at most %d transfers ran at once, want 3", peak)
	}
	checkReceived(t, b, files)
	if st := statusOf(a, "NODEB"); st.MaxStreams != 3 {
		t.Fatalf("status max streams %d", st.MaxStreams)
	}
}

func TestMaxStreamsOneSerialises(t *testing.T) {
	a, b := startStreams(t, 1)
	files, peak := sendFiles(t, a, 2, 200<<10)
	if peak != 1 {
		t.Fatalf("%d transfers at once with max_streams=1", peak)
	}
	checkReceived(t, b, files)
}
