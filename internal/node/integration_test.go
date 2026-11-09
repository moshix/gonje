package node

import (
	"bytes"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
	"gonje/internal/nje/tcpip"
)

// testNode is one in-process gonje node plus its event subscription.
type testNode struct {
	*Node
	events <-chan link.Event
	dir    string
	logBuf *syncBuffer
}

func newTestNode(t *testing.T, name, peer, user string, keepalive int) *testNode {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.LocalNode, cfg.LocalIP, cfg.LocalPort = name, "127.0.0.1", 0
	cfg.SendDir, cfg.RecvDir = filepath.Join(dir, "outbox"), filepath.Join(dir, "inbox")
	cfg.Remotes = []*config.Remote{{Name: peer, Addr: "127.0.0.1", Keepalive: keepalive}}
	logBuf := &syncBuffer{}
	n := New(cfg, Options{RetryMin: 200 * time.Millisecond, RetryMax: time.Second,
		NAKMin: 50 * time.Millisecond, NAKMax: 200 * time.Millisecond,
		Handshake: 3 * time.Second, StartJitter: 50 * time.Millisecond, User: user,
		Log: logx.New(logBuf, logx.Debug)})
	if err := n.Listen(); err != nil {
		t.Fatal(err)
	}
	_, ch := n.Bus().Subscribe(100000)
	return &testNode{Node: n, events: ch, dir: dir, logBuf: logBuf}
}

// syncBuffer is a goroutine-safe writer for the test log.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// port returns the listening port.
func (tn *testNode) port() int { return tn.Addr().(*net.TCPAddr).Port }

// wait returns the first event matching pred, failing after timeout.
func (tn *testNode) wait(t *testing.T, what string, timeout time.Duration, pred func(link.Event) bool) link.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-tn.events:
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("%s: timeout waiting for %s", tn.cfg.LocalNode, what)
		}
	}
}

func kindText(k link.Kind, sub string) func(link.Event) bool {
	return func(ev link.Event) bool { return ev.Kind == k && strings.Contains(ev.Text, sub) }
}

