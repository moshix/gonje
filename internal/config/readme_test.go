// Copyright 2026 by moshix

package config

import (
	"os"
	"strings"
	"testing"
)

// readmeKeys are the keys the loader accepts; each must be in README.md.
var readmeKeys = []string{"local_node", "local_IP", "local_port", "send_dir", "recv_dir", "log_level",
	"panel_level", "split_view", "split_ratio", "msg_pane", "msg_bell", "timeout", "route", "default_route",
	"remote_host_name", "remote_host_addr", "remote_host_port", "remote_keepalive", "remote_password",
	"remote_turn", "remote_buffersize", "remote_max_streams", "remote_line", "remote_natin",
	"remote_natout", "remote_strict_bcb"}

// TestReadmeHasEveryKey: every config key is documented, and every key
// in the list really is accepted by the loader (no "unknown key"
// warning), so the list cannot drift from the code silently.
func TestReadmeHasEveryKey(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range readmeKeys {
		if !strings.Contains(string(b), "`"+k+"`") {
			t.Errorf("README.md does not document %s", k)
		}
		if !IsKey(k) {
			t.Errorf("%s missing from Keys", k)
		}
	}
	if len(Keys) != len(readmeKeys) || IsKey("remote_hst_port") || !IsKey("Remote-Max-Streams") {
		t.Error("Keys out of step with the loader")
	}
	cfg, err := Parse(strings.NewReader(sampleAllKeys))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "unknown key") {
			t.Errorf("listed key not accepted: %s", w)
		}
	}
}

const sampleAllKeys = `local_node=ME local_IP=127.0.0.1 local_port=1175 send_dir=o recv_dir=i log_level=info
panel_level=2 split_view=yes split_ratio=40 msg_pane=6 msg_bell=no timeout=0
remote_host_name=A remote_host_addr=a remote_host_port=175 remote_keepalive=10 remote_password=x
remote_turn=free remote_buffersize=1012 remote_max_streams=2 remote_line=3 remote_natin=10.0.0.1
remote_natout=1.2.3.4 remote_strict_bcb=no
route=X,A default_route=A
`
