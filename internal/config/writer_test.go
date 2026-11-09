// Copyright 2026 by moshix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const editSample = `# gonje test config
local_node=NODEA      # our name
local_port=1175

# the B link
remote_host_name=NODEB
remote_host_addr=127.0.0.1   # loopback
remote_host_port=2175
remote_keepalive=10
custom_key=1

# the C link, compact
remote_host_name=NODEC, remote_host_addr=c.example, remote_host_name=NODED
remote_host_addr=d.example
`

func writeTemp(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gonje.cnf")
	if err := os.WriteFile(p, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func load(t *testing.T, p string) *Config {
	t.Helper()
	c, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return c
}

func TestEditRemoteKeepsComments(t *testing.T) {
	p := writeTemp(t, editSample, 0o640)
	f, err := OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f.SetRemote(&Remote{Name: "NODEB", Addr: "10.0.0.2", Port: 3175, Keepalive: 5, StrictBCB: true, Turn: "strict"})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	text := string(b)
	for _, want := range []string{"# gonje test config", "local_node=NODEA      # our name", "# the B link",
		"remote_host_addr=10.0.0.2   # loopback", "remote_host_port=3175", "remote_keepalive=5",
		"custom_key=1", "remote_strict_bcb=yes", "# the C link, compact"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	c := load(t, p)
	r := c.Remote("NODEB")
	if r.Addr != "10.0.0.2" || r.Port != 3175 || r.Keepalive != 5 || !r.StrictBCB || r.Turn != "strict" {
		t.Fatalf("%+v", r)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %o", st.Mode().Perm())
	}
}

func TestEditAddDeleteRemote(t *testing.T) {
	p := writeTemp(t, editSample, 0o644)
	f, _ := OpenFile(p)
	f.SetRemote(&Remote{Name: "NODEE", Addr: "e.example", Port: 175, Keepalive: 10, Password: "PW1"})
	if !f.DeleteRemote("nodec") || f.DeleteRemote("NOPE") {
		t.Fatal("delete result")
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	c := load(t, p)
	if c.Remote("NODEC") != nil || c.Remote("NODED") == nil || c.Remote("NODED").Addr != "d.example" {
		t.Fatalf("shared-line delete broke NODED: %+v", c.Remotes)
	}
	e := c.Remote("NODEE")
	if e == nil || e.Password != "PW1" || e.Addr != "e.example" {
		t.Fatalf("added remote %+v", e)
	}
	// A password in the file makes it 0600.
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o, want 0600", st.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "# the B link") || !strings.Contains(string(b), "custom_key=1") {
		t.Fatal("unrelated lines lost")
	}
	// Removing the password and the address deletes their tokens.
	f, _ = OpenFile(p)
	f.SetRemote(&Remote{Name: "NODEE", Port: 175, Keepalive: 10})
	f.Save()
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "PW1") || strings.Contains(string(b), "e.example") {
		t.Fatalf("cleared keys still there:\n%s", b)
	}
	// Deleting a whole block removes its comment lines and leaves the rest.
	f, _ = OpenFile(p)
	f.DeleteRemote("NODEB")
	f.Save()
	c = load(t, p)
	if c.Remote("NODEB") != nil || c.LocalNode != "NODEA" || len(c.Remotes) != 2 {
		t.Fatalf("after delete: %+v", c.Remotes)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "loopback") {
		t.Fatalf("block comment left behind:\n%s", b)
	}
}

func TestEditLocal(t *testing.T) {
	p := writeTemp(t, editSample, 0o644)
	f, _ := OpenFile(p)
	f.SetLocal("local_node", "NEWNAME")
	f.SetLocal("local_IP", "127.0.0.1") // not in the file yet
	f.SetLocal("local_port", "1999")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	c := load(t, p)
	if c.LocalNode != "NEWNAME" || c.LocalIP != "127.0.0.1" || c.LocalPort != 1999 {
		t.Fatalf("%+v", c)
	}
	b, _ := os.ReadFile(p)
	lines := strings.Split(string(b), "\n")
	if lines[1] != "local_node=NEWNAME      # our name" || lines[2] != "local_port=1999" || lines[3] != "local_IP=127.0.0.1" {
		t.Fatalf("local keys placed wrong:\n%s", b)
	}
	// File with only remote blocks: the new key goes above the first one.
	p = writeTemp(t, "remote_host_name=X\n", 0o644)
	f, _ = OpenFile(p)
	f.SetLocal("local_node", "A")
	f.Save()
	if b, _ := os.ReadFile(p); string(b) != "local_node=A\nremote_host_name=X\n" {
		t.Fatalf("%q", b)
	}
}

func TestEditRefusesBrokenResult(t *testing.T) {
	p := writeTemp(t, editSample, 0o644)
	f, _ := OpenFile(p)
	f.SetLocal("local_node", "TOOLONGNAME")
	if err := f.Save(); err == nil {
		t.Fatal("saved a config that does not load")
	}
	if b, _ := os.ReadFile(p); string(b) != editSample {
		t.Fatal("original file modified")
	}
}

// TestEditRepoSample edits a temporary copy of test/nodeA.cnf.
func TestEditRepoSample(t *testing.T) {
	orig, err := os.ReadFile("../../test/nodeA.cnf")
	if err != nil {
		t.Skip("test/nodeA.cnf missing")
	}
	p := writeTemp(t, string(orig), 0o644)
	f, _ := OpenFile(p)
	f.SetRemote(&Remote{Name: "NODEB", Addr: "127.0.0.1", Port: 2175, Keepalive: 30})
	f.SetRemote(&Remote{Name: "NODEZ", Addr: "z", Port: 175, Keepalive: 10})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	c := load(t, p)
	if c.Remote("NODEB").Keepalive != 30 || c.Remote("NODEZ") == nil || len(c.Warnings) != 0 {
		t.Fatalf("%+v", c)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), strings.SplitN(string(orig), "\n", 2)[0]) {
		t.Fatal("header comment lost")
	}
}

func TestValidateRemote(t *testing.T) {
	ok := &Remote{Name: "nodeb", Port: 175, Keepalive: 10, Password: "pw"}
	if err := ValidateRemote(ok, "NODEA"); err != nil || ok.Name != "NODEB" || ok.Password != "PW" {
		t.Fatal(err)
	}
	bad := []Remote{
		{Name: "TOOLONGNAME", Port: 1, Keepalive: 1},
		{Name: "NODEA", Port: 1, Keepalive: 1},
		{Name: "B", Port: 0, Keepalive: 1},
		{Name: "B", Port: 1, Keepalive: 0},
		{Name: "B", Port: 1, Keepalive: 1, Password: "123456789"},
		{Name: "B", Port: 1, Keepalive: 1, Password: "a b"},
		{Name: "B", Addr: "a,b", Port: 1, Keepalive: 1},
	}
	for _, r := range bad {
		if err := ValidateRemote(&r, "NODEA"); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

func TestBufferSizeOption(t *testing.T) {
	c, err := Parse(strings.NewReader("local_node=A REMOTE_HOST_NAME=B REMOTE_BUFFERSIZE=1012"))
	if err != nil || c.Remote("B").BufSize != 1012 {
		t.Fatalf("%v %+v", err, c)
	}
	for _, bad := range []string{"299", "32769", "x"} {
		if _, err := Parse(strings.NewReader("local_node=A remote_host_name=B remote_buffersize=" + bad)); err == nil {
			t.Errorf("buffersize %s accepted", bad)
		}
	}
	if err := ValidateRemote(&Remote{Name: "B", Port: 1, Keepalive: 1, BufSize: 100}, "A"); err == nil {
		t.Fatal("ValidateRemote accepted 100")
	}
	p := writeTemp(t, "local_node=A\nremote_host_name=B\n", 0o644)
	f, _ := OpenFile(p)
	f.SetRemote(&Remote{Name: "B", Port: 175, Keepalive: 10, BufSize: 4096})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if c := load(t, p); c.Remote("B").BufSize != 4096 {
		t.Fatal("buffersize not saved")
	}
}

func TestMaxStreamsTimeoutAndOldKeys(t *testing.T) {
	c, err := Parse(strings.NewReader("local_node=A timeout=120 remote_host_name=B remote_max-streams=3 " +
		"remote_host_name=C REMOTE_MAX_STREAMS=8 remote_eof_wait=10"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != 120 || c.Remote("B").MaxStreams != 3 || c.Remote("C").MaxStreams != 8 {
		t.Fatalf("%+v %+v %+v", c, c.Remote("B"), c.Remote("C"))
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "remote_eof_wait") {
		t.Fatalf("old key not warned: %v", c.Warnings)
	}
	for _, bad := range []string{"remote_host_name=B remote_max_streams=0", "remote_host_name=B remote_max_streams=9", "timeout=-1"} {
		if _, err := Parse(strings.NewReader("local_node=A " + bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	p := writeTemp(t, "local_node=A\nremote_host_name=B remote_max-streams=2\n", 0o644)
	f, _ := OpenFile(p)
	f.SetLocal("timeout", "300")
	f.SetRemote(&Remote{Name: "B", Port: 175, Keepalive: 10, MaxStreams: 4})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	c = load(t, p)
	if c.Timeout != 300 || c.Remote("B").MaxStreams != 4 {
		t.Fatalf("saved %+v %+v", c, c.Remote("B"))
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "remote_max-streams=4") {
		t.Fatalf("hyphen key not updated in place:\n%s", b)
	}
}

func TestRoutesConfig(t *testing.T) {
	text := "local_node=A\nroute=MVSF,MVSB,MVSC   # via B, else C\nroute=far,mvsb\ndefault_route=MVSC\n" +
		"remote_host_name=MVSB\nremote_host_name=MVSC\n"
	c, err := Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Routes) != 2 || c.Routes[0] != (Route{"MVSF", "MVSB", "MVSC"}) || c.Routes[1] != (Route{"FAR", "MVSB", ""}) ||
		c.DefaultRoute != "MVSC" {
		t.Fatalf("%+v %q", c.Routes, c.DefaultRoute)
	}
	bad := []string{
		"local_node=A default_route=NOPE remote_host_name=B",  // undefined default route
		"local_node=A route=X,NOPE remote_host_name=B",        // via not a link
		"local_node=A route=X,B,NOPE remote_host_name=B",      // alt not a link
		"local_node=A route=A,B remote_host_name=B",           // to ourselves
		"local_node=A route=B,B remote_host_name=B",           // via itself
		"local_node=A route=X remote_host_name=B",             // no via
		"local_node=A route=X,B route=X,B remote_host_name=B", // twice
		"local_node=A route=TOOLONGNAME,B remote_host_name=B", // bad name
	}
	for _, b := range bad {
		if _, err := Parse(strings.NewReader(b)); err == nil {
			t.Errorf("accepted %q", b)
		}
	}
	// Writer round trip: change, add, delete routes; default route.
	p := writeTemp(t, text, 0o644)
	f, _ := OpenFile(p)
	f.SetRoute(Route{"MVSF", "MVSC", ""})
	f.SetRoute(Route{"NEW", "MVSB", "MVSC"})
	f.DeleteRoute("far")
	f.SetLocal("default_route", "MVSB")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	c = load(t, p)
	if len(c.Routes) != 2 || c.Routes[0] != (Route{"MVSF", "MVSC", ""}) || c.Routes[1] != (Route{"NEW", "MVSB", "MVSC"}) ||
		c.DefaultRoute != "MVSB" {
		t.Fatalf("after save %+v %q", c.Routes, c.DefaultRoute)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "route=MVSF,MVSC   # via B, else C") {
		t.Fatalf("route comment lost:\n%s", b)
	}
	f, _ = OpenFile(p)
	f.DeleteLocal("default_route")
	f.Save()
	if load(t, p).DefaultRoute != "" {
		t.Fatal("default route not deleted")
	}
	// First route in a file without any: placed before the remote blocks.
	p = writeTemp(t, "local_node=A\nremote_host_name=B\n", 0o644)
	f, _ = OpenFile(p)
	f.SetRoute(Route{"X", "B", ""})
	f.Save()
	if b, _ := os.ReadFile(p); string(b) != "local_node=A\nroute=X,B\nremote_host_name=B\n" {
		t.Fatalf("%q", b)
	}
}
