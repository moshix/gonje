// Copyright 2026 by moshix

package node

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gonje/internal/ebcdic"
	"gonje/internal/netdata"
	"gonje/internal/nje/link"
	"gonje/internal/nje/records"
)

// Record sizes for outbound files.
const (
	// maxTextRecord: RSCS print lines are 132 characters (1403/3211
	// width), so longer lines are split into several records rather
	// than sent as one record RSCS would truncate. No spanning.
	maxTextRecord = 132
	// binaryRecord: binary files go as punch, RECFM F LRECL 80 (the VM
	// convention; RSCS would treat long print records as 1403/3211
	// lines). The last record is sent short rather than padded, so a
	// receiver that does not pad (gonje) gets the file back byte-exact.
	binaryRecord  = 80
	maxFileSize   = 64 << 20
	textThreshold = 0.95 // share of printable/whitespace bytes for "text"
)

// isTextByte reports printable ASCII or common whitespace.
func isTextByte(b byte) bool {
	return (b >= 0x20 && b <= 0x7E) || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

// textRatio returns the share of text bytes; any NUL makes it 0.
func textRatio(data []byte, conv func(byte) byte) float64 {
	if len(data) == 0 {
		return 1
	}
	good := 0
	for _, b := range data {
		a := conv(b)
		if a == 0 {
			return 0
		}
		if isTextByte(a) {
			good++
		}
	}
	return float64(good) / float64(len(data))
}

func identity(b byte) byte { return b }

// IsText applies the text/binary heuristic to a local (ASCII) file.
func IsText(data []byte) bool { return textRatio(data, identity) >= textThreshold }

// IsEBCDICText applies the same heuristic to received EBCDIC records.
func IsEBCDICText(recs [][]byte) bool {
	return textRatio(bytes.Join(recs, nil), ebcdic.ToLatin1Byte) >= textThreshold
}

// nameField upper-cases s and keeps only node-name style characters,
// at most 8 of them.
func nameField(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '@' || r == '#' || r == '$' {
			sb.WriteRune(r)
			if sb.Len() == 8 {
				break
			}
		}
	}
	return sb.String()
}

// splitName returns NDHGNAME/PROC and STEP (RSCS file name and type)
// for a local path, e.g. "notes/readme.txt" -> "README", "TXT".
func splitName(path string, binary bool) (name, ftype string) {
	base := filepath.Base(path)
	stem, ext := base, ""
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		stem, ext = base[:i], base[i+1:]
	}
	name, ftype = nameField(stem), nameField(ext)
	if name == "" {
		name = "FILE"
	}
	if ftype == "" {
		ftype = "TEXT"
		if binary {
			ftype = "BIN"
		}
	}
	return name, ftype
}

// textRecords converts an ASCII text file into EBCDIC records: one per
// line (CR LF or LF), no carriage control. Empty lines become one blank
// (some systems dislike zero-length records); lines over maxTextRecord
// (132) bytes are
// split into several records.
func textRecords(data []byte) (recs [][]byte, lrecl int) {
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1] // file ended with a newline
	}
	for _, l := range lines {
		l = bytes.TrimSuffix(l, []byte{'\r'})
		e := ebcdic.EncodeBytes(l)
		if len(e) == 0 {
			e = []byte{ebcdic.Blank}
		}
		for len(e) > maxTextRecord {
			recs = append(recs, e[:maxTextRecord])
			e = e[maxTextRecord:]
		}
		recs = append(recs, e)
	}
	for _, r := range recs {
		lrecl = max(lrecl, len(r))
	}
	return recs, max(lrecl, 1)
}

// binaryRecords cuts data into untranslated records.
func binaryRecords(data []byte) (recs [][]byte, lrecl int) {
	for len(data) > 0 {
		n := min(binaryRecord, len(data))
		recs = append(recs, append([]byte(nil), data[:n]...))
		data = data[n:]
	}
	if len(recs) > 0 {
		lrecl = len(recs[0])
	}
	return recs, max(lrecl, 1)
}

