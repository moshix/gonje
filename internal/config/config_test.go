// Copyright 2026 by moshix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `
# sample
local_node=mynode
local_IP=0.0.0.0, local_port=1175
send_dir=./out recv_dir=./in
log_level=info
split_view=yes
split_ratio=30
panel_level=2
bogus_key=1
remote_host_name=remnode          # starts a new remote block
remote_host_addr=remote.node.com
remote_host_port=2175
remote_keepalive=5
remote_password=
remote_host_name=OTHER
remote_password=se#cr  # '#' inside a value is kept
remote_strict_bcb=yes
remote_turn=strict
`

func TestParseSample(t *testing.T) {
	c, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.LocalNode != "MYNODE" || c.LocalIP != "0.0.0.0" || c.LocalPort != 1175 {
		t.Fatalf("local: %+v", c)
	}
	if c.SendDir != "./out" || c.RecvDir != "./in" || c.LogLevel != "info" {
		t.Fatalf("dirs: %+v", c)
	}
	if !c.SplitView || c.SplitRatio != 30 || c.PanelLevel != 2 {
		t.Fatalf("view: %+v", c)
	}
	if len(c.Remotes) != 2 {
		t.Fatalf("remotes: %d", len(c.Remotes))
	}
	r := c.Remotes[0]
	if r.Name != "REMNODE" || r.Addr != "remote.node.com" || r.Port != 2175 ||
		r.Keepalive != 5 || r.Password != "" {
		t.Fatalf("remote0: %+v", r)
	}
	r = c.Remote("other")
	if r == nil || r.Port != 175 || r.Keepalive != 10 || r.Password != "SE#CR" || r.Addr != "" ||
		!r.StrictBCB || c.Remotes[0].StrictBCB || r.Turn != "strict" || c.Remotes[0].Turn != "" {
		t.Fatalf("remote1: %+v", r)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "bogus_key") {
		t.Fatalf("warnings: %v", c.Warnings)
	}
}

func TestDefaults(t *testing.T) {
	c, err := Parse(strings.NewReader("local_node=A"))
	if err != nil {
		t.Fatal(err)
	}
	if c.LocalPort != 175 || c.SendDir != "./outbox" || c.RecvDir != "./inbox" ||
		!c.SplitView || c.SplitRatio != 40 || c.PanelLevel != 1 || c.LogLevel != "debug" {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestErrors(t *testing.T) {
	bad := []string{
		"",                                // no local node
		"local_node=TOOLONGNAME",          // > 8
		"local_node=BAD!",                 // bad char
		"local_node=A local_port=x",       // not a number
		"local_node=A local_port=70000",   // range
		"local_node=A remote_host_port=1", // remote key before block
		"local_node=A remote_host_name=A", // same as local
		"local_node=A remote_host_name=B remote_host_name=b",
		"local_node=A panel_level=4",
		"local_node=A split_view=maybe",
		"local_node=A log_level=loud",
		"local_node=A justaword",
		"local_node=A remote_host_name=B remote_password=123456789",
		"local_node=A remote_host_name=B remote_strict_bcb=perhaps",
		"local_node=A remote_host_name=B remote_turn=sometimes",
	}
	for _, s := range bad {
		if _, err := Parse(strings.NewReader(s)); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

func TestLoadRepoSamples(t *testing.T) {
	for _, p := range []string{"../../gonje.cnf.example", "../../test/nodeA.cnf", "../../test/nodeB.cnf"} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s missing", p)
		}
		c, err := Load(filepath.Clean(p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(c.Warnings) != 0 {
			t.Fatalf("%s warnings: %v", p, c.Warnings)
		}
	}
}
