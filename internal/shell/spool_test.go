// Copyright 2026 by moshix

package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gonje/internal/node"
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
	s.Exec("spool") // ids by age: OLD 1, LEGACY 2, NEW 3
	d := joinText(s.Exec("spool 3").Lines)
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
	if r := s.Exec("spool DELETE 3"); r.Lines[0].Color != Green {
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

// TestSpoolStableIDs: a file that arrives between "spool" and "spool
// delete <id>" does not renumber anything (second review M6).
func TestSpoolStableIDs(t *testing.T) {
	s, _ := newTestShell(t)
	dir := spoolDir(t)
	s.set.RecvDir = dir
	first := s.Exec("spool").Lines
	if !strings.HasPrefix(strings.TrimSpace(first[1].Text), "3 ") || !strings.Contains(first[1].Text, "NODEB/CARL") {
		t.Fatalf("newest is not #3:\n%s", joinText(first))
	}
	// A newer file arrives (saved by the node: next id).
	id, err := node.NextSpoolID(dir)
	if err != nil || id != 4 {
		t.Fatalf("next id %d %v", id, err)
	}
	newer := filepath.Join(dir, "NODEC.X.LATEST.20261109130000.txt")
	os.WriteFile(newer, []byte("x\n"), 0o644)
	os.WriteFile(newer+".meta", []byte("id=4\nreceived=2026-11-09T13:00:00Z\nfrom_node=NODEC\n"), 0o644)
	if r := s.Exec("spool delete 3"); r.Lines[0].Color != Green || !strings.Contains(r.Lines[0].Text, "NODEB.JOB.NEW") {
		t.Fatalf("%+v", r.Lines)
	}
	if _, err := os.Stat(newer); err != nil {
		t.Fatal("the newer file was deleted")
	}
	// Ids of legacy files persist in their sidecar.
	if b, _ := os.ReadFile(filepath.Join(dir, "HOUCICS.HERC01.LEGACY.20261109110000.txt.meta")); string(b) != "id=2\n" {
		t.Fatalf("legacy sidecar %q", b)
	}
}

// TestSpoolForwards: forward spool files are listed with their state; a
// held one can be deleted, a queued one cannot.
func TestSpoolForwards(t *testing.T) {
	s, _ := newTestShell(t)
	dir := t.TempDir()
	s.set.RecvDir = dir
	fdir := node.ForwardDir(dir)
	os.MkdirAll(fdir, 0o755)
	os.WriteFile(filepath.Join(fdir, "fwd-000007.json"), []byte(`{"id":7,"dest":"FAR","arrived":"NODEB","state":"held",`+
		`"reason":"no route to FAR","stored":"2026-11-09T12:00:00Z","file":{"OrigNode":"NODEB","FromUser":"BOB","ToUser":"U","DSName":"X.Y","Records":["AQID"]}}`), 0o644)
	os.WriteFile(filepath.Join(fdir, "fwd-000008.json"), []byte(`{"id":8,"dest":"FAR","via":"NODEC","state":"queued",`+
		`"stored":"2026-11-09T12:01:00Z","file":{"OrigNode":"NODEB","Records":[]}}`), 0o644)
	s.width = 200
	text := joinText(s.Exec("spool").Lines)
	for _, want := range []string{"STATUS", "held (forward failed: no route to FAR)", "forwarding via NODEC", "FAR/U", "forward"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lacks %q:\n%s", want, text)
		}
	}
	if r := s.Exec("spool delete 8"); r.Lines[0].Color != Red {
		t.Fatal("queued forward deleted")
	}
	if r := s.Exec("spool delete 7"); r.Lines[0].Color != Green {
		t.Fatalf("%+v", r.Lines)
	}
	if list, _ := node.ForwardEntries(dir); len(list) != 1 || list[0].ID != 8 {
		t.Fatalf("%+v", list)
	}
}
