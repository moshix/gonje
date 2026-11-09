package node

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gonje/internal/ebcdic"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
)

func TestIsText(t *testing.T) {
	cases := []struct {
		data []byte
		want bool
	}{
		{nil, true},
		{[]byte("hello world\nline two\r\n\ttabbed\n"), true},
		{[]byte("almost text\x00"), false},
		{[]byte{0xFF, 0xFE, 0x01, 0x02, 'a', 'b'}, false},
		{append(bytes.Repeat([]byte("a"), 99), 0x80), true}, // 99% printable
		{append(bytes.Repeat([]byte("a"), 90), bytes.Repeat([]byte{0x80}, 10)...), false},
	}
	for i, c := range cases {
		if got := IsText(c.data); got != c.want {
			t.Errorf("case %d: IsText = %v want %v", i, got, c.want)
		}
	}
	if !IsEBCDICText([][]byte{ebcdic.Encode("HELLO WORLD"), ebcdic.Encode("LINE 2")}) {
		t.Fatal("EBCDIC text not detected")
	}
	if IsEBCDICText([][]byte{{0x00, 0x01, 0xFF, 0x12}}) {
		t.Fatal("EBCDIC binary detected as text")
	}
	// ASCII bytes look like garbage when read as EBCDIC.
	if IsEBCDICText([][]byte{[]byte("ascii text is not ebcdic text!")}) {
		t.Fatal("ASCII mistaken for EBCDIC text")
	}
}

func TestTextRecords(t *testing.T) {
	recs, lrecl := textRecords([]byte("ab\r\n\nxyz\n"))
	if len(recs) != 3 || ebcdic.Decode(recs[0]) != "ab" || !bytes.Equal(recs[1], []byte{0x40}) ||
		ebcdic.Decode(recs[2]) != "xyz" || lrecl != 3 {
		t.Fatalf("recs %q lrecl %d", recs, lrecl)
	}
	long := strings.Repeat("x", 600)
	recs, lrecl = textRecords([]byte(long))
	// 600 = 4*132 + 72: four full RSCS print lines and a short one.
	if len(recs) != 5 || len(recs[0]) != 132 || len(recs[4]) != 72 || lrecl != 132 {
		t.Fatalf("long line split: %d %d", len(recs), lrecl)
	}
	recs, lrecl = binaryRecords(make([]byte, 600))
	if len(recs) != 8 || len(recs[7]) != 40 || lrecl != 80 {
		t.Fatal("binary split: punch records of 80, last one short")
	}
	if recs, _ := binaryRecords(nil); len(recs) != 0 {
		t.Fatal("empty binary")
	}
}

func TestSplitName(t *testing.T) {
	n, ty := splitName("/x/read-me.txt", false)
	if n != "README" || ty != "TXT" {
		t.Fatalf("%s %s", n, ty)
	}
	n, ty = splitName("data", true)
	if n != "DATA" || ty != "BIN" {
		t.Fatalf("%s %s", n, ty)
	}
	n, _ = splitName(".hidden", false)
	if n != "HIDDEN" {
		t.Fatalf("%s", n)
	}
	if nameField("averyverylongname") != "AVERYVER" {
		t.Fatal("truncate")
	}
}

func TestPrepareFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.txt")
	os.WriteFile(p, []byte("one\ntwo\n"), 0o644)
	f, err := PrepareFile(p, "NODEB", "BOB", "MOSHE")
	if err != nil || f.Binary || len(f.Records) != 2 || f.DSName != "NOTES" || f.Step != "TXT" ||
		f.RecFM != records.RecFmVariable {
		t.Fatalf("%+v %v", f, err)
	}
	b := filepath.Join(dir, "img.bin")
	os.WriteFile(b, []byte{0, 1, 2, 3, 0xFF}, 0o644)
	f, err = PrepareFile(b, "NODEB", "BOB", "MOSHE")
	if err != nil || !f.Binary || f.RecFM != records.RecFmFixed || !f.Punch || f.LRECL != 80 {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := PrepareFile(dir, "N", "U", "M"); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := PrepareFile(filepath.Join(dir, "missing"), "N", "U", "M"); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestSaveDataset(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 8, 30, 15, 0, time.Local)
	ds := &link.InboundDataset{
		Job:    &records.JobHeader{OrigNode: "NODEA", OrigUser: "MOSHE", JobName: "MOSHE"},
		Header: &records.DatasetHeader{Name: "REPORT"},
		Records: []link.DataRecord{
			{CC: link.CCASA, Data: ebcdic.Encode("1PAGE ONE   ")},
			{CC: link.CCASA, Data: ebcdic.Encode(" LINE TWO")},
		},
	}
	path, text, err := SaveDataset(dir, "NODEA", ds, now)
	if err != nil || !text {
		t.Fatal(err)
	}
	if filepath.Base(path) != "NODEA.MOSHE.REPORT.20261109083015.txt" {
		t.Fatalf("name %s", path)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "PAGE ONE\nLINE TWO\n" {
		t.Fatalf("content %q", got)
	}
	// Same second again: collision suffix.
	path2, _, _ := SaveDataset(dir, "NODEA", ds, now)
	if filepath.Base(path2) != "NODEA.MOSHE.REPORT.20261109083015-2.txt" {
		t.Fatalf("collision name %s", path2)
	}
	// Binary, no headers at all.
	raw := []byte{0, 1, 2, 0xFF, 0x40, 0x40}
	bin := &link.InboundDataset{Records: []link.DataRecord{{Data: raw[:3]}, {Data: raw[3:]}}}
	path, text, err = SaveDataset(dir, "NODEX", bin, now)
	if err != nil || text || filepath.Base(path) != "NODEX.NOJOB.NONAME.20261109083015.bin" {
		t.Fatalf("%s %v %v", path, text, err)
	}
	got, _ = os.ReadFile(path)
	if !bytes.Equal(got, raw) {
		t.Fatalf("binary content % X", got)
	}
	// No temporary files are left behind.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("leftover %s", e.Name())
		}
	}
	// A failed write leaves nothing: the directory is not writable.
	ro := filepath.Join(dir, "ro")
	os.Mkdir(ro, 0o500)
	if _, _, err := SaveDataset(ro, "NODEX", bin, now); err == nil {
		t.Fatal("write into read-only dir succeeded")
	}
	if ents, _ := os.ReadDir(ro); len(ents) != 0 {
		t.Fatalf("files left in read-only dir: %v", ents)
	}
}
