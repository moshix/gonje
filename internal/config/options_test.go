// Copyright 2026 by moshix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOptionsOnly: one -o string with several remotes, no config file.
func TestOptionsOnly(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gonje.cnf")
	o := "local_node=MOSHIX local_IP=10.157.58.220 local_port=1750 remote_host_name=HOUCICS " +
		"remote_host_addr=10.157.58.220 remote_host_port=4002 remote_keepalive=10,remote_host_name=NODEB\n" +
		"remote_host_addr=b.example remote_password=sekret route=FAR,NODEB,HOUCICS default_route=HOUCICS"
	c, hasFile, err := LoadWithOptions(missing, false, []string{o})
	if err != nil || hasFile {
		t.Fatalf("%v %v", hasFile, err)
	}
	if c.LocalNode != "MOSHIX" || c.LocalIP != "10.157.58.220" || c.LocalPort != 1750 || len(c.Remotes) != 2 {
		t.Fatalf("%+v", c)
	}
	h, b := c.Remote("HOUCICS"), c.Remote("NODEB")
	if h.Port != 4002 || h.Addr != "10.157.58.220" || b.Addr != "b.example" || b.Port != 175 || b.Password != "SEKRET" {
		t.Fatalf("%+v %+v", h, b)
	}
	if len(c.Routes) != 1 || c.Routes[0].Alt != "HOUCICS" || c.DefaultRoute != "HOUCICS" {
		t.Fatalf("%+v %q", c.Routes, c.DefaultRoute)
	}
	// Repeated -o: concatenated in order.
	c, _, err = LoadWithOptions(missing, false, []string{"local_node=A remote_host_name=B", "remote_host_port=99"})
	if err != nil || c.Remote("B").Port != 99 {
		t.Fatalf("%v", err)
	}
	// Without -o, or with -c/-f naming the file, a missing file is an error.
	if _, _, err := LoadWithOptions(missing, false, nil); err == nil {
		t.Fatal("missing file accepted without -o")
	}
	if _, _, err := LoadWithOptions(missing, true, []string{o}); err == nil {
		t.Fatal("missing explicit file accepted")
	}
}

// TestOptionsOverrideFile: -o values override the file, a remote named
// again is extended, a new one is added.
func TestOptionsOverrideFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gonje.cnf")
	os.WriteFile(p, []byte("local_node=ME local_port=175\nremote_host_name=A\nremote_host_addr=a\nremote_keepalive=10\n"), 0o644)
	c, hasFile, err := LoadWithOptions(p, true, []string{"local_port=1750 remote_host_name=A remote_keepalive=30 remote_host_name=C remote_host_addr=c"})
	if err != nil || !hasFile {
		t.Fatal(err)
	}
	if c.LocalPort != 1750 || len(c.Remotes) != 2 || c.Remote("A").Keepalive != 30 || c.Remote("A").Addr != "a" || c.Remote("C").Addr != "c" {
		t.Fatalf("%+v %+v", c, c.Remotes)
	}
}

// TestOptionsValidation: the same checks as the file, naming -o.
func TestOptionsValidation(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gonje.cnf")
	for o, want := range map[string]string{
		"local_node=ME default_route=NOPE": "-o: default_route NOPE is not a configured remote node",
		"local_node=ME local_port=99999":   "-o: local_port",
		"local_node=TOOLONGNAME":           "-o: local_node",
		"remote_host_name=A":               "-o: local_node is required",
		"local_node=ME remote_keepalive=5": "-o: remote_keepalive before remote_host_name",
		"local_node=ME remote_host_name=A remote_line=1 remote_host_name=B remote_line=1": "-o: line 1 used by both",
		"local_node=ME bogus": "-o: expected key=value",
	} {
		_, _, err := LoadWithOptions(missing, false, []string{o})
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v, want %q", o, err, want)
		}
	}
	// A password in -o is masked for the log.
	if m := MaskSecrets("local_node=ME remote_password=sekret"); strings.Contains(m, "sekret") {
		t.Fatal(m)
	}
}
