// Copyright 2026 by moshix

package link

import (
	"net"
	"testing"
	"time"
)

// TestSetUserTimeout: the option can be set on a real TCP connection
// (Linux; a no-op elsewhere).
func TestSetUserTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := setUserTimeout(c.(*net.TCPConn), 35*time.Second); err != nil {
		t.Fatal(err)
	}
}
