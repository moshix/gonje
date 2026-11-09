// Copyright 2026 by moshix

package shell

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/ebcdic"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/node"
)

func TestFitAndWrap(t *testing.T) {
	long := strings.Repeat("x", 200)
	// No built-in column limit: width 0 means "do not cut".
	if Width(Fit(long, 0)) != 200 || Width(Fit(long, 30)) != 30 || Width(Fit(long, 150)) != 150 {
		t.Fatal("Fit width")
	}
	if Fit("a\x1b[2Jb\tc\r\n", 0) != "a.[2Jb.c.." {
		t.Fatalf("control chars: %q", Fit("a\x1b[2Jb\tc", 0))
	}
	if Fit("ééé", 2) != "éé" {
		t.Fatal("rune truncation")
	}
	text := strings.Repeat("word ", 50) + strings.Repeat("Z", 150)
	for _, width := range []int{40, 79, 132, 200} {
		for i, l := range Wrap(text, width, "    ") {
			if Width(l) > width || (i > 0 && !strings.HasPrefix(l, "    ")) {
				t.Fatalf("width %d: row %d %q", width, i, l)
			}
		}
	}
	if got := Wrap(text, 0, "  "); len(got) != 1 || Width(got[0]) != Width(text) {
		t.Fatal("width 0 must not wrap")
	}
	if usableWidth(200) != 199 || usableWidth(0) != DefaultWidth-1 || usableWidth(5) != 10 {
		t.Fatal("usableWidth")
	}
}

func TestColorOnOff(t *testing.T) {
	l := SegLine(Seg{"ok ", BoldGreen}, Seg{"text", Default}, Seg{" red", Red})
	if got := render(l, 0, false); got != "ok text red" {
		t.Fatalf("colors off: %q", got)
	}
	on := render(l, 0, true)
	if !strings.Contains(on, "\x1b[1;32mok \x1b[0m") || !strings.Contains(on, "\x1b[31m red\x1b[0m") {
		t.Fatalf("colors on: %q", on)
	}
	// Width counts text, not escape sequences; segments are clipped.
	if got := render(l, 5, false); got != "ok te" {
		t.Fatalf("clip: %q", got)
	}
	if got := renderPad(Line{Text: "ab"}, 5, true); got != "ab   " {
		t.Fatalf("pad: %q", got)
	}
	// The log never gets colors: it uses Line.Text.
	var buf bytes.Buffer
	s, _ := newTestShell(t)
	s.set.Log = logx.New(&buf, logx.Debug)
	s.set.Color = true
	s.Exec("nodes")
	s.Exec("help")
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("escape sequence in the log:\n%q", buf.String())
	}
}

func TestUseUnicode(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		m    map[string]string
		want bool
	}{
		{map[string]string{"LANG": "en_US.UTF-8"}, true},
		{map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, false},
		{map[string]string{"LC_CTYPE": "de_DE.utf8"}, true},
		{map[string]string{"LANG": "en_US.UTF-8", "GONJE_ASCII": "1"}, false},
		{map[string]string{}, false},
	}
	for _, c := range cases {
		if got := UseUnicode(env(c.m)); got != c.want {
			t.Errorf("%v: %v", c.m, got)
		}
	}
}

func ev(k link.Kind, level int, text string, lines ...string) link.Event {
	return link.Event{Time: time.Date(2026, 1, 1, 8, 51, 2, 0, time.Local), Kind: k, Level: level,
		Node: "NODEB", Text: text, Lines: lines}
}

