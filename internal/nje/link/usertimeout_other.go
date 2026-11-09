// Copyright 2026 by moshix

//go:build !linux

package link

import (
	"net"
	"time"
)

// setUserTimeout: only Linux has TCP_USER_TIMEOUT. Elsewhere a vanished
// peer is found by the keepalive probes when the link is idle, or by the
// system's retransmission timeout while data is unacknowledged.
func setUserTimeout(tc *net.TCPConn, d time.Duration) error { return nil }
