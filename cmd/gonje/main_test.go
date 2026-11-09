package main

import (
	"bufio"
	"bytes"
	"fmt"
	"flag"
	"io"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
	"gonje/internal/nje/tcpip"
	"gonje/internal/node"
)

// TestSplitFlag: -split, -split=true and -split=false override the
// config; leaving the flag out keeps the config value.
func TestSplitFlag(t *testing.T) {
	cases := []struct {
		args      []string
		set, want bool
	}{
		{nil, false, false},
		{[]string{"-split"}, true, true},
		{[]string{"-split=true"}, true, true},
		{[]string{"-split=false"}, true, false},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		var o optBool
		fs.Var(&o, "split", "")
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if o.set != c.set || o.val != c.want {
			t.Errorf("%v: set=%v val=%v", c.args, o.set, o.val)
		}
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o optBool
	fs.Var(&o, "split", "")
	if err := fs.Parse([]string{"-split=maybe"}); err == nil {
		t.Fatal("bad value accepted")
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args       []string
		mode, conf string
		ops        []string
		version    bool
		wait       int
	}{
		{nil, "", "gonje.cnf", nil, false, 30},
		{[]string{"-c", "my.cnf"}, "", "my.cnf", nil, false, 30},
		{[]string{"-f", "my.cnf", "-split=false"}, "", "my.cnf", nil, false, 30},
		{[]string{"-c", "send", "file.txt", "NODEID", "USERID"}, "send", "gonje.cnf", []string{"file.txt", "NODEID", "USERID"}, false, 30},
		{[]string{"-f", "o.cnf", "-c", "send", "f", "N", "U"}, "send", "o.cnf", []string{"f", "N", "U"}, false, 30},
		{[]string{"-w", "10", "-c", "cmd", "NODEID", "cp", "q", "users"}, "cmd", "gonje.cnf", []string{"NODEID", "cp", "q", "users"}, false, 10},
		{[]string{"-c", "msg", "NODEID", "USERID", "hello", "how", "are", "you?"}, "msg", "gonje.cnf", []string{"NODEID", "USERID", "hello", "how", "are", "you?"}, false, 30},
		{[]string{"-v"}, "", "gonje.cnf", nil, true, 30},
	}
	for _, c := range cases {
		o, err := parseArgs(c.args, io.Discard)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if o.mode != c.mode || o.config != c.conf || strings.Join(o.args, "|") != strings.Join(c.ops, "|") ||
			o.version != c.version || o.wait != c.wait {
			t.Errorf("%v: %+v", c.args, o)
		}
	}
	for _, bad := range [][]string{{"-w", "0"}, {"stray"}, {"-nosuch"}} {
		if _, err := parseArgs(bad, io.Discard); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// Operand checks per action.
	for _, bad := range []*cliOpts{
		{mode: "send", args: []string{"f", "N"}},
		{mode: "cmd", args: []string{"N"}},
		{mode: "msg", args: []string{"N", "U"}},
	} {
		bad.wait = 30
		if _, err := newBatchJob(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	j, _ := newBatchJob(&cliOpts{mode: "cmd", wait: 30, args: []string{"houcics", "cp", "q", "users"}})
	if j.node != "HOUCICS" || j.text != "cp q users" {
		t.Fatalf("%+v", j)
	}
}

// peer starts an in-process gonje node NODEB that accepts NODEA.
func peer(t *testing.T) (*node.Node, string, <-chan link.Event) {
	t.Helper()
	cfg := config.Defaults()
	dir := t.TempDir()
	cfg.LocalNode, cfg.LocalIP, cfg.LocalPort = "NODEB", "127.0.0.1", 0
	cfg.SendDir, cfg.RecvDir = filepath.Join(dir, "out"), filepath.Join(dir, "in")
	cfg.Remotes = []*config.Remote{{Name: "NODEA", Port: 1, Keepalive: 5}}
	n := node.New(cfg, node.Options{User: "BOB", Handshake: 3 * time.Second})
	if err := n.Listen(); err != nil {
		t.Fatal(err)
	}
	_, ev := n.Bus().Subscribe(10000)
	n.Start()
	t.Cleanup(n.Stop)
	return n, cfg.RecvDir, ev
}

func batchCfg(t *testing.T, peerAddr string) *config.Config {
	cfg := config.Defaults()
	dir := t.TempDir()
	host, port, _ := net.SplitHostPort(peerAddr)
	p, _ := strconv.Atoi(port)
	cfg.LocalNode, cfg.LocalIP, cfg.LocalPort = "NODEA", "127.0.0.1", 0
	cfg.SendDir, cfg.RecvDir = filepath.Join(dir, "out"), filepath.Join(dir, "in")
	cfg.Remotes = []*config.Remote{{Name: "NODEB", Addr: host, Port: p, Keepalive: 5}}
	return cfg
}

func job(mode string, args ...string) *batchJob {
	j, _ := newBatchJob(&cliOpts{mode: mode, wait: 10, args: args})
	j.quiet, j.grace, j.setup = 300*time.Millisecond, 200*time.Millisecond, 10*time.Second
	return j
}

func TestBatchEndToEnd(t *testing.T) {
	b, recv, bev := peer(t)
	cfg := batchCfg(t, b.Addr().String())
	opts := node.Options{User: "ALICE", Handshake: 3 * time.Second}

	// send: binary file, byte-exact at the peer.
	data := make([]byte, 5000)
	rand.New(rand.NewSource(3)).Read(data)
	f := filepath.Join(t.TempDir(), "blob.bin")
	os.WriteFile(f, data, 0o644)
	var out bytes.Buffer
	if rc := runBatch(job("send", f, "nodeb", "bob"), cfg, logx.Discard(), &out, opts); rc != 0 {
		t.Fatalf("send rc %d:\n%s", rc, out.String())
	}
	if !strings.Contains(out.String(), "gonje: connected to NODEB") ||
		!strings.Contains(out.String(), "gonje: blob.bin sent to NODEB/BOB (63 records)") {
		t.Fatalf("send output:\n%s", out.String())
	}
	waitFor(t, bev, link.EvFileIn, "BLOB")
	ents, _ := os.ReadDir(recv)
	got, _ := os.ReadFile(filepath.Join(recv, ents[0].Name()))
	if !bytes.Equal(got, data) {
		t.Fatal("file not byte-exact")
	}

	// cmd: the auto-answer comes back on stderr.
	out.Reset()
	if rc := runBatch(job("cmd", "NODEB", "Q", "SYS"), cfg, logx.Discard(), &out, opts); rc != 0 {
		t.Fatalf("cmd rc %d:\n%s", rc, out.String())
	}
	if !strings.Contains(out.String(), "NODEB: from NODEB") || !strings.Contains(out.String(), "NJE node") ||
		!strings.Contains(out.String(), "response line(s) from NODEB") {
		t.Fatalf("cmd output:\n%s", out.String())
	}

	// msg
	out.Reset()
	if rc := runBatch(job("msg", "NODEB", "BOB", "hello", "how", "are", "you?"), cfg, logx.Discard(), &out, opts); rc != 0 {
		t.Fatalf("msg rc %d:\n%s", rc, out.String())
	}
	waitFor(t, bev, link.EvMsgIn, "hello how are you?")
	if !strings.Contains(out.String(), "gonje: message sent to NODEB/BOB") {
		t.Fatalf("msg output:\n%s", out.String())
	}

	// Failures: unknown node, missing file, peer not listening.
	for _, j := range []*batchJob{job("msg", "NOPE", "U", "x"), job("send", "/no/such/file", "NODEB", "BOB")} {
		out.Reset()
		if rc := runBatch(j, cfg, logx.Discard(), &out, opts); rc != 1 || !strings.Contains(out.String(), "gonje: error: ") {
			t.Fatalf("%+v: rc %d\n%s", j, rc, out.String())
		}
	}
	dead := batchCfg(t, "127.0.0.1:1")
	out.Reset()
	dj := job("msg", "NODEB", "U", "x")
	dj.setup = 1500 * time.Millisecond
	if rc := runBatch(dj, dead, logx.Discard(), &out, opts); rc != 1 ||
		!strings.Contains(out.String(), "error: connect") {
		t.Fatalf("dead peer: rc %d\n%s", rc, out.String())
	}
}

func waitFor(t *testing.T, ev <-chan link.Event, k link.Kind, sub string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-ev:
			if e.Kind == k && strings.Contains(e.Text, sub) {
				return
			}
		case <-deadline:
			t.Fatalf("peer never saw %q", sub)
		}
	}
}

// fakeHOUCICS imitates the reference peer's connect behaviour: our dial
// is answered with answer() (NAK 03, or ACK and then silence), and
// shortly afterwards it calls us and signs on as primary. It returns the
// text of the first message NMR it receives.
func fakeHOUCICS(t *testing.T, batchPort int, answer func(c net.Conn)) (string, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tcpip.ReadControl(c)
		answer(c)
		time.Sleep(300 * time.Millisecond)
		// Now it dials us.
		d, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", batchPort))
		if err != nil {
			got <- "dial failed: " + err.Error()
			return
		}
		defer d.Close()
		br := bufio.NewReader(d)
		d.Write((&tcpip.Control{Type: tcpip.TypeOpen, RHost: "NODEB", OHost: "NODEA"}).Marshal())
		if r, _, err := tcpip.ReadControl(br); err != nil || r.Type != tcpip.TypeACK {
			got <- fmt.Sprintf("no ACK: %v %v", r, err)
			return
		}
		tcpip.WriteBlock(d, records.SOHENQ)
		tcpip.ReadBlock(br)
		i := &records.Buffer{BCB: 0x80, FCS: records.FCSNormal,
			Records: []records.Record{(&records.Signon{SRCB: records.SRCBSignonI, Node: "NODEB", BufSize: 1012}).Record()}}
		tcpip.WriteBlock(d, i.Encode())
		d.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			recs, _, err := tcpip.ReadBlock(br)
			if err != nil {
				got <- "read: " + err.Error()
				return
			}
			for _, r := range recs {
				b, err := records.Decode(r)
				if err != nil || b == nil {
					continue
				}
				for _, rec := range b.Records {
					if rec.RCB == records.RCBNMR {
						m, _ := records.ParseNMR(rec.Data)
						got <- m.Text()
						return
					}
				}
			}
		}
	}()
	return ln.Addr().String(), got
}