// waitUp polls Status until the link to peer is up.
func (tn *testNode) waitUp(t *testing.T, peer string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range tn.Status() {
			if s.Name == peer && s.State == Up {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: link to %s never came up", tn.cfg.LocalNode, peer)
}

func (tn *testNode) state(peer string) State {
	for _, s := range tn.Status() {
		if s.Name == peer {
			return s.State
		}
	}
	return Down
}

// startPair starts NODEA and NODEB, both configured to connect to each
// other, so the startup connect race is exercised every time.
func startPair(t *testing.T, keepalive int) (a, b *testNode) {
	a = newTestNode(t, "NODEA", "NODEB", "ALICE", keepalive)
	b = newTestNode(t, "NODEB", "NODEA", "BOB", keepalive)
	a.cfg.Remotes[0].Port = b.port()
	b.cfg.Remotes[0].Port = a.port()
	a.Start()
	b.Start()
	t.Cleanup(func() {
		a.Stop()
		b.Stop()
	})
	a.waitUp(t, "NODEB")
	b.waitUp(t, "NODEA")
	return a, b
}

func TestIntegrationTwoNodes(t *testing.T) {
	a, b := startPair(t, 2)

	// --- message ---
	if err := a.SendMessage("nodeb", "bob", "Hello from A"); err != nil {
		t.Fatal(err)
	}
	ev := b.wait(t, "message", 5*time.Second, kindText(link.EvMsgIn, "Hello from A"))
	if !strings.Contains(ev.Text, "NODEA/ALICE") || !strings.Contains(ev.Text, "to BOB") || ev.Level != link.L1 {
		t.Fatalf("message event %+v", ev)
	}

	// --- commands with auto-answer ---
	if err := a.SendCommand("NODEB", "Q SYS"); err != nil {
		t.Fatal(err)
	}
	b.wait(t, "command at B", 5*time.Second, kindText(link.EvCmdIn, "Q SYS"))
	a.wait(t, "Q SYS response", 5*time.Second, kindText(link.EvRspIn, "NJE node"))
	if err := a.SendCommand("NODEB", "q links"); err != nil {
		t.Fatal(err)
	}
	a.wait(t, "Q LINKS response", 5*time.Second, kindText(link.EvRspIn, "NODEA    UP"))
	if err := a.SendCommand("NODEB", "Q FILES"); err != nil {
		t.Fatal(err)
	}
	a.wait(t, "Q FILES response", 5*time.Second, kindText(link.EvRspIn, "queued for NODEA"))
	if err := a.SendCommand("NODEB", "SHUTDOWN NOW"); err != nil {
		t.Fatal(err)
	}
	a.wait(t, "unsupported response", 5*time.Second, kindText(link.EvRspIn, "unsupported command: SHUTDOWN NOW"))

	// --- text file A -> B ---
	text := "First line\n\nThird line with   spaces inside\n" + strings.Repeat("long ", 80) + "end\nlast\n"
	tf := filepath.Join(a.dir, "hello.txt")
	if err := os.WriteFile(tf, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendFile("NODEB", "BOB", tf); err != nil {
		t.Fatal(err)
	}
	ev = b.wait(t, "text file", 10*time.Second, kindText(link.EvFileIn, "HELLO"))
	if ev.Level != link.L1 || !strings.Contains(ev.Text, "text") {
		t.Fatalf("file event %+v", ev)
	}
	got := readSingle(t, filepath.Join(b.dir, "inbox"), "NODEA.ALICE.HELLO.", ".txt")
	// The 405-character line arrives as four records (3*132 + 9), so four
	// lines.
	if want := wantSplit(text); string(got) != want {
		t.Fatalf("text mismatch:\n got %q\nwant %q", got, want)
	}
	a.wait(t, "file delivered", 5*time.Second, kindText(link.EvFileOut, "delivered"))

	// --- binary file B -> A ---
	bin := make([]byte, 5000)
	rand.New(rand.NewSource(42)).Read(bin)
	bin[0], bin[1], bin[len(bin)-1] = 0x00, 0x40, 0x40 // NUL and trailing blanks survive
	bf := filepath.Join(b.dir, "blob.dat")
	os.WriteFile(bf, bin, 0o644)
	if _, err := b.SendFile("NODEA", "ALICE", bf); err != nil {
		t.Fatal(err)
	}
	a.wait(t, "binary file", 10*time.Second, kindText(link.EvFileIn, "binary"))
	gotBin := readSingle(t, filepath.Join(a.dir, "inbox"), "NODEB.BOB.BLOB.", ".bin")
	if !bytes.Equal(gotBin, bin) {
		t.Fatalf("binary mismatch: %d bytes vs %d", len(gotBin), len(bin))
	}

	// --- drop and reconnect ---
	if err := a.Drop("NODEB"); err != nil {
		t.Fatal(err)
	}
	a.wait(t, "A link down", 5*time.Second, kindText(link.EvLink, "link down"))
	b.wait(t, "B sees signoff", 5*time.Second, kindText(link.EvLink, "peer signed off"))
	time.Sleep(600 * time.Millisecond) // nobody may auto-reconnect
	if a.state("NODEB") != Down || b.state("NODEA") != Down {
		t.Fatalf("link reconnected after drop: %v %v", a.state("NODEB"), b.state("NODEA"))
	}
	if err := a.SendMessage("NODEB", "BOB", "x"); err == nil {
		t.Fatal("message on a down link accepted")
	}
	// A file queued while down is sent after reconnect.
	if _, err := a.SendFile("NODEB", "BOB", tf); err != nil {
		t.Fatal(err)
	}
	if err := a.Connect("NODEB"); err != nil {
		t.Fatal(err)
	}
	a.waitUp(t, "NODEB")
	b.waitUp(t, "NODEA")
	b.wait(t, "queued file after reconnect", 10*time.Second, kindText(link.EvFileIn, "HELLO"))

	// --- broken connection: both sides reconnect automatically ---
	if err := b.Kill("NODEA"); err != nil {
		t.Fatal(err)
	}
	b.wait(t, "B link down", 5*time.Second, kindText(link.EvLink, "link down"))
	a.wait(t, "A link down", 5*time.Second, kindText(link.EvLink, "link down"))
	a.wait(t, "A link up again", 10*time.Second, kindText(link.EvLink, "link up"))
	a.waitUp(t, "NODEB")
	b.waitUp(t, "NODEA")
	if err := b.SendMessage("NODEA", "*", "after reconnect"); err != nil {
		t.Fatal(err)
	}
	a.wait(t, "message after reconnect", 5*time.Second, kindText(link.EvMsgIn, "after reconnect"))

	// --- keepalive: idle link stays up and exchanges null buffers ---
	ka := a.Status()[0].Stats.KeepaliveIn
	time.Sleep(4500 * time.Millisecond) // > 2x keepalive, > 3x would kill a dead link
	st := a.Status()[0]
	if st.State != Up || st.Stats.KeepaliveIn <= ka {
		t.Fatalf("keepalive: state %v, keepalives %d -> %d", st.State, ka, st.Stats.KeepaliveIn)
	}

	// The log got raw blocks and decoded records.
	logText := a.logBuf.String()
	for _, want := range []string{"TTB block sent", "TTB block received", "NMR 9A/80", "signon complete"} {
		if !strings.Contains(logText, want) {
			t.Errorf("log lacks %q", want)
		}
	}
}

// wantSplit predicts what the receiver writes for a text file: lines over
// maxTextRecord (132) characters arrive as several records (one line each).
func wantSplit(text string) string {
	var sb strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		for len(l) > maxTextRecord {
			sb.WriteString(strings.TrimRight(l[:maxTextRecord], " ") + "\n")
			l = l[maxTextRecord:]
		}
		sb.WriteString(strings.TrimRight(l, " ") + "\n")
	}
	return sb.String()
}

// readSingle returns the content of the one file in dir with the given
// prefix and suffix.
func readSingle(t *testing.T, dir, prefix, suffix string) []byte {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var found []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), suffix) {
			found = append(found, e.Name())
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one %s*%s in %s, found %v", prefix, suffix, dir, found)
	}
	b, err := os.ReadFile(filepath.Join(dir, found[0]))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInboundOPENValidation(t *testing.T) {
	a := newTestNode(t, "NODEA", "NODEB", "ALICE", 5)
	a.cfg.Remotes[0].Addr = "" // passive: never dials
	a.Start()
	defer a.Stop()

	open := func(rhost, ohost string) *tcpip.Control {
		c, err := net.Dial("tcp", a.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Write((&tcpip.Control{Type: tcpip.TypeOpen, RHost: rhost, OHost: ohost}).Marshal())
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		reply, _, err := tcpip.ReadControl(c)
		if err != nil {
			return nil
		}
		return reply
	}
	if r := open("STRANGER", "NODEA"); r == nil || r.Type != tcpip.TypeNAK || r.Reason != tcpip.NAKNoSuchLink {
		t.Fatalf("unknown node: %+v", r)
	}
	if r := open("NODEB", "OTHER"); r == nil || r.Type != tcpip.TypeNAK || r.Reason != tcpip.NAKNoSuchLink {
		t.Fatalf("wrong OHOST: %+v", r)
	}
	if r := open("NODEB", "NODEA"); r == nil || r.Type != tcpip.TypeACK || r.RHost != "NODEA" || r.OHost != "NODEB" {
		t.Fatalf("valid OPEN: %+v", r)
	}
	// While a signon is in progress (Accepting), another OPEN gets NAK 04.
	setState := func(st State) {
		// Let the previous accepted connection's signon fail first.
		for i := 0; i < 200 && a.state("NODEB") != Down; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		a.mu.Lock()
		a.remotes["NODEB"].state = st
		a.mu.Unlock()
	}
	setState(Accepting)
	if r := open("NODEB", "NODEA"); r == nil || r.Type != tcpip.TypeNAK || r.Reason != tcpip.NAKTemporary {
		t.Fatalf("accepting: %+v", r)
	}
	// Collision tie-break: NODEB > NODEA, so while NODEA is itself
	// connecting, the higher node's inbound connection is accepted.
	setState(Connecting)
	if r := open("NODEB", "NODEA"); r == nil || r.Type != tcpip.TypeACK {
		t.Fatalf("collision with higher caller: %+v", r)
	}
	// Garbage control record: connection is closed without a reply.
	c, _ := net.Dial("tcp", a.Addr().String())
	c.Write(bytes.Repeat([]byte{0xFF}, 33))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := tcpip.ReadControl(c); err == nil {
		t.Fatal("expected close on malformed control record")
	}
	c.Close()
	if err := a.Connect("NODEB"); err == nil {
		t.Fatal("connect to passive remote accepted")
	}
	if err := a.Drop("NOPE"); err == nil {
		t.Fatal("drop of unknown node accepted")
	}
}

func TestCollisionLowerCallerGetsNAK03(t *testing.T) {
	b := newTestNode(t, "NODEB", "NODEA", "BOB", 5)
	b.cfg.Remotes[0].Addr = ""
	b.Start()
	defer b.Stop()
	b.mu.Lock()
	b.remotes["NODEA"].state = Connecting // NODEB is dialling NODEA itself
	b.mu.Unlock()
	c, err := net.Dial("tcp", b.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write((&tcpip.Control{Type: tcpip.TypeOpen, RHost: "NODEA", OHost: "NODEB"}).Marshal())
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	r, _, err := tcpip.ReadControl(c)
	if err != nil || r.Type != tcpip.TypeNAK || r.Reason != tcpip.NAKCollision {
		t.Fatalf("expected NAK 03, got %+v %v", r, err)
	}
	b.mu.Lock()
	b.remotes["NODEA"].state = Down
	b.mu.Unlock()
}

func TestConnectCollisionTieBreak(t *testing.T) {
	// Run several pair startups: both nodes dial at almost the same time
	// (StartJitter 50ms), so collisions happen; the link must always
	// settle with exactly one connection.
	for i := 0; i < 3; i++ {
		a, b := startPair(t, 5)
		if err := a.SendMessage("NODEB", "BOB", "ping"); err != nil {
			t.Fatal(err)
		}
		b.wait(t, "ping", 5*time.Second, kindText(link.EvMsgIn, "ping"))
		a.Stop()
		b.Stop()
	}
}

func TestAnswer(t *testing.T) {
	cfg := config.Defaults()
	cfg.LocalNode = "NODEB"
	cfg.Remotes = []*config.Remote{{Name: "NODEA", Addr: "h", Port: 1}}
	n := New(cfg, Options{User: "BOB"})
	cmd := func(s string) []string {
		m, _ := records.NewCommand("NODEA", "ALICE", "NODEB", s)
		return n.answer(m)
	}
	if l := cmd("QUERY SYSTEM"); len(l) != 2 || !strings.Contains(l[0], "gonje") {
		t.Fatalf("Q SYS %q", l)
	}
	if l := cmd("Q LINKS"); len(l) != 2 || !strings.Contains(l[1], "NODEA") || !strings.Contains(l[1], "DOWN") {
		t.Fatalf("Q LINKS %q", l)
	}
	if l := cmd("Q FILES"); len(l) != 2 {
		t.Fatalf("Q FILES %q", l)
	}
	if l := cmd("CP SHUTDOWN"); len(l) != 1 || !strings.Contains(l[0], "unsupported command: CP SHUTDOWN") {
		t.Fatalf("other %q", l)
	}
	f := &records.NMR{Flag: 0xA0, Type: records.NMRTypeFormatted, Msg: []byte{1}}
	if l := n.answer(f); !strings.Contains(l[0], "formatted") {
		t.Fatal("formatted")
	}
	long := cmd(strings.Repeat("Z", 130))
	for _, l := range long {
		if len(l) > records.NMRMaxText {
			t.Fatalf("response line longer than an NMR allows: %d", len(l))
		}
	}
}

func TestClean(t *testing.T) {
	if clean("a\nb\x1b[2Jc\u0085") != "a.b.[2Jc." || clean("plain") != "plain" {
		t.Fatalf("%q", clean("a\nb\x1b[2Jc"))
	}
}

// TestStopWithStalledInbound: a caller that connects and never sends an
// OPEN must not delay shutdown (review C1).
func TestStopWithStalledInbound(t *testing.T) {
	a := newTestNode(t, "NODEA", "NODEB", "ALICE", 5)
	a.cfg.Remotes[0].Addr = ""
	a.opts.Handshake = 30 * time.Second
	a.Start()
	c, err := net.Dial("tcp", a.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	a.Stop()
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Stop took %v", d)
	}
}

// TestResponseClassification: a level-3 message is a response only if we
// recently sent that node a command (review N7).
func TestResponseClassification(t *testing.T) {
	a, b := startPair(t, 5)
	// JES2-style normal message at level X'30' with no command pending.
	m := records.NewMessages("NODEB", "BOB", "NODEA", "ALICE", "NORMAL LEVEL 3")[0]
	m.Level = 0x30
	_, s, err := b.session("NODEA")
	if err != nil {
		t.Fatal(err)
	}
	s.SendNMR(m)
	ev := a.wait(t, "level-3 message", 5*time.Second, func(e link.Event) bool {
		return strings.Contains(e.Text, "NORMAL LEVEL 3")
	})
	if ev.Kind != link.EvMsgIn {
		t.Fatalf("shown as kind %d, want message", ev.Kind)
	}
}

// TestBackoffAfterFlappingLink: a link that drops right after signon is
// redialled with a growing delay; the delay resets only after the link
// stayed up for StableAfter (review N3).
func TestBackoffAfterFlappingLink(t *testing.T) {
	a, b := startPair(t, 5)
	a.mu.Lock()
	first := a.remotes["NODEB"].retryDelay
	a.mu.Unlock()
	for i := 0; i < 2; i++ {
		b.Kill("NODEA")
		a.wait(t, "link down", 5*time.Second, kindText(link.EvLink, "link down"))
		a.waitUp(t, "NODEB")
		b.waitUp(t, "NODEA")
	}
	a.mu.Lock()
	grown := a.remotes["NODEB"].retryDelay
	a.mu.Unlock()
	if grown <= first {
		t.Fatalf("backoff did not grow: %v -> %v", first, grown)
	}
}

func TestBackoffCap(t *testing.T) {
	cfg := config.Defaults()
	cfg.LocalNode = "A"
	cfg.Remotes = []*config.Remote{{Name: "B", Addr: "h", Port: 1}}
	n := New(cfg, Options{RetryMin: 40 * time.Second, RetryMax: time.Minute})
	r := n.remotes["B"]
	for i := 0; i < 20; i++ {
		if d := n.backoffLocked(r); d > time.Minute || d < 40*time.Second {
			t.Fatalf("delay %v outside 40s..60s", d)
		}
	}
}