func TestFormatEvent(t *testing.T) {
	l := FormatEvent(ev(link.EvMsgIn, 2, "from NODEA/ALICE: hello"))
	if len(l) != 1 || l[0].Text != "08:51:02 MSG  < NODEB from NODEA/ALICE: hello" || l[0].Color != Cyan {
		t.Fatalf("%+v", l)
	}
	dump := ebcdic.HexDump(bytes.Repeat([]byte{0xC1}, 40))
	l = FormatEvent(ev(link.EvDump, 3, "-> NMR", dump...))
	if len(l) != 4 {
		t.Fatalf("lines %d", len(l))
	}
	// Dump line colors: offset dim, hex default, characters yellow.
	d := l[1]
	if d.Text != "  "+dump[0] || d.Segs[1].Color != Dim || d.Segs[1].Text != "0000" ||
		d.Segs[3].Color != Yellow || d.Segs[3].Text != "AAAAAAAAAAAAAAAA" {
		t.Fatalf("dump segments %+v", d.Segs)
	}
	colors := map[link.Kind]Color{link.EvError: Red, link.EvLink: Yellow, link.EvCmdIn: Magenta,
		link.EvRspIn: Magenta, link.EvFileIn: Green, link.EvMsgOut: Cyan}
	for k, c := range colors {
		if FormatEvent(ev(k, 1, "x"))[0].Color != c {
			t.Errorf("kind %d color", k)
		}
	}
}

func TestPanel(t *testing.T) {
	p := NewPanel(10)
	for i := 0; i < 15; i++ {
		p.Add(Line{Text: string(rune('a' + i))})
	}
	if p.Len() != 10 {
		t.Fatalf("len %d", p.Len())
	}
	if v := p.Visible(4, 80); v[0].Text != "l" || v[3].Text != "o" {
		t.Fatalf("visible %+v", v)
	}
	p.ScrollUp(2, 4)
	if p.Scroll() != 2 || p.Visible(4, 80)[3].Text != "m" {
		t.Fatalf("scroll %d", p.Scroll())
	}
	p.Add(Line{Text: "p"}) // view stays put while scrolled back
	if p.Visible(4, 80)[3].Text != "m" {
		t.Fatal("view moved while scrolled")
	}
	for i := 0; i < 10; i++ {
		p.ScrollUp(2, 4)
	}
	if p.Scroll() != 6 || p.Visible(4, 80)[0].Text != "g" {
		t.Fatalf("top clamp %d", p.Scroll())
	}
	p.ScrollDown(100)
	if p.Scroll() != 0 || p.Visible(4, 80)[3].Text != "p" {
		t.Fatal("bottom clamp")
	}
	p.ScrollUp(3, 4)
	p.Live()
	if p.Scroll() != 0 {
		t.Fatal("live")
	}
	short := NewPanel(10)
	short.Add(Line{Text: "x"})
	if v := short.Visible(3, 80); len(v) != 3 || v[2].Text != "x" || v[0].Text != "" {
		t.Fatalf("padding %+v", v)
	}
	l, rep := short.AddKeepalive("NODEB", "08:00:00")
	if rep || l.Text != "08:00:00 keepalive x1 NODEB" {
		t.Fatalf("%q %v", l.Text, rep)
	}
	short.AddKeepalive("NODEB", "08:00:10")
	l, rep = short.AddKeepalive("NODEB", "08:00:20")
	if !rep || l.Text != "08:00:20 keepalive x3 NODEB" || short.Len() != 2 {
		t.Fatalf("%q %v %d", l.Text, rep, short.Len())
	}
	if _, rep = short.AddKeepalive("NODEC", "08:00:30"); rep {
		t.Fatal("other node collapsed")
	}
}

func TestPanelWrapsAtRenderTime(t *testing.T) {
	p := NewPanel(10)
	p.Add(Line{Text: strings.Repeat("abcd ", 30), Cont: "  "}) // 150 columns
	p.Add(Line{Text: "tail"})
	if wide := p.Visible(3, 200); Width(wide[1].Text) < 140 {
		t.Fatalf("wide terminal shows the line cut: %q", wide[1].Text)
	}
	narrow := p.Visible(3, 60)
	if narrow[2].Text != "tail" || !strings.HasPrefix(narrow[1].Text, "  ") {
		t.Fatalf("narrow: %+v", narrow)
	}
	for _, r := range narrow {
		if Width(r.Text) > 60 {
			t.Fatalf("row wider than panel: %d", Width(r.Text))
		}
	}
}

// fakeBackend implements Backend for command tests.
type fakeBackend struct {
	calls     []string
	fail      bool
	local     node.LocalInfo
	remotes   map[string]config.Remote
	lastRoute config.Route
	dones     []func(error) // send callbacks, fired by the tests
}

