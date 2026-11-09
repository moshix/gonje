package node

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gonje/internal/nje/link"
)

// bigFile writes a random binary file large enough that its transfer is
// still running when the test acts on it.
func bigFile(t *testing.T, dir string, mb int) string {
	t.Helper()
	b := make([]byte, mb<<20)
	rand.New(rand.NewSource(7)).Read(b)
	p := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// waitTransfer polls until n has an outbound (out) or inbound transfer
// running on its link to peer.
func waitTransfer(t *testing.T, n *testNode, peer string, out bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, st := range n.Status() {
			if st.Name == peer && ((out && st.Stats.ActiveOut > 0) || (!out && st.Stats.ActiveIn > 0)) {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: no transfer running to %s", n.cfg.LocalNode, peer)
}

func statusOf(n *testNode, peer string) LinkStatus {
	for _, st := range n.Status() {
		if st.Name == peer {
			return st
		}
	}
	return LinkStatus{}
}

func waitState(t *testing.T, n *testNode, peer string, want State) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if statusOf(n, peer).State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: link to %s is %v, want %v", n.cfg.LocalNode, peer, statusOf(n, peer).State, want)
}

func TestDrainIdleDropsAtOnce(t *testing.T) {
	a, b := startPair(t, 5)
	msg, err := a.Drain("nodeb")
	if err != nil || !strings.Contains(msg, "no transfer in progress") {
		t.Fatalf("%q %v", msg, err)
	}
	waitState(t, a, "NODEB", Down)
	waitState(t, b, "NODEA", Down)
	time.Sleep(500 * time.Millisecond) // no auto-reconnect, like drop
	if st := statusOf(a, "NODEB"); st.State != Down || !st.Held {
		t.Fatalf("after drain: %+v", st)
	}
	if _, err := a.Drain("NODEB"); err == nil {
		t.Fatal("drain of a down link accepted")
	}
}

// TestDrainOutboundInFlight: our big file finishes, a file queued while
// draining is not started, the peer's new file is refused and kept by
// the peer, then the link drops.
func TestDrainOutboundInFlight(t *testing.T) {
	a, b := startPair(t, 5)
	if _, err := a.SendFile("NODEB", "BOB", bigFile(t, a.dir, 4)); err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, a, "NODEB", true)
	msg, err := a.Drain("NODEB")
	if err != nil || !strings.Contains(msg, "waiting for 0 inbound, 1 outbound") {
		t.Fatalf("%q %v", msg, err)
	}
	if st := statusOf(a, "NODEB"); st.State != Draining {
		t.Fatalf("state %v", st.State)
	}
	// Queued while draining: must not start.
	small := filepath.Join(a.dir, "later.txt")
	os.WriteFile(small, []byte("later\n"), 0o644)
	a.SendFile("NODEB", "BOB", small)
	// The peer offers a file: refused, it keeps it.
	theirs := filepath.Join(b.dir, "theirs.txt")
	os.WriteFile(theirs, []byte("theirs\n"), 0o644)
	b.SendFile("NODEA", "ALICE", theirs)
	b.wait(t, "refusal", 20*time.Second, kindText(link.EvStream, "stays queued"))

	b.wait(t, "big file received", 30*time.Second, kindText(link.EvFileIn, "BIG"))
	waitState(t, a, "NODEB", Down)
	waitState(t, b, "NODEA", Down)
	if q := statusOf(a, "NODEB").Queued; q != 1 {
		t.Fatalf("A queue %d, want the file queued during the drain", q)
	}
	if q := statusOf(b, "NODEA").Queued; q != 1 {
		t.Fatalf("B queue %d, want its refused file kept", q)
	}
	if strings.Contains(a.logBuf.String(), "sending later.txt") {
		t.Fatal("queued file was started during the drain")
	}
	if !strings.Contains(a.logBuf.String(), "drain complete") {
		t.Fatal("drain completion not logged")
	}
}

func TestDrainInboundInFlight(t *testing.T) {
	a, b := startPair(t, 5)
	b.SendFile("NODEA", "ALICE", bigFile(t, b.dir, 4))
	waitTransfer(t, a, "NODEB", false)
	msg, err := a.Drain("NODEB")
	if err != nil || !strings.Contains(msg, "waiting for 1 inbound, 0 outbound") {
		t.Fatalf("%q %v", msg, err)
	}
	a.wait(t, "big file received", 30*time.Second, kindText(link.EvFileIn, "BIG"))
	waitState(t, a, "NODEB", Down)
	if q := statusOf(b, "NODEA").Queued; q != 0 {
		t.Fatalf("B still has %d queued: the file was not completed", q)
	}
}

func TestDrainCancelAndForce(t *testing.T) {
	a, b := startPair(t, 5)
	a.SendFile("NODEB", "BOB", bigFile(t, a.dir, 4))
	waitTransfer(t, a, "NODEB", true)
	if _, err := a.Drain("NODEB"); err != nil {
		t.Fatal(err)
	}
	// connect cancels the drain: the link stays up after the file.
	if err := a.Connect("NODEB"); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(a, "NODEB"); st.State != Up || st.Held {
		t.Fatalf("after cancel %+v", st)
	}
	b.wait(t, "file", 30*time.Second, kindText(link.EvFileIn, "BIG"))
	time.Sleep(300 * time.Millisecond)
	if statusOf(a, "NODEB").State != Up {
		t.Fatal("link dropped after a cancelled drain")
	}
	// Drain, then drop: immediate, the file stays queued at the sender.
	a.SendFile("NODEB", "BOB", filepath.Join(a.dir, "big.bin"))
	waitTransfer(t, a, "NODEB", true)
	a.Drain("NODEB")
	if err := a.Drop("NODEB"); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, "NODEB", Down)
	if q := statusOf(a, "NODEB").Queued; q != 1 {
		t.Fatalf("forced drop: queue %d, want the unfinished file kept", q)
	}
	// "drain" twice also forces.
	a.Connect("NODEB")
	a.waitUp(t, "NODEB")
	waitTransfer(t, a, "NODEB", true)
	a.Drain("NODEB")
	msg, err := a.Drain("NODEB")
	if err != nil || !strings.Contains(msg, "forced") {
		t.Fatalf("%q %v", msg, err)
	}
	waitState(t, a, "NODEB", Down)
}
