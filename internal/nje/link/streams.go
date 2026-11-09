package link

import (
	"fmt"
	"time"

	"gonje/internal/nje/records"
)

// Outbound SYSOUT transmission state machine (spec 02 sections 2.2, 4.2):
//
//	idle --RI(90 s)--> waitPermit --A0 s--> header --NJH--> dsHeader
//	  --NDH--> data --records...--> trailer --NJT--> eof --EOF(s 00)-->
//	  waitAck --C0 s--> idle            (B0 s at any point: failed)
//
// One file = one job with one data set. Multi-job streams (NCCIRIF) are
// not used, so every file starts with a fresh 90/A0 exchange.
type txPhase int

const (
	txIdle txPhase = iota
	txWaitPermit
	txHeader
	txDSHeader
	txData
	txTrailer
	txEOF
	txWaitAck
)

// txStream is the SYSOUT stream gonje transmits on.
var txStream = records.SysoutRCB(1)

type txState struct {
	state    txPhase
	f        *OutboundFile
	next     int       // next record index to send
	since    time.Time // when the current wait started
	reported int       // last progress quarter reported
}

// txNext returns the next buffer of the outbound file, or nil.
func (s *Session) txNext() []records.Record {
	t := &s.tx
	if t.state == txWaitPermit || t.state == txWaitAck {
		return nil
	}
	if !s.peerFCS.StreamEnabled(txStream) {
		return nil // receiver holds the stream (FCS bit off)
	}
	switch t.state {
	case txIdle:
		if s.draining || s.peerDrained {
			return nil // no new transfers while draining / peer refused
		}
		f := s.p.Handler.NextFile(s.p.Remote)
		if f == nil {
			return nil
		}
		*t = txState{state: txWaitPermit, f: f, since: time.Now()}
		s.emit(EvFileOut, L2, fmt.Sprintf("sending %s to %s/%s (%d records, %s)",
			f.Name, f.ToNode, f.ToUser, len(f.Records), kindName(f.Binary)))
		s.emit(EvStream, L2, "request "+records.StreamName(txStream))
		return []records.Record{{RCB: records.RCBRequest, SRCB: txStream}}
	case txHeader:
		t.state = txDSHeader
		return []records.Record{{RCB: txStream, SRCB: records.SRCBJobHeader, Data: s.jobHeader(t.f).Encode()}}
	case txDSHeader:
		t.state = txData
		return []records.Record{{RCB: txStream, SRCB: records.SRCBDSHeader, Data: s.dsHeader(t.f).Encode()}}
	case txData:
		var recs []records.Record
		size := 6 // DLE STX BCB FCS(2) + EOB
		for t.next < len(t.f.Records) {
			r := records.Record{RCB: txStream, SRCB: records.SRCBData, Data: t.f.Records[t.next]}
			n := records.EncodedSize(r)
			if len(recs) > 0 && size+n > s.bufLimit {
				break
			}
			recs, size = append(recs, r), size+n
			t.next++
		}
		sent := len(recs)
		s.count(func(st *Stats) { st.RecordsOut += sent })
		s.txProgress()
		if t.next >= len(t.f.Records) {
			t.state = txTrailer
		}
		if len(recs) == 0 {
			return s.txNext() // empty file: go straight to the trailer
		}
		return recs
	case txTrailer:
		t.state = txEOF
		tr := &records.JobTrailer{Lines: uint32(len(t.f.Records))}
		return []records.Record{{RCB: txStream, SRCB: records.SRCBJobTrailer, Data: tr.Encode()}}
	case txEOF:
		t.state, t.since = txWaitAck, time.Now()
		return []records.Record{{RCB: txStream, SRCB: records.SRCBEOF}}
	}
	return nil
}

func kindName(binary bool) string {
	if binary {
		return "binary"
	}
	return "text"
}

// txProgress reports every 25% of a transfer of 100+ records.
func (s *Session) txProgress() {
	t := &s.tx
	total := len(t.f.Records)
	if total < 100 {
		return
	}
	q := t.next * 4 / total
	if q > t.reported && q < 4 {
		t.reported = q
		s.emit(EvProgress, L2, fmt.Sprintf("%s: %d/%d records sent (%d%%)", t.f.Name, t.next, total, q*25))
	}
}

