// Copyright 2026 by moshix

package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseNameservers(t *testing.T) {
	in := "# Termux\nnameserver 8.8.8.8\n  nameserver   2001:4860:4860::8888 \n" +
		"search lan\nnameserver bogus\nnameserver\n;nameserver 9.9.9.9\nnameserver 8.8.4.4\n"
	got := strings.Join(parseNameservers([]byte(in)), ",")
	want := "8.8.8.8,2001:4860:4860::8888,8.8.4.4"
	if got != want {
		t.Errorf("parseNameservers = %q, want %q", got, want)
	}
}

func TestAndroidNameservers(t *testing.T) {
	files := map[string]string{}
	read := func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("no such file")
	}
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	check := func(name, want string) {
		t.Helper()
		if got := strings.Join(androidNameservers(read, getenv), ","); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}

	// nothing at all: public fallback
	check("none", "8.8.8.8,8.8.4.4")
	// Termux default prefix, as seen on the phone
	files["/data/data/com.termux/files/usr/etc/resolv.conf"] = "nameserver 8.8.8.8\nnameserver 8.8.4.4\n"
	check("termux default", "8.8.8.8,8.8.4.4")
	// $PREFIX wins over the default path
	env["PREFIX"] = "/p"
	files["/p/etc/resolv.conf"] = "nameserver 9.9.9.9\n"
	check("PREFIX", "9.9.9.9")
	// a real /etc/resolv.conf (rooted phone) wins over Termux's
	files["/etc/resolv.conf"] = "nameserver 10.0.0.1\n"
	check("etc", "10.0.0.1")
	// an /etc/resolv.conf without name servers is skipped
	files["/etc/resolv.conf"] = "# empty\n"
	check("empty etc", "9.9.9.9")
}
