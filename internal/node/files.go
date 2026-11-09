package node

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gonje/internal/ebcdic"
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
		for _, r := range ds.Records {
			out.Write(r.Data)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", text, err
	}
	stem := fmt.Sprintf("%s.%s.%s.%s", fsName(from, "NONODE"), fsName(job, "NOJOB"),
		fsName(name, "NONAME"), now.Format("20060102150405"))
	// Write to a temporary .part file first and link it to its final name
	// only when complete, so a failed write never leaves a truncated file
	// that looks like a received one (review N2). os.Link fails if the
	// name exists, which also picks the "-2", "-3"... suffix safely.
	tmp, err := os.CreateTemp(dir, stem+".*.part")
	if err != nil {
		return "", text, err
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o644)
	_, werr := tmp.Write(out.Bytes())
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", text, werr
	}
	path = filepath.Join(dir, stem+"."+ext)
	for i := 2; ; i++ {
		err := os.Link(tmp.Name(), path)
		if err == nil {
			return path, text, nil
		}
		if !os.IsExist(err) {
			return "", text, err
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.%s", stem, i, ext))
	}
}
