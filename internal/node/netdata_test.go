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
	if meta, _ := os.ReadFile(path + MetaSuffix); !strings.Contains(string(meta), "netdata=pds (not unpacked)") {
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
