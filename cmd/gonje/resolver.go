// Copyright 2026 by moshix

package main

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
)

// fallbackDNS is the default on Android (Google public DNS, the same as
// Termux ships); a resolv.conf with name servers overrides it.
var fallbackDNS = []string{"8.8.8.8", "8.8.4.4"}

// parseNameservers returns the "nameserver" addresses of a resolv.conf,
// in file order, skipping comments and malformed lines.
func parseNameservers(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
			out = append(out, f[1])
		}
	}
	return out
}

// androidNameservers picks the DNS servers for an Android (Termux) run.
// Android has no /etc/resolv.conf: bionic asks the netd daemon instead.
// A Go binary built with CGO_ENABLED=0 cannot talk to netd, and its pure
// Go resolver then falls back to localhost:53, where nothing listens, so
// every lookup fails ("lookup moshix.tech on [::1]:53 ... connection
// refused"). Termux ships its own resolv.conf under $PREFIX, so read that
// one; readFile and getenv are parameters so tests can fake them.
func androidNameservers(readFile func(string) ([]byte, error), getenv func(string) string) []string {
	prefix := getenv("PREFIX")
	if prefix == "" {
		prefix = "/data/data/com.termux/files/usr"
	}
	for _, p := range []string{"/etc/resolv.conf", prefix + "/etc/resolv.conf"} {
		if data, err := readFile(p); err == nil {
			if ns := parseNameservers(data); len(ns) > 0 {
				return ns
			}
		}
	}
	return fallbackDNS
}

// setupResolver makes DNS work on Android; elsewhere it does nothing.
// net.Dial and net.Dialer use net.DefaultResolver, so replacing it
// covers every outgoing connection gonje makes.
func setupResolver() {
	if runtime.GOOS != "android" {
		return
	}
	servers := androidNameservers(os.ReadFile, os.Getenv)
	d := net.Dialer{Timeout: 3 * time.Second}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		// The Go resolver calls Dial with the server it would use
		// (localhost:53); ignore that and try our servers in order,
		// keeping the network (udp or tcp) the resolver asked for.
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var err error
			for _, s := range servers {
				var c net.Conn
				if c, err = d.DialContext(ctx, network, net.JoinHostPort(s, "53")); err == nil {
					return c, nil
				}
			}
			return nil, err
		},
	}
}