func (f *fakeBackend) rec(s string) error {
	f.calls = append(f.calls, s)
	if f.fail {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeBackend) Status() []node.LinkStatus {
	return []node.LinkStatus{
		{Name: "NODEB", Addr: "127.0.0.1:2175", State: node.Up, Since: time.Now(),
			Stats: link.Stats{Primary: true, MsgsIn: 1}},
		{Name: "NODEC", Line: 1, State: node.Down, Held: true, Queued: 2, LastErr: strings.Repeat("e", 120)},
	}
}
func (f *fakeBackend) Local() node.LocalInfo { return f.local }
func (f *fakeBackend) SetTimeout(sec int) error {
	f.local.Timeout = sec
	return f.rec("timeout")
}
func (f *fakeBackend) Connect(n string) error { return f.rec("connect " + n) }
func (f *fakeBackend) Drop(n string) error    { return f.rec("drop " + n) }
func (f *fakeBackend) Drain(n string) (string, error) {
	return n + " draining: waiting for 1 inbound, 0 outbound transfer(s)", f.rec("drain " + n)
}
func (f *fakeBackend) SendCommandNotify(n, c string, done func(error)) error {
	f.dones = append(f.dones, done)
	return f.rec("cmd " + n + "|" + c)
}
func (f *fakeBackend) SendMessageNotify(n, u, t string, done func(error)) error {
	f.dones = append(f.dones, done)
	return f.rec("msg " + n + "|" + u + "|" + t)
}
func (f *fakeBackend) SendFileNotify(n, u, p string, done func(error)) (*link.OutboundFile, error) {
	f.dones = append(f.dones, done)
	if err := f.rec("send " + n + "|" + u + "|" + p); err != nil {
		return nil, err
	}
	return &link.OutboundFile{Name: p, ToNode: n, ToUser: u, Records: make([][]byte, 3)}, nil
}
func (f *fakeBackend) ResetFraming(n string) error { return f.rec("reset-framing " + n) }
func (f *fakeBackend) SetLocalName(n string) (int, error) {
	if err := f.rec("name " + n); err != nil {
		return 0, err
	}
	f.local.Name = n
	return 1, nil
}
func (f *fakeBackend) SetListen(ip string, port int) error {
	if err := f.rec("listen " + ip); err != nil {
		return err
	}
	f.local.IP, f.local.Port, f.local.Listen = ip, port, ip+":x"
	return nil
}
func (f *fakeBackend) RemoteConfig(n string) (config.Remote, error) {
	r, ok := f.remotes[strings.ToUpper(n)]
	if !ok {
		return r, errors.New("no remote node " + n)
	}
	return r, nil
}
func (f *fakeBackend) DefineLink(rc config.Remote) error {
	if err := config.ValidateRemote(&rc, f.local.Name); err != nil {
		return err
	}
	if _, ok := f.remotes[rc.Name]; ok {
		return errors.New("link exists")
	}
	f.remotes[rc.Name] = rc
	return f.rec("define " + rc.Name)
}
func (f *fakeBackend) ChangeLink(rc config.Remote) error {
	if err := config.ValidateRemote(&rc, f.local.Name); err != nil {
		return err
	}
	f.remotes[rc.Name] = rc
	return f.rec("change " + rc.Name)
}
func (f *fakeBackend) Counters() node.Counters {
	return node.Counters{Start: time.Now().Add(-192 * time.Second), Since: time.Now(), Accepted: 2, Rejected: 1,
		Links: []node.LinkCounters{
			{Name: "NODEB", Addr: "127.0.0.1:2175", State: node.Up, SessionSince: time.Now().Add(-time.Minute),
				Connects: 3, Failures: 1, RspsOut: 2,
				Total: link.Stats{MsgsIn: 4, MsgsOut: 5, CmdsIn: 1, FilesIn: 1, FileBytesIn: 2048,
					BytesIn: 1300, BytesOut: 900, KeepaliveIn: 77, BCBErrors: 1, LastActivity: time.Now()}},
			{Name: "NODEC", State: node.Down, Held: true, LastErr: "refused"},
		}}
}
func (f *fakeBackend) ResetCounters(n string) error { return f.rec("reset " + n) }
func (f *fakeBackend) Routes() []node.RouteStatus {
	return []node.RouteStatus{{Route: config.Route{Dest: "MVSF", Via: "MVSB", Alt: "MVSC"}, Using: "MVSC"},
		{Route: config.Route{Dest: "*", Via: "MVSB"}, Default: true}}
}
func (f *fakeBackend) SetRoute(rt config.Route) error {
	f.lastRoute = rt
	return f.rec("route " + rt.String())
}
func (f *fakeBackend) DeleteRoute(d string) error     { return f.rec("route delete " + d) }
func (f *fakeBackend) SetDefaultRoute(d string) error { return f.rec("route default " + d) }
func (f *fakeBackend) DeleteLink(n string) (int, error) {
	delete(f.remotes, n)
	return 2, f.rec("delete " + n)
}

func newTestShell(t *testing.T) (*Shell, *fakeBackend) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	logp := filepath.Join(dir, "gonje.log")
	os.WriteFile(logp, []byte(strings.Repeat("log line "+strings.Repeat("L", 100)+"\n", 30)), 0o644)
	be := &fakeBackend{local: node.LocalInfo{Name: "NODEA", IP: "127.0.0.1", Port: 1175,
		Listen: "127.0.0.1:1175", Links: 2, Up: 1}, remotes: map[string]config.Remote{}}
	return New(be, Settings{Version: "9.9", SendDir: dir, RecvDir: dir, LogPath: logp,
		Level: 2, SplitRatio: 40, Split: true, HistoryPath: filepath.Join(dir, "hist")}), be
}

