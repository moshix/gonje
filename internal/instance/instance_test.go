// Copyright 2026 by moshix

package instance

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gonje/internal/config"
	"gonje/internal/logx"
	"gonje/internal/node"
)

// testCfg writes an empty config file and returns a config for it.
func testCfg(t *testing.T, port int) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gonje.cnf")
	os.WriteFile(p, []byte("local_node=MOSHIX\n"), 0o644)
	cfg := config.Defaults()
	cfg.LocalNode, cfg.LocalIP, cfg.LocalPort = "MOSHIX", "127.0.0.1", port
	return cfg, p
}

// deadPID returns a pid that is not running.
func deadPID(t *testing.T) int {
	for pid := 999999; pid > 900000; pid-- {
		if !Alive(pid) {
			return pid
		}
	}
	t.Skip("no free pid found")
	return 0
}

func TestAcquireStaleAndLive(t *testing.T) {
	cfg, p := testCfg(t, 1750)
	me := NewInfo("9.9", cfg, p)
	// A stale file (dead pid) is removed and reported.
	dead := me
	dead.PID = deadPID(t)
	os.WriteFile(PidPath(p), dead.encode(), 0o644)
	l, stale, err := Acquire(p, me)
	if err != nil || l == nil || stale == nil || stale.PID != dead.PID {
		t.Fatalf("stale: %v %v %v", l, stale, err)
	}
	got, err := Read(PidPath(p))
	if err != nil || got.PID != os.Getpid() || got.Node != "MOSHIX" || got.Listen != "127.0.0.1:1750" ||
		got.Config != absPath(p) || got.Version != "9.9" {
		t.Fatalf("pid file %+v %v", got, err)
	}
	// A second instance (another pid, same config) is refused while we
	// (a live process) hold the file, with the operator message.
	other := me
	other.PID = os.Getpid() + 1
	_, _, err = Acquire(p, other)
	var running *ErrRunning
	if err == nil || !strings.Contains(err.Error(), "another gonje (pid "+strconv.Itoa(os.Getpid())+", node MOSHIX, started ") ||
		!strings.Contains(err.Error(), ") is already running with this config on 127.0.0.1:1750") {
		t.Fatalf("live: %v", err)
	}
	if e, ok := err.(*ErrRunning); ok {
		running = e
	}
	if running == nil || running.Info.PID != os.Getpid() {
		t.Fatal("not an ErrRunning")
	}
	// Another config in the same directory: no lock, no error, file kept.
	o2 := other
	o2.Config = filepath.Join(filepath.Dir(p), "other.cnf")
	if l2, _, err := Acquire(o2.Config, o2); err != nil || l2 != nil {
		t.Fatalf("other config: %v %v", l2, err)
	}
	// Release removes only our own file.
	l.Release()
	if _, err := os.Stat(PidPath(p)); !os.IsNotExist(err) {
		t.Fatal("pid file not removed")
	}
	var nilLock *Lock
	nilLock.Release()
}

// TestDiagnose: the bind failure is explained: a running gonje, an NJE
// node answering the probe, or another program; plus the remote-port
// hint.
func TestDiagnose(t *testing.T) {
	// Another program: a listener that never answers.
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	port := plain.Addr().(*net.TCPAddr).Port
	cfg, p := testCfg(t, port)
	cfg.Remotes = []*config.Remote{{Name: "PEER", Addr: "127.0.0.1", Port: port, Keepalive: 5}}
	_, bindErr := net.Listen("tcp", ListenAddr(cfg))
	if bindErr == nil {
		t.Fatal("bind did not fail")
	}
	msg := Diagnose(cfg, p, bindErr)
	want := "address 127.0.0.1:" + strconv.Itoa(port) + " is in use by another program; port " + strconv.Itoa(port) +
		" is also configured as the port of remote node PEER — is a peer running on this machine?"
	if msg != want {
		t.Fatalf("got  %q\nwant %q", msg, want)
	}
	// A running gonje recorded in the pid file (this process, other pid
	// in the lock call).
	info := NewInfo("9.9", cfg, p)
	os.WriteFile(PidPath(p), info.encode(), 0o644)
	cfg.Remotes = nil
	if msg := Diagnose(cfg, p, bindErr); !strings.HasPrefix(msg, "another gonje (pid ") {
		// Diagnose skips our own pid: write a different live pid (1 =
		// init/launchd always exists).
		info.PID = 1
		os.WriteFile(PidPath(p), info.encode(), 0o644)
		if msg := Diagnose(cfg, p, bindErr); !strings.HasPrefix(msg, "another gonje (pid 1, node MOSHIX") {
			t.Fatalf("running: %q", msg)
		}
	}
	os.Remove(PidPath(p))

	// An NJE node: an in-process gonje node answers the OPEN probe.
	ncfg := config.Defaults()
	ncfg.LocalNode, ncfg.LocalIP, ncfg.LocalPort = "MOSHIX", "127.0.0.1", 0
	ncfg.RecvDir, ncfg.SendDir = t.TempDir(), t.TempDir()
	n := node.New(ncfg, node.Options{Log: logx.Discard(), Handshake: 2 * time.Second})
	if err := n.Listen(); err != nil {
		t.Fatal(err)
	}
	n.Start()
	defer n.Stop()
	cfg2, p2 := testCfg(t, n.Addr().(*net.TCPAddr).Port)
	_, bindErr = net.Listen("tcp", ListenAddr(cfg2))
	if msg := Diagnose(cfg2, p2, bindErr); !strings.HasPrefix(msg, "an NJE node (probably another gonje or NJE server) is listening on 127.0.0.1:") {
		t.Fatalf("NJE: %q", msg)
	}
	if ProbeNJE(plain.Addr().String(), "X", 300*time.Millisecond) {
		t.Fatal("plain listener taken for NJE")
	}
}