// jobHeader builds the NJH for an outbound file (spec 04 section 7).
func (s *Session) jobHeader(f *OutboundFile) *records.JobHeader {
	return &records.JobHeader{
		JobID: f.JobID, JobNo: uint32(f.JobID), JobName: f.JobName,
		NotifyUser: f.FromUser, NotifyNode: s.p.Local, Entered: f.Queued,
		OrigNode: s.p.Local, OrigUser: f.FromUser,
		ExecNode: s.p.Local, ExecUser: f.FromUser,
		PrintNode: f.ToNode, PrintUser: f.ToUser,
		PunchNode: f.ToNode, PunchUser: f.ToUser,
		NRec: uint32(len(f.Records)),
	}
}

// dsHeader builds the NDH for an outbound file (spec 05 section 8.d).
// NDHGPROC/NDHGSTEP carry file name/type with NDHGF2RM set, which is
// how RSCS names the file in the receiving user's reader.
func (s *Session) dsHeader(f *OutboundFile) *records.DatasetHeader {
	return &records.DatasetHeader{
		Node: f.ToNode, Remote: f.ToUser, Proc: f.Proc, Step: f.Step,
		DD: "SYSPRINT", DSNo: 1, NRec: uint32(len(f.Records)),
		RecFM: f.RecFM, LRECL: uint16(f.LRECL), Name: f.DSName,
		Flag2: flag2(f), PMode: "LINE",
	}
}

// flag2 selects print or punch. Binary files go as punch (RECFM F 80),
// the VM convention for data that must not be treated as print lines.
func flag2(f *OutboundFile) byte {
	if f.Punch {
		return records.NDHFlag2Punch | records.NDHFlag2RSCS
	}
	return records.NDHFlag2Print | records.NDHFlag2RSCS
}

// txPermit handles A0 (permission to start our stream).
func (s *Session) txPermit(stream byte) {
	if s.tx.state != txWaitPermit || stream != txStream {
		s.log.Warn("unexpected permission for " + records.StreamName(stream))
		return
	}
	s.emit(EvStream, L2, "permit "+records.StreamName(stream)+" received")
	s.tx.state = txHeader
}

// txDenied handles B0 (receiver cancel / permission denied).
func (s *Session) txDenied(stream byte, reason []byte) {
	if s.tx.state == txIdle || stream != txStream {
		s.log.Warn("unexpected deny for " + records.StreamName(stream))
		return
	}
	if s.tx.state == txWaitPermit {
		// Permission refused (receiver drained or busy): the file stays
		// queued, and we do not ask again until the receiver says it is
		// ready (D0) or the link restarts (Ch. 9: "treat receiver as
		// drained until it sends D0").
		f := s.tx.f
		s.tx = txState{}
		s.peerDrained = true
		s.emit(EvStream, L2, fmt.Sprintf("%s refused %s (reason % X); %s stays queued",
			s.p.Remote, records.StreamName(stream), reason, f.Name))
		return
	}
	if s.tx.state > txWaitPermit && s.tx.state < txWaitAck {
		// Receiver cancelled mid-transmission: end the stream with EOF
		// (allowed answer under the default LRTS rules).
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: txStream, SRCB: records.SRCBEOF})
	}
	s.txFail(fmt.Errorf("rejected by %s (reason % X)", s.p.Remote, reason))
}

// txAcked handles C0 (EOF acknowledged: the peer has the file).
func (s *Session) txAcked(stream byte) {
	if s.tx.state != txWaitAck || stream != txStream {
		// E.g. a late C0 for a file we already counted as unconfirmed.
		s.log.Warn("unexpected EOF ack for " + records.StreamName(stream) + " ignored")
		return
	}
	s.txComplete(" (confirmed)")
}

// txComplete counts the current outbound file as sent and frees the
// stream. note says whether the peer confirmed it.
func (s *Session) txComplete(note string) {
	f := s.tx.f
	s.tx = txState{}
	var n int64
	for _, r := range f.Records {
		n += int64(len(r))
	}
	s.count(func(st *Stats) {
		st.FilesOut++
		st.FileBytesOut += n
	})
	verb := "delivered to"
	if note != " (confirmed)" {
		verb = "sent to"
	}
	s.emit(EvFileOut, L2, fmt.Sprintf("%s %s %s/%s%s", f.Name, verb, f.ToNode, f.ToUser, note))
	s.p.Handler.FileDone(s.p.Remote, f, nil)
}