func TestExecCommands(t *testing.T) {
	s, be := newTestShell(t)
	cases := []struct {
		line, call, out string
	}{
		{"connect nodeb", "connect nodeb", "connecting to NODEB"},
		{"drop NODEB", "drop NODEB", "dropping NODEB"},
		{"msg nodeb bob  hello   there", "msg nodeb|bob|hello   there", "message sent to NODEB/BOB"},
		{"cmd nodeb q sys", "cmd nodeb|q sys", "command sent to NODEB"},
		{"send nodeb bob my file.txt", "send nodeb|bob|my file.txt", "queued for nodeb/bob (3 records, text)"},
	}
	for _, c := range cases {
		be.calls = nil
		res := s.Exec(c.line)
		if len(be.calls) != 1 || be.calls[0] != c.call {
			t.Errorf("%q: calls %q", c.line, be.calls)
		}
		if len(res.Lines) == 0 || !strings.Contains(res.Lines[0].Text, c.out) || res.Lines[0].Color != Green {
			t.Errorf("%q: output %+v", c.line, res.Lines)
		}
	}
	be.fail = true
	if res := s.Exec("connect x"); res.Lines[0].Color != Red || res.Lines[0].Text != "error: boom" {
		t.Fatal("error not shown")
	}
	be.fail = false
	for _, bad := range []string{"connect", "drop", "msg a b", "cmd a", "send a b", "level 4",
		"split maybe", "split 5", "frob", "local name", "local zz y", "link", "link define X",
		"link define TOOLONGNAME h", "link define X h port=0", "link define X h keepalive=0",
		"link define X h password=123456789", "link define X h strict_bcb=maybe", "link define X h bogus=1",
		"link change NOPE port=1", "link delete"} {
		if res := s.Exec(bad); len(res.Lines) == 0 || res.Lines[0].Color != Red {
			t.Errorf("%q not rejected: %+v", bad, res.Lines)
		}
	}
	if res := s.Exec("level 3"); s.Level() != 3 || res.Lines[0].Color != Green {
		t.Fatal("level")
	}
	if s.Exec("split on").Split != 1 || s.Exec("split off").Split != -1 {
		t.Fatal("split")
	}
	if !s.Exec("quit").Quit {
		t.Fatal("quit")
	}
	if len(s.Exec("").Lines) != 0 {
		t.Fatal("empty line")
	}
	if v := s.Exec("version").Lines[0].Text; v != "gonje 9.9" {
		t.Fatalf("version %q", v)
	}
	for _, c := range []string{"help", "nodes", "status", "ls", "inbox", "log", "link list", "local"} {
		if res := s.Exec(c); len(res.Lines) < 2 {
			t.Errorf("%s: too little output", c)
		}
	}
}

