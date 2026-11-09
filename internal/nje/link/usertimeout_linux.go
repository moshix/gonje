// Copyright 2026 by moshix

package link

import (
	"net"
	"syscall"
	"time"
)

// tcpUserTimeout is TCP_USER_TIMEOUT (linux/tcp.h): how long sent data
// may stay unacknowledged before the kernel drops the connection.
const tcpUserTimeout = 0x12

// setUserTimeout sets TCP_USER_TIMEOUT on a connection.
func setUserTimeout(tc *net.TCPConn, d time.Duration) error {
	raw, err := tc.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout, int(d/time.Millisecond))
	})
	if err != nil {
		return err
	}
	return serr
}
