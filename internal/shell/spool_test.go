// Copyright 2026 by moshix

package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func spoolDir(t *testing.T) string {
	dir := t.TempDir()
	write := func(name, content, meta string) {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
		if meta != "" {
			os.WriteFile(filepath.Join(dir, name+".meta"), []byte(meta), 0o644)
		}
	}
	write("NODEA.ALICE.OLD.20261109100000.txt", "old\n",
		"received=2026-11-09T10:00:00Z\nfrom_node=NODEA\nfrom_user=ALICE\nto_user=BOB\njobname=ALICE\ndsname=OLD\n"+
			"records=1\nkind=text\nrecfm=V\nlrecl=3\ncc=none\n")
	write("NODEB.JOB.NEW.20261109120000.bin", strings.Repeat("x", 3000),
		"received=2026-11-09T12:00:00Z\nfrom_node=NODEB\nfrom_user=CARL\nto_user=BOB\njobname=JOB\ndsname=NEW\n"+
			"records=38\nkind=binary\nrecfm=F\nlrecl=80\ncc=none\n")
	write("HOUCICS.HERC01.LEGACY.20261109110000.txt", "legacy\n", "") // no sidecar: its mtime counts
	mt := time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC)
	os.Chtimes(filepath.Join(dir, "HOUCICS.HERC01.LEGACY.20261109110000.txt"), mt, mt)
	os.WriteFile(filepath.Join(dir, "x.part"), nil, 0o644)
	return dir
}

func TestSpoolListing(t *testing.T) {
	s, _ := newTestShell(t)
	s.set.RecvDir = spoolDir(t)
	lines := s.Exec("spool").Lines
	if len(lines) != 4 {
		t.Fatalf("%d lines:\n%s", len(lines), joinText(lines))
	}
	// Newest first (received time, or mtime without a sidecar); sidecars
	// and .part files hidden.
	text := joinText(lines)
	if strings.Contains(text, ".meta") || strings.Contains(text, ".part") {
		t.Fatal("sidecar listed")
	}
	if !strings.Contains(lines[1].Text, "NODEB/CARL") || !strings.Contains(lines[3].Text, "NODEA/ALICE") ||
		!strings.Contains(lines[1].Text, "2.9 KB") || !strings.Contains(lines[1].Text, "F 80 none") {
		t.Fatalf("order/format:\n%s", text)
	}
	if !strings.Contains(lines[2].Text, "HOUCICS") || !strings.Contains(lines[2].Text, "LEGACY") {
		t.Fatalf("legacy file from its name:\n%s", text)
	}
	if lines[1].Segs[7].Color != Magenta || lines[3].Segs[7].Color != Green {
		t.Fatalf("kind colors %+v", lines[1].Segs)
	}
	// Narrow: low-priority columns go, # and FROM stay.
	narrow := s.spoolTable(mustEntries(t, s.set.RecvDir), 40)
	if strings.Contains(narrow[0].Text, "FORMAT") || !strings.Contains(narrow[0].Text, "FROM") {
		t.Fatalf("narrow header %q", narrow[0].Text)
	}
	if s.Exec("sp").Lines[0].Text != lines[0].Text {
		t.Fatal("alias sp")
	}
}

func mustEntries(t *testing.T, dir string) []spoolEntry {
	l, err := spoolEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestSpoolDetailAndDelete(t *testing.T) {
	s, _ := newTestShell(t)
	dir := spoolDir(t)
	s.set.RecvDir = dir
	d := joinText(s.Exec("spool 1").Lines)
	for _, want := range []string{"NODEB.JOB.NEW.20261109120000.bin", "from_user", "CARL", "lrecl", "80", filepath.Join(dir, "NODEB.JOB.NEW")} {
		if !strings.Contains(d, want) {
			t.Fatalf("detail lacks %q:\n%s", want, d)
		}
	}
	if d := joinText(s.Exec("spool HOUCICS.HERC01.LEGACY.20261109110000.txt").Lines); !strings.Contains(d, "no metadata sidecar") {
		t.Fatal(d)
	}
	// Path traversal: only names from the listing match.
	outside := filepath.Join(filepath.Dir(dir), "victim.txt")
	os.WriteFile(outside, []byte("keep"), 0o644)
	for _, bad := range []string{"../victim.txt", outside, "9", "0", "NODEA.ALICE.OLD.20261109100000.txt.meta"} {
		if r := s.Exec("spool delete " + bad); r.Lines[0].Color != Red {
			t.Errorf("delete %q accepted", bad)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("file outside recv_dir deleted")
	}
	if r := s.Exec("spool delete 1"); r.Lines[0].Color != Green {
		t.Fatalf("%+v", r.Lines)
	}
	for _, p := range []string{"NODEB.JOB.NEW.20261109120000.bin", "NODEB.JOB.NEW.20261109120000.bin.meta"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Fatalf("%s not deleted", p)
		}
	}
	if len(mustEntries(t, dir)) != 2 {
		t.Fatal("wrong file deleted")
	}
}
