// Copyright 2026 by moshix

package node

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
)

// cards splits a NETDATA stream into 80-byte received records.
func cards(b []byte) *link.InboundDataset {
	ds := &link.InboundDataset{Stream: 0x99,
		Job:    &records.JobHeader{OrigNode: "HOUCICS", OrigUser: "HERC01", JobName: "HERC01"},
		Header: &records.DatasetHeader{Node: "MOSHIX", Remote: "MOSHE", RecFM: records.RecFmFixed, LRECL: 80}}
	for len(b) > 0 {
		n := min(80, len(b))
		ds.Records = append(ds.Records, link.DataRecord{Data: b[:n]})
		b = b[n:]
	}
	return ds
}

func golden(t *testing.T) []byte {
	b, err := os.ReadFile("../netdata/testdata/sendfile.netdata")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSaveNetdataText(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 12, 6, 5, 0, time.Local)
	path, text, warn, err := SaveDatasetInfo(dir, "HOUCICS", cards(golden(t)), now)
	if err != nil || !text || warn != "" {
		t.Fatalf("%v %v %q", err, text, warn)
	}
	if filepath.Base(path) != "HOUCICS.HERC01.HERC01.SENDFILE.20261109120605.txt" {
		t.Fatalf("name %s", path)
	}
	if b, _ := os.ReadFile(path); string(b) != "THIS IS A TEXT FILE\n" {
		t.Fatalf("content %q", b)
	}
	meta, _ := os.ReadFile(path + MetaSuffix)
	for _, want := range []string{"netdata=yes", "original_dsname=HERC01.SENDFILE", "from_node=HOUCICS", "from_user=HERC01",
		"to_node=MOSHIX", "to_user=MOSHE", "recfm=FB", "lrecl=80", "dsorg=PS", "utility=INMCOPY", "sent=2026", "kind=text"} {
		if !strings.Contains(string(meta), want) {
			t.Errorf("sidecar lacks %q:\n%s", want, meta)
		}
	}
	if strings.Count(string(meta), "dsname=") != 2 { // original_dsname + dsname, no duplicate keys
		t.Fatalf("duplicate keys:\n%s", meta)
	}
}

func TestSaveNetdataPDSAndInvalid(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// PDS: INMDSORG X'4000' -> X'0200' in INMR02.
	g := golden(t)
	pds := bytes.Replace(g, []byte{0x00, 0x3C, 0x00, 0x01, 0x00, 0x02, 0x40, 0x00}, []byte{0x00, 0x3C, 0x00, 0x01, 0x00, 0x02, 0x02, 0x00}, 1)
	path, text, warn, err := SaveDatasetInfo(dir, "HOUCICS", cards(pds), now)
	if err != nil || text || warn != "" || !strings.HasSuffix(path, ".bin") {
		t.Fatalf("pds: %s %v %q %v", path, text, warn, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, pds) {
		t.Fatal("PDS not saved raw")
	}
	if meta, _ := os.ReadFile(path + MetaSuffix); !strings.Contains(string(meta), "netdata=pds\n") {
		t.Fatalf("%s", meta)
	}
	// Corrupt: cut in the middle of a segment -> raw + warning.
	bad := g[:150]
	path, _, warn, err = SaveDatasetInfo(dir, "HOUCICS", cards(bad), now)
	if err != nil || !strings.Contains(warn, "NETDATA could not be decoded") {
		t.Fatalf("%q %v", warn, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, bad) {
		t.Fatal("corrupt NETDATA not saved raw")
	}
	if meta, _ := os.ReadFile(path + MetaSuffix); !strings.Contains(string(meta), "netdata=invalid") {
		t.Fatalf("%s", meta)
	}
}

// TestSaveNetdataTwoINMR02: a TRANSMITted PDS (two INMR02 records) is
// kept as the raw NETDATA stream, never flattened (second review M2).
func TestSaveNetdataTwoINMR02(t *testing.T) {
	raw, err := os.ReadFile("../netdata/testdata/pds2.netdata")
	if err != nil {
		t.Fatal(err)
	}
	path, text, _, err := SaveDatasetInfo(t.TempDir(), "NODEA", cards(raw), time.Now())
	if err != nil || text || !strings.HasSuffix(path, ".bin") {
		t.Fatalf("%s %v %v", path, text, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, raw) {
		t.Fatal("PDS not saved as the raw NETDATA stream")
	}
	meta, _ := os.ReadFile(path + MetaSuffix)
	if !strings.Contains(string(meta), "netdata=pds\n") || !strings.Contains(string(meta), "utility=IEBCOPY") {
		t.Fatalf("%s", meta)
	}
}

// TestSaveLongNames: node, user and data set names from the peer are cut
// so the file name stays short (second review m12).
func TestSaveLongNames(t *testing.T) {
	long := strings.Repeat("N", 300)
	ds := &link.InboundDataset{Stream: 0x99,
		Job:     &records.JobHeader{OrigNode: long, OrigUser: long, JobName: long},
		Header:  &records.DatasetHeader{Name: strings.Repeat("ABCDEFGH.", 40), RecFM: records.RecFmFixed, LRECL: 80},
		Records: []link.DataRecord{{Data: []byte{0xC1}}}}
	path, _, _, err := SaveDatasetInfo(t.TempDir(), long, ds, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(filepath.Base(path)); n > 100 {
		t.Fatalf("name %d bytes: %s", n, filepath.Base(path))
	}
}

// FuzzSaveDataset: any received stream (NETDATA or not) is saved without
// a panic and under a short name (second review m12).
func FuzzSaveDataset(f *testing.F) {
	if g, err := os.ReadFile("../netdata/testdata/sendfile.netdata"); err == nil {
		f.Add(g)
	}
	f.Add([]byte{0x10, 0xE0, 0xC9, 0xD5, 0xD4, 0xD9, 0xF0, 0xF1, 0x00, 0x00})
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, b []byte) {
		path, _, _, err := SaveDatasetInfo(dir, "NODEA", cards(b), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(filepath.Base(path)) > 120 {
			t.Fatalf("name %d bytes", len(filepath.Base(path)))
		}
		os.Remove(path)
		os.Remove(path + MetaSuffix)
	})
}