// txFail abandons the current outbound file.
func (s *Session) txFail(err error) {
	f := s.tx.f
	s.tx = txState{}
	s.emit(EvError, L2, fmt.Sprintf("file %s failed: %v", f.Name, err))
	s.p.Handler.FileDone(s.p.Remote, f, err)
}

// rxStream collects one inbound job on one stream.
type rxStream struct {
	rcb      byte
	job      *records.JobHeader
	trailer  *records.JobTrailer
	datasets []*InboundDataset
	cur      *InboundDataset
	hdr      records.Reassembler // job header / trailer segments
	dsh      records.Reassembler // data set header segments
	span     []byte              // spanned record being rebuilt
	spanLen  int
	count    int // data records received
	reported int // count at the last progress event
	bytes    int // data bytes buffered for this job
	rejected bool
	s        *Session
}

// rxRequest handles 90 (request to initiate a stream).
func (s *Session) rxRequest(stream byte) {
	if !records.IsStream(stream) {
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBDeny, SRCB: stream})
		s.log.Warn("request for unsupported stream " + records.StreamName(stream) + " denied")
		return
	}
	if s.draining {
		// Draining: refuse new jobs (B0, reason X'08' = shutdown) so the
		// sender keeps them for the next connection.
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBDeny, SRCB: stream, Data: []byte{0x08, 0x00}})
		s.emit(EvStream, L2, "request "+records.StreamName(stream)+" refused (draining)")
		return
	}
	if s.rx[stream] != nil {
		// Already active: deny, but keep the connection (spec 02 1.1).
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBDeny, SRCB: stream})
		s.emit(EvStream, L2, "request "+records.StreamName(stream)+" denied (already active)")
		return
	}
	s.rx[stream] = &rxStream{rcb: stream, s: s}
	s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBPermit, SRCB: stream})
	s.emit(EvStream, L2, "request "+records.StreamName(stream)+" received, permitted")
}

// rxRecord handles a record on an inbound SYSIN/SYSOUT stream.
func (s *Session) rxRecord(r records.Record) {
	st := s.rx[r.RCB]
	if st == nil {
		// Data on a stream that was never started. The manual says to
		// terminate the connection; we accept it and log a warning.
		s.log.Warn("NONSTANDARD data on unstarted stream " + records.StreamName(r.RCB) + " accepted")
		st = &rxStream{rcb: r.RCB, s: s}
		s.rx[r.RCB] = st
	}
	if st.rejected {
		// We cancelled this job (too big); swallow the rest until the
		// sender ends the stream with EOF or a transmitter cancel.
		if r.SRCB == records.SRCBEOF || r.SRCB == records.SRCBCancel || r.Aborted {
			delete(s.rx, r.RCB)
		}
		return
	}
	if st.bytes += len(r.Data); st.bytes > s.p.MaxJob {
		// Receiver cancel with reason X'1004' (resource shortage), fig 18.
		st.rejected, st.datasets, st.cur = true, nil, nil
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBDeny, SRCB: r.RCB, Data: []byte{0x10, 0x04}})
		s.emit(EvError, L2, fmt.Sprintf("%s: job larger than %d bytes rejected", records.StreamName(r.RCB), s.p.MaxJob))
		return
	}
	switch {
	case r.SRCB == records.SRCBCancel || r.Aborted:
		delete(s.rx, r.RCB)
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBDeny, SRCB: r.RCB})
		s.emit(EvStream, L2, records.StreamName(r.RCB)+" cancelled by sender")
	case r.SRCB == records.SRCBEOF:
		delete(s.rx, r.RCB)
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBAckEOF, SRCB: r.RCB})
		s.emit(EvStream, L2, records.StreamName(r.RCB)+" EOF, acknowledged")
		s.rxComplete(st)
	case r.SRCB == records.SRCBJobHeader || r.SRCB == records.SRCBJobTrailer:
		full, err := st.hdr.Add(r.Data)
		if err != nil {
			s.log.Warn(err.Error())
			return
		}
		if full == nil {
			return
		}
		if r.SRCB == records.SRCBJobHeader {
			if st.job, err = records.ParseJobHeader(full); err == nil {
				s.emit(EvProgress, L2, fmt.Sprintf("receiving job %s (%d) from %s/%s",
					st.job.JobName, st.job.JobID, st.job.OrigNode, st.job.OrigUser))
			}
		} else {
			st.trailer, err = records.ParseJobTrailer(full)
		}
		if err != nil {
			s.log.Warn(err.Error())
		}
	case r.SRCB == records.SRCBDSHeader:
		full, err := st.dsh.Add(r.Data)
		if err != nil {
			s.log.Warn(err.Error())
			return
		}
		if full == nil {
			return
		}
		h, err := records.ParseDatasetHeader(full)
		if err != nil {
			s.log.Warn(err.Error())
		}
		st.cur = &InboundDataset{Stream: r.RCB, Header: h}
		st.datasets = append(st.datasets, st.cur)
	case r.SRCB&0xC0 == 0x80:
		s.rxData(st, r)
	default:
		s.log.Warn(fmt.Sprintf("unknown SRCB %02X on %s ignored", r.SRCB, records.StreamName(r.RCB)))
	}
}