func TestStatusColors(t *testing.T) {
	s, _ := newTestShell(t)
	lines := s.Exec("nodes").Lines
	if lines[1].Segs[2].Color != BoldGreen || !strings.HasPrefix(lines[1].Segs[2].Text, "UP") {
		t.Fatalf("UP not green: %+v", lines[1].Segs)
	}
	if lines[2].Segs[2].Color != Red || lines[2].Segs[5].Text != "held q=2" || lines[2].Segs[5].Color != Dim {
		t.Fatalf("DOWN/held: %+v", lines[2].Segs)
	}
	if stateColor(node.Connecting) != Yellow || stateColor(node.Accepting) != Yellow {
		t.Fatal("in-between states")
	}
}

func TestExecLogsCommands(t *testing.T) {
	var buf bytes.Buffer
	s, be := newTestShell(t)
	s.set.Log = logx.New(&buf, logx.Debug)
	be.fail = true
	s.Exec("msg NODEB bob hi")
	s.Exec("level 2")
	out := buf.String()
	for _, want := range []string{"INFO  operator: msg NODEB bob hi", "ERROR shell: error: boom",
		"INFO  operator: level 2", "INFO  shell: panel level 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// copyConfig makes a temporary copy of test/nodeA.cnf for save tests.
func copyConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../test/nodeA.cnf")
	if err != nil {
		t.Skip("test/nodeA.cnf missing")
	}
	p := filepath.Join(t.TempDir(), "gonje.cnf")
	os.WriteFile(p, b, 0o644)
	return p
}

func TestLocalCommands(t *testing.T) {
	s, be := newTestShell(t)
	s.set.ConfigPath = copyConfig(t)
	res := s.Exec("local")
	if !strings.Contains(res.Lines[0].Text, "NODEA") || !strings.Contains(res.Lines[2].Text, "127.0.0.1:1175") {
		t.Fatalf("%+v", res.Lines)
	}
	res = s.Exec("local name nodex save")
	text := joinText(res.Lines)
	if be.local.Name != "NODEX" || !strings.Contains(text, "1 active link(s) will reconnect as NODEX") ||
		!strings.Contains(text, "saved to") {
		t.Fatalf("%s", text)
	}
	res = s.Exec("local port 1999 save")
	if be.calls[len(be.calls)-1] != "listen 127.0.0.1" || be.local.Port != 1999 || !strings.Contains(joinText(res.Lines), "saved") {
		t.Fatalf("%+v %+v", be.local, res.Lines)
	}
	s.Exec("local ip 0.0.0.0 save")
	c, err := config.Load(s.set.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.LocalNode != "NODEX" || c.LocalPort != 1999 || c.LocalIP != "0.0.0.0" {
		t.Fatalf("saved config %+v", c)
	}
	if b, _ := os.ReadFile(s.set.ConfigPath); !strings.HasPrefix(string(b), "# Local test node A") {
		t.Fatal("comments lost")
	}
	if res := s.Exec("local port 70000"); res.Lines[0].Color != Red {
		t.Fatal("bad port accepted")
	}
	s.set.ConfigPath = ""
	if res := s.Exec("local name y save"); !strings.Contains(joinText(res.Lines), "no config file") {
		t.Fatal("save without config file")
	}
}

func TestLinkCommands(t *testing.T) {
	s, be := newTestShell(t)
	s.set.ConfigPath = copyConfig(t)
	res := s.Exec("link define nodec 10.0.0.3 port=2999 keepalive=7 password=pw strict_bcb=yes save")
	if !strings.Contains(joinText(res.Lines), "link NODEC defined") || !strings.Contains(joinText(res.Lines), "saved") {
		t.Fatalf("%+v", res.Lines)
	}
	if rc := be.remotes["NODEC"]; rc.Addr != "10.0.0.3" || rc.Port != 2999 || rc.Keepalive != 7 ||
		rc.Password != "PW" || !rc.StrictBCB {
		t.Fatalf("%+v", rc)
	}
	if res := s.Exec("link define nodec 10.0.0.3"); res.Lines[0].Color != Red {
		t.Fatal("duplicate define accepted")
	}
	s.Exec("link change nodec - keepalive=30 save") // "-" = passive
	if rc := be.remotes["NODEC"]; rc.Addr != "" || rc.Keepalive != 30 || rc.Port != 2999 {
		t.Fatalf("change: %+v", rc)
	}
	c, _ := config.Load(s.set.ConfigPath)
	if r := c.Remote("NODEC"); r == nil || r.Keepalive != 30 || r.Addr != "" || r.Password != "PW" {
		t.Fatalf("saved %+v", r)
	}
	if st, _ := os.Stat(s.set.ConfigPath); st.Mode().Perm() != 0o600 {
		t.Fatalf("config with password not 0600: %o", st.Mode().Perm())
	}
	res = s.Exec("link delete nodec save")
	if !strings.Contains(joinText(res.Lines), "2 queued file(s) for NODEC discarded") {
		t.Fatalf("%+v", res.Lines)
	}
	c, _ = config.Load(s.set.ConfigPath)
	if c.Remote("NODEC") != nil || c.Remote("NODEB") == nil {
		t.Fatal("delete not saved / wrong block removed")
	}
}

func joinText(ls []Line) string {
	var sb strings.Builder
	for _, l := range ls {
		sb.WriteString(l.Text + "\n")
	}
	return sb.String()
}

func TestHandleEventLevels(t *testing.T) {
	s, _ := newTestShell(t)
	s.level = 1
	if s.HandleEvent(ev(link.EvMsgIn, 2, "hidden at L1")) {
		t.Fatal("L2 event shown at L1")
	}
	if !s.HandleEvent(ev(link.EvCmdIn, 1, "Q SYS")) || s.events.Len() != 1 || s.mixed.Len() != 1 {
		t.Fatal("L1 event hidden")
	}
	s.level = 3
	s.HandleEvent(ev(link.EvKeepalive, 3, "keepalive"))
	s.HandleEvent(ev(link.EvKeepalive, 3, "keepalive"))
	if s.events.Len() != 2 || !strings.Contains(s.mixed.Visible(1, 80)[0].Text, "keepalive x2 NODEB") {
		t.Fatal("keepalive collapse")
	}
}

func TestHistoryFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h")
	if LoadHistory(p) != nil || LoadHistory("") != nil {
		t.Fatal("missing file")
	}
	AppendHistory(p, "one")
	AppendHistory(p, "two")
	AppendHistory("", "ignored")
	AppendHistory(filepath.Join(dir, "no", "such", "dir"), "x") // unwritable: ignored
	if h := LoadHistory(p); len(h) != 2 || h[1] != "two" {
		t.Fatalf("history %q", h)
	}
	var big []string
	for i := 0; i < HistoryMax+50; i++ {
		big = append(big, "cmd "+strings.Repeat("x", i%10))
	}
	os.WriteFile(p, []byte(strings.Join(big, "\n")), 0o600)
	if len(LoadHistory(p)) != HistoryMax {
		t.Fatal("cap on load")
	}
	s := New(&fakeBackend{}, Settings{HistoryPath: p})
	if len(s.editor.History()) != HistoryMax {
		t.Fatal("shell did not load history")
	}
}

func TestRunPlain(t *testing.T) {
	s, be := newTestShell(t)
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	events := make(chan link.Event, 4)
	events <- ev(link.EvFileIn, 1, "from NODEA: "+strings.Repeat("f", 120))
	done := make(chan error, 1)
	go func() { done <- s.runPlain(inR, outW, events, nil) }()
	inW.WriteString("connect nodeb\nsplit on\nquit\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	outW.Close()
	var buf bytes.Buffer
	buf.ReadFrom(outR)
	out := buf.String()
	for _, want := range []string{"gonje 9.9", "local node NODEA listening on 127.0.0.1:1175",
		"connecting to NODEB", "needs a terminal", "closing links",
		strings.Repeat("f", 120)} { // not a terminal: nothing is cut
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, c := range []string{"\x1b", "┌", "+--"} {
		if strings.Contains(out, c) {
			t.Errorf("plain output contains %q", c)
		}
	}
	if len(be.calls) != 1 {
		t.Fatalf("calls %q", be.calls)
	}
}
