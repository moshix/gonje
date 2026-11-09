// Copyright 2026 by moshix

package node

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gonje/internal/nje/records"
)

// maxResponseLine is the NMR text limit for one response line.
const maxResponseLine = records.NMRMaxText

// answer builds the response lines for an inbound command NMR.
// Supported: Q SYS, Q LINKS, Q FILES (QUERY and long forms accepted).
// Everything else gets "unsupported command".
func (n *Node) answer(m *records.NMR) []string {
	text := strings.TrimSpace(m.Text())
	var lines []string
	if m.IsFormatted() {
		lines = []string{n.localName() + ": formatted commands are not supported"}
	} else {
		w := strings.Fields(strings.ToUpper(text))
		if len(w) == 2 && (w[0] == "Q" || w[0] == "QUERY") {
			switch w[1] {
			case "SYS", "SYSTEM":
				lines = n.querySys()
			case "LINKS", "LINK":
				lines = n.queryLinks()
			case "FILES", "FILE":
				lines = n.queryFiles()
			}
		}
		if lines == nil {
			lines = []string{fmt.Sprintf("%s: unsupported command: %s", n.localName(), text)}
		}
	}
	for i, l := range lines {
		if len(l) > maxResponseLine {
			lines[i] = l[:maxResponseLine]
		}
	}
	return lines
}

func (n *Node) querySys() []string {
	up := 0
	st := n.Status()
	for _, s := range st {
		if s.State == Up {
			up++
		}
	}
	addr := "?"
	if l := n.Local().Listen; l != "" {
		addr = l
	}
	return []string{
		fmt.Sprintf("%s: gonje %s NJE node, up %s, %d of %d links active",
			n.localName(), Version, time.Since(n.start).Round(time.Second), up, len(st)),
		fmt.Sprintf("%s: listening on %s, time %s", n.localName(), addr,
			time.Now().Format("2006-01-02 15:04:05")),
	}
}

func (n *Node) queryLinks() []string {
	st := n.Status()
	lines := []string{fmt.Sprintf("%s: %d link(s) defined", n.localName(), len(st))}
	for _, s := range st {
		l := fmt.Sprintf("  %-8s %-10s %s", s.Name, s.State, s.Addr)
		if s.State == Up {
			l += " since " + s.Since.Format("15:04:05")
		}
		lines = append(lines, l)
	}
	return lines
}

func (n *Node) queryFiles() []string {
	inbox := 0
	if ents, err := os.ReadDir(n.cfg.RecvDir); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				inbox++
			}
		}
	}
	lines := []string{fmt.Sprintf("%s: %d file(s) received", n.localName(), inbox)}
	for _, s := range n.Status() {
		lines = append(lines, fmt.Sprintf("  %d file(s) queued for %s", s.Queued, s.Name))
	}
	return lines
}