// rxData adds a data record, rebuilding spanned records.
//
// SRCB = 10cc ss00: cc = carriage control kind, ss = spanning
// (00 whole, 10 first, 01 middle, 11 last). Spanned segments (spec 02
// section 1B): first = SEGL(1) LRECL(2) data, later = SEGL(1) data, where
// SEGL counts the data bytes; blank-truncated segments are padded back to
// SEGL and the whole record is padded with blanks to LRECL.
func (s *Session) rxData(st *rxStream, r records.Record) {
	if st.cur == nil {
		// SYSIN jobs (or sloppy senders) may have no data set header.
		st.cur = &InboundDataset{Stream: r.RCB}
		st.datasets = append(st.datasets, st.cur)
	}
	cc := (r.SRCB >> 4) & 0x03
	if records.IsSysin(r.RCB) {
		cc = CCNone
	}
	ss := (r.SRCB >> 2) & 0x03
	if records.IsSysin(r.RCB) {
		ss = 0
	}
	switch ss {
	case 0: // whole record
		st.add(DataRecord{CC: cc, Data: r.Data})
	case 2: // first segment
		if len(r.Data) < 3 {
			s.log.Warn("short first segment of spanned record")
			return
		}
		st.spanLen = int(r.Data[1])<<8 | int(r.Data[2])
		if st.spanLen > maxSpanned {
			s.log.Warn(fmt.Sprintf("NONSTANDARD spanned LRECL %d > %d: record dropped", st.spanLen, maxSpanned))
			st.span, st.spanLen = nil, 0
			return
		}
		st.span = padSegment(r.Data[3:], int(r.Data[0]))
	case 1, 3: // middle, last
		if len(r.Data) < 1 || st.span == nil {
			s.log.Warn("spanned segment without first segment")
			return
		}
		st.span = append(st.span, padSegment(r.Data[1:], int(r.Data[0]))...)
		if len(st.span) > maxSpanned {
			s.log.Warn("NONSTANDARD spanned record longer than its limit: dropped")
			st.span, st.spanLen = nil, 0
			return
		}
		if ss == 3 {
			rec := st.span
			for len(rec) < st.spanLen {
				rec = append(rec, 0x40)
			}
			st.add(DataRecord{CC: cc, Data: rec})
			st.span, st.spanLen = nil, 0
		}
	}
	if st.count >= st.reported+500 {
		st.reported = st.count
		s.emit(EvProgress, L2, fmt.Sprintf("%s: %d records received", records.StreamName(st.rcb), st.count))
	}
}

func (st *rxStream) add(r DataRecord) {
	st.s.count(func(x *Stats) { x.RecordsIn++ })
	st.cur.Records = append(st.cur.Records, r)
	st.count++
}

// padSegment returns data padded with EBCDIC blanks to n bytes (segments
// may be blank-truncated by the sender).
func padSegment(data []byte, n int) []byte {
	out := append([]byte(nil), data...)
	for len(out) < n {
		out = append(out, 0x40)
	}
	return out
}

// rxComplete delivers every data set of a finished job.
func (s *Session) rxComplete(st *rxStream) {
	if len(st.datasets) == 0 {
		// A job with no data at all still counts as one (empty) file.
		st.datasets = append(st.datasets, &InboundDataset{Stream: st.rcb})
	}
	for _, ds := range st.datasets {
		ds.Job, ds.Trailer = st.job, st.trailer
		var n int64
		for _, r := range ds.Records {
			n += int64(len(r.Data))
		}
		s.count(func(stt *Stats) {
			stt.FilesIn++
			stt.FileBytesIn += n
		})
		s.p.Handler.Dataset(s.p.Remote, ds)
	}
}