// PrepareFile reads a local file and turns it into an OutboundFile.
func PrepareFile(path, toNode, toUser, fromUser string) (*link.OutboundFile, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if st.Size() > maxFileSize {
		return nil, fmt.Errorf("%s is larger than %d MB", path, maxFileSize>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &link.OutboundFile{Name: filepath.Base(path), ToNode: toNode, ToUser: toUser,
		FromUser: fromUser, JobName: fromUser, Queued: time.Now()}
	f.Binary = !IsText(data)
	if f.Binary {
		f.Records, f.LRECL = binaryRecords(data)
		f.RecFM, f.LRECL, f.Punch = records.RecFmFixed, binaryRecord, true
	} else {
		f.Records, f.LRECL = textRecords(data)
		f.RecFM = records.RecFmVariable
	}
	f.DSName, f.Step = splitName(path, f.Binary)
	f.Proc = f.DSName
	return f, nil
}

// fsName makes a string safe for a file name component.
func fsName(s, def string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	if sb.Len() == 0 {
		return def
	}
	return sb.String()
}

// SaveDataset writes a received data set to dir:
//
//	<FROMNODE>.<JOBNAME>.<NDHGNAME>.<yyyymmddhhmmss>.txt|.bin
//
// Text (by the EBCDIC heuristic) is saved as ASCII/UTF-8 lines with
// carriage control stripped and trailing blanks removed; anything else
// is saved as the raw record bytes, untranslated.
func SaveDataset(dir, remote string, ds *link.InboundDataset, now time.Time) (path string, text bool, err error) {
	path, text, _, err = SaveDatasetInfo(dir, remote, ds, now)
	return path, text, err
}

// SaveDatasetInfo is SaveDataset that also returns a warning (e.g. a
// NETDATA transmission that could not be decoded and was saved raw).
//
// NETDATA (TSO/NJE38 TRANSMIT) is unpacked: the file is named after the
// transmitted data set (<FROMNODE>.<FROMUSER>.<DSNAME>.<time>) and holds
// the data set's records, as text or binary like any other file. A PDS
// (IEBCOPY unload) is kept as the raw NETDATA .bin for now.
func SaveDatasetInfo(dir, remote string, ds *link.InboundDataset, now time.Time) (path string, text bool, warn string, err error) {
	from, job, name := remote, "NOJOB", "NONAME"
	if ds.Job != nil {
		if ds.Job.OrigNode != "" {
			from = ds.Job.OrigNode
		}
		if ds.Job.JobName != "" {
			job = ds.Job.JobName
		}
	}
	if ds.Header != nil && ds.Header.Name != "" {
		name = ds.Header.Name
	}
	// Content without carriage control bytes, for the text check.
	body := make([][]byte, len(ds.Records))
	for i, r := range ds.Records {
		body[i] = r.Data
		if r.CC != link.CCNone && len(r.Data) > 0 {
			body[i] = r.Data[1:]
		}
	}
	raw := make([][]byte, len(ds.Records)) // what a binary save writes
	for i, r := range ds.Records {
		raw[i] = r.Data
	}
	var extra [][2]string // NETDATA details for the sidecar
	if stream := bytes.Join(body, nil); netdata.IsNetdata(stream) {
		nd, perr := netdata.Parse(stream)
		if perr == nil {
			from, job, name = orStr(nd.FromNode, from), orStr(nd.FromUser, job), orStr(nd.DSName, name)
			extra = netdataMeta(nd)
		}
		switch {
		case perr != nil:
			warn = "NETDATA could not be decoded, saved raw: " + perr.Error()
			extra = [][2]string{{"netdata", "invalid (" + perr.Error() + ")"}}
		case nd.PDS():
			extra = append(extra, [2]string{"netdata", "pds (not unpacked)"})
		default:
			recs := nd.Records
			if nd.Fixed() {
				recs = netdata.StripSequenceNumbers(recs)
			}
			body, raw = recs, nd.Records
			extra = append(extra, [2]string{"netdata", "yes"})
		}
	}
	text = IsEBCDICText(body)
	var out bytes.Buffer
	ext := "bin"
	if text {
		ext = "txt"
		for _, b := range body {
			out.WriteString(strings.TrimRight(ebcdic.Decode(b), " "))
			out.WriteByte('\n')
		}
	} else {
		for _, r := range raw {
			out.Write(r)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", text, warn, err
	}
	stem := fmt.Sprintf("%s.%s.%s.%s", fsName(from, "NONODE"), fsName(job, "NOJOB"),
		fsNameDots(name, "NONAME"), now.Format("20060102150405"))
	// Write to a temporary .part file first and link it to its final name
	// only when complete, so a failed write never leaves a truncated file
	// that looks like a received one (review N2). os.Link fails if the
	// name exists, which also picks the "-2", "-3"... suffix safely.
	tmp, err := os.CreateTemp(dir, stem+".*.part")
	if err != nil {
		return "", text, warn, err
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o644)
	_, werr := tmp.Write(out.Bytes())
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", text, warn, werr
	}
	path = filepath.Join(dir, stem+"."+ext)
	for i := 2; ; i++ {
		err := os.Link(tmp.Name(), path)
		if err == nil {
			// The sidecar is informational: a failure to write it is
			// not a failure to receive the file.
			_ = writeMeta(path, remote, ds, now, text, out.Len(), extra)
			return path, text, warn, nil
		}
		if !os.IsExist(err) {
			return "", text, warn, err
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.%s", stem, i, ext))
	}
}

// MetaSuffix is the extension of the metadata sidecar of a received file.
const MetaSuffix = ".meta"

// recfmName describes NDHGRCFM: F/V/U plus the carriage control kind.
func recfmName(rcfm byte) string {
	switch rcfm & 0xC0 {
	case records.RecFmFixed:
		return "F"
	case records.RecFmVariable:
		return "V"
	case records.RecFmUndefined:
		return "U"
	}
	return "?"
}

// ccName names the carriage control of a data set's records.
func ccName(ds *link.InboundDataset) string {
	for _, r := range ds.Records {
		switch r.CC {
		case link.CCASA:
			return "asa"
		case link.CCMachine:
			return "machine"
		case link.CCPage:
			return "page"
		}
		return "none"
	}
	return "none"
}

// writeMeta writes <file>.meta: key=value lines with the NJE metadata of
// a received file (shown by the shell's "spool" command).
func writeMeta(path, remote string, ds *link.InboundDataset, now time.Time, text bool, size int, extra [][2]string) error {
	kv := [][2]string{{"received", now.Format(time.RFC3339)}, {"link", remote},
		{"stream", records.StreamName(ds.Stream)}}
	add := func(k, v string) { kv = append(kv, [2]string{k, v}) }
	if j := ds.Job; j != nil {
		add("from_node", j.OrigNode)
		add("from_user", j.OrigUser)
		add("jobname", j.JobName)
		add("jobid", fmt.Sprint(j.JobID))
	}
	if h := ds.Header; h != nil {
		add("to_node", h.Node)
		add("to_user", h.Remote)
		add("dsname", h.Name)
		add("class", ebcdic.Decode([]byte{orBlank(h.Class)}))
		add("form", h.Form)
		add("recfm", recfmName(h.RecFM))
		add("lrecl", fmt.Sprint(h.LRECL))
	}
	kind := "binary"
	if text {
		kind = "text"
	}
	add("records", fmt.Sprint(len(ds.Records)))
	add("bytes", fmt.Sprint(size))
	add("kind", kind)
	add("cc", ccName(ds))
	// NETDATA fields replace the NJE ones of the same name.
	over := map[string]bool{}
	for _, p := range extra {
		over[p[0]] = true
	}
	var all [][2]string
	for _, p := range kv {
		if !over[p[0]] {
			all = append(all, p)
		}
	}
	kv = append(all, extra...)
	var sb strings.Builder
	for _, p := range kv {
		sb.WriteString(p[0] + "=" + clean(strings.TrimSpace(p[1])) + "\n")
	}
	return os.WriteFile(path+MetaSuffix, []byte(sb.String()), 0o644)
}

func orBlank(b byte) byte {
	if b == 0 {
		return ebcdic.Blank
	}
	return b
}

func orStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// fsNameDots is fsName that keeps dots (data set name qualifiers), but
// never as a leading dot or "..".
func fsNameDots(s, def string) string {
	parts := strings.Split(s, ".")
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, fsName(p, "_"))
		}
	}
	if len(out) == 0 {
		return def
	}
	return strings.Join(out, ".")
}

// netdataMeta lists the NETDATA details for the sidecar.
func netdataMeta(nd *netdata.File) [][2]string {
	return [][2]string{
		{"original_dsname", nd.DSName}, {"from_node", nd.FromNode}, {"from_user", nd.FromUser},
		{"to_node", nd.ToNode}, {"to_user", nd.ToUser}, {"sent", nd.Time},
		{"recfm", netdata.RecfmName(nd.RECFM)}, {"lrecl", fmt.Sprint(nd.LRECL)},
		{"dsorg", netdata.DSOrgName(nd.DSOrg)}, {"utility", nd.Utility},
		{"dsname", nd.DSName}, {"records", fmt.Sprint(len(nd.Records))},
	}
}