func freePort(t *testing.T) int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestBatchPeerDialsUs: the peer NAKs our OPEN with 03 (or ACKs it and
// stays silent) and calls us instead; batch mode accepts that call.
func TestBatchPeerDialsUs(t *testing.T) {
	answers := map[string]func(net.Conn){
		"NAK 03": func(c net.Conn) {
			c.Write((&tcpip.Control{Type: tcpip.TypeNAK, RHost: "NODEB", OHost: "NODEA", Reason: tcpip.NAKCollision}).Marshal())
		},
		"silent ACK": func(c net.Conn) {
			c.Write((&tcpip.Control{Type: tcpip.TypeACK, RHost: "NODEB", OHost: "NODEA"}).Marshal())
		},
	}
	for name, answer := range answers {
		port := freePort(t)
		addr, got := fakeHOUCICS(t, port, answer)
		cfg := batchCfg(t, addr)
		cfg.LocalPort = port
		var out bytes.Buffer
		rc := runBatch(job("msg", "NODEB", "HERC01", "via", "inbound"), cfg, logx.Discard(), &out,
			node.Options{User: "ALICE", Handshake: 3 * time.Second})
		if rc != 0 {
			t.Fatalf("%s: rc %d\n%s", name, rc, out.String())
		}
		if text := <-got; text != "via inbound" {
			t.Fatalf("%s: peer got %q", name, text)
		}
		if !strings.Contains(out.String(), "gonje: connected to NODEB") {
			t.Fatalf("%s: %s", name, out.String())
		}
	}
}
