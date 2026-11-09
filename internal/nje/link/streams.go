// Copyright 2026 by moshix

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

// Outbound slots: with max_streams = n, up to n files are in flight at
// once, slot i on SYSOUT stream i+1 (X'99', X'A9', ...). Without the
// NCCIMRCB feature a buffer holds one RCB, so files interleave buffer by
// buffer (round robin).
type txState struct {
	rcb      byte // stream RCB of this slot
	state    txPhase
	f        *OutboundFile
	next     int              // next record index to send
	pend     []records.Record // segments of a spanned record still to send
	since    time.Time        // when the current wait started
	reported int              // last progress quarter reported
}

// initTx creates the outbound slots.
func (s *Session) initTx() {
	s.txs = make([]*txState, s.p.MaxStreams)
	for i := range s.txs {
		s.txs[i] = &txState{rcb: records.SysoutRCB(i + 1)}
	}
}

// txBusy lists the files currently in flight (so NextFile skips them).
func (s *Session) txBusy() []*OutboundFile {
	var out []*OutboundFile
	for _, t := range s.txs {
		if t.state != txIdle {
			out = append(out, t.f)
		}
	}
	return out
}

// txActive counts slots with a transfer in progress.
func (s *Session) txActive() int { return len(s.txBusy()) }

// txSlot finds the slot transmitting on stream rcb (nil if none).
func (s *Session) txSlot(rcb byte) *txState {
	for _, t := range s.txs {
		if t.rcb == rcb {
			return t
		}
	}
	return nil
}

// txNext returns the next outbound buffer of any slot, taking the slots
// in turn so concurrent files share the link fairly.
func (s *Session) txNext() []records.Record {
	for i := range s.txs {
		t := s.txs[(s.txTurn+i)%len(s.txs)]
		if recs := s.txStep(t); recs != nil {
			s.txTurn = (s.txTurn + i + 1) % len(s.txs)
			return recs
		}
	}
	return nil
}

// txStep advances one slot's state machine and returns its next buffer.
func (s *Session) txStep(t *txState) []records.Record {
	if t.state == txWaitPermit || t.state == txWaitAck {
		return nil
	}
	if !s.peerFCS.StreamEnabled(t.rcb) {
		return nil // receiver holds the stream (FCS bit off)
	}
	switch t.state {
	case txIdle:
		if s.draining || s.peerDrained {
			return nil // no new transfers while draining / peer refused
		}
		if !s.settled {
			if time.Since(s.signedOnAt) < s.p.StreamSettle {
				return nil // let the peer finish its signon (see streamSettle)
			}
			s.settled = true
		}
		f := s.p.Handler.NextFile(s.p.Remote, s.txBusy())
		if f == nil {
			return nil
		}
		*t = txState{rcb: t.rcb, state: txWaitPermit, f: f, since: time.Now()}
		s.emit(EvFileOut, L2, fmt.Sprintf("sending %s to %s/%s (%d records, %s)",
			f.Name, f.ToNode, f.ToUser, len(f.Records), kindName(f.Binary)))
		s.emit(EvStream, L2, "request "+records.StreamName(t.rcb))
		return []records.Record{{RCB: records.RCBRequest, SRCB: t.rcb}}
	case txHeader:
		t.state = txDSHeader
		return []records.Record{{RCB: t.rcb, SRCB: records.SRCBJobHeader, Data: s.jobHeader(t.f).Encode()}}
	case txDSHeader:
		t.state = txData
		return []records.Record{{RCB: t.rcb, SRCB: records.SRCBDSHeader, Data: s.dsHeader(t.f).Encode()}}
	case txData:
		var recs []records.Record
		size := 6 // DLE STX BCB FCS(2) + EOB
		sent := 0
		for {
			if len(t.pend) == 0 {
				if t.next >= len(t.f.Records) {
					break
				}
				t.pend = s.dataRecords(t.rcb, t.f.CC, t.f.Records[t.next])
				t.next++
				sent++
			}
			r := t.pend[0]
			n := records.EncodedSize(r)
			if len(recs) > 0 && size+n > s.bufLimit {
				break
			}
			recs, size = append(recs, r), size+n
			t.pend = t.pend[1:]
		}
		s.count(func(st *Stats) { st.RecordsOut += sent })
		s.txProgress(t)
		if t.next >= len(t.f.Records) && len(t.pend) == 0 {
			t.state = txTrailer
		}
		if len(recs) == 0 {
			return s.txStep(t) // empty file: go straight to the trailer
		}
		return recs
	case txTrailer:
		t.state = txEOF
		tr := &records.JobTrailer{Lines: uint32(len(t.f.Records))}
		return []records.Record{{RCB: t.rcb, SRCB: records.SRCBJobTrailer, Data: tr.Encode()}}
	case txEOF:
		t.state, t.since = txWaitAck, time.Now()
		// End of file is an EMPTY data record: RCB, SRCB X'80', SCB 00.
		// NJE38 (DMTXJE, label RDEOF: "MVI RCTTSRC1,X'80' Reset SRCB for
		// EOF record" + zero tank count) sends EOF this way, and its
		// receiver rejects SRCB X'00' with NJE939E reason code 01 ("Is
		// SRCB plausable looking?" tests the X'80' bit). Live HOUCICS
		// traces 11:20-11:48 on 2026-11-09 failed exactly like that.
		return []records.Record{{RCB: t.rcb, SRCB: records.SRCBData}}
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
func (s *Session) txProgress(t *txState) {
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
	orig := f.OrigNode
	if orig == "" {
		orig = s.p.Local
	}
	return &records.JobHeader{
		JobID: f.JobID, JobNo: uint32(f.JobID), JobName: f.JobName, Hops: f.Hops,
		NotifyUser: f.FromUser, NotifyNode: orig, Entered: f.Queued,
		OrigNode: orig, OrigUser: f.FromUser,
		ExecNode: orig, ExecUser: f.FromUser,
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
	t := s.txSlot(stream)
	if t == nil || t.state != txWaitPermit {
		s.log.Warn("unexpected permission for " + records.StreamName(stream))
		return
	}
	s.emit(EvStream, L2, "permit "+records.StreamName(stream)+" received")
	t.state = txHeader
}

// txDenied handles B0 (receiver cancel / permission denied).
func (s *Session) txDenied(stream byte, reason []byte) {
	t := s.txSlot(stream)
	if t == nil || t.state == txIdle {
		s.log.Warn("unexpected deny for " + records.StreamName(stream))
		return
	}
	if t.state == txWaitPermit {
		// Permission refused (receiver drained or busy): the file stays
		// queued, and we do not ask again until the receiver says it is
		// ready (D0) or the link restarts (Ch. 9: "treat receiver as
		// drained until it sends D0").
		f := t.f
		*t = txState{rcb: t.rcb}
		s.peerDrained = true
		s.emit(EvStream, L2, fmt.Sprintf("%s refused %s (reason % X); %s stays queued",
			s.p.Remote, records.StreamName(stream), reason, f.Name))
		return
	}
	if t.state > txWaitPermit && t.state < txWaitAck {
		// Receiver cancelled mid-transmission: end the stream with EOF
		// (allowed answer under the default LRTS rules).
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: t.rcb, SRCB: records.SRCBEOF})
	}
	s.txFail(t, fmt.Errorf("rejected by %s (reason % X)", s.p.Remote, reason))
}

// txAcked handles C0 (EOF acknowledged: the peer has the file).
func (s *Session) txAcked(stream byte) {
	t := s.txSlot(stream)
	if t == nil || t.state != txWaitAck {
		// E.g. a late C0 for a file we already counted as unconfirmed.
		s.log.Warn("unexpected EOF ack for " + records.StreamName(stream) + " ignored")
		return
	}
	s.txComplete(t, " (confirmed)")
}

// txComplete counts a slot's file as sent and frees the slot. note says
// whether the peer confirmed it.
func (s *Session) txComplete(t *txState, note string) {
	f := t.f
	*t = txState{rcb: t.rcb}
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

// txFail abandons a slot's file.
func (s *Session) txFail(t *txState, err error) {
	f := t.f
	*t = txState{rcb: t.rcb}
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
	if len(s.rx) >= s.p.MaxStreams {
		// All our streams are busy (max_streams): refuse; the sender
		// keeps the file. We send D0 (ready) when a stream frees up.
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBDeny, SRCB: stream, Data: []byte{0x10, 0x10}})
		s.refusedFull = true
		s.emit(EvStream, L2, fmt.Sprintf("request %s refused (%d stream(s) busy)", records.StreamName(stream), len(s.rx)))
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
		if isEOF(r) || r.SRCB == records.SRCBCancel || r.Aborted {
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
	case isEOF(r):
		delete(s.rx, r.RCB)
		s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBAckEOF, SRCB: r.RCB})
		s.emit(EvStream, L2, records.StreamName(r.RCB)+" EOF, acknowledged")
		s.rxComplete(st)
		if s.refusedFull && !s.draining {
			// Tell the sender we can take a file again (Ch. 9: a sender
			// treats us as drained after B0 until it sees D0).
			s.ctlQueue = append(s.ctlQueue, records.Record{RCB: records.RCBReady, SRCB: r.RCB})
			s.refusedFull = false
		}
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

// isEOF reports an end-of-file record on a stream. Two forms exist: the
// SRCB X'00' form from our NJE spec notes, and the empty data record
// (SRCB 10cc 0000, i.e. a whole, unspanned data record with no bytes)
// that NJE38 sends and expects ("Empty tank => end of file"). gonje never
// sends a genuinely empty data record (empty text lines go as one
// blank), so a zero-length whole data record is always EOF.
func isEOF(r records.Record) bool {
	if r.SRCB == records.SRCBEOF {
		return true
	}
	return r.SRCB&0xCC == 0x80 && len(r.Data) == 0 && !r.Aborted
}

// stripRecordLength removes the 1-byte record length that starts an
// unspanned data record (figs 20/21; NJE38 always sends it) and pads a
// blank-truncated record back to that length. It only strips when the
// byte really is the length: equal to the bytes that follow, or larger
// and equal to the data set's LRECL (trailing blanks truncated). A record
// without the prefix (an older gonje) is kept as it is, with a warning.
func (s *Session) stripRecordLength(st *rxStream, d []byte) []byte {
	if len(d) == 0 {
		return d
	}
	n := int(d[0])
	switch {
	case n == len(d)-1:
		return d[1:]
	case n > len(d)-1 && st.cur != nil && st.cur.Header != nil && int(st.cur.Header.LRECL) == n:
		return padSegment(d[1:], n)
	}
	s.log.Warn(fmt.Sprintf("NONSTANDARD data record without length byte on %s (first byte %02X, %d bytes): kept as is",
		records.StreamName(st.rcb), d[0], len(d)))
	return d
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
		st.add(DataRecord{CC: cc, Data: s.stripRecordLength(st, r.Data)})
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

// dataRecords encodes one logical SYSOUT record (SRCB 10cc ss00, cc =
// carriage control kind).
//
// A record that fits is sent whole (ss = 00) and starts with its 1-byte
// length (LRECL incl. the CC byte), as in figs 20/21 of the manual; spec
// 02 AMBIG-2 left open whether BSC/TCP carries it, and the NJE38 trace on
// 2026-11-09 settled it: NJE38 sends "50 <80 bytes>" and its receiver
// reads byte 0 as the length (DMTXJE PDATAREC "Don't count length
// byte"), so without it HOUCICS lost the first character of every line.
//
// That length is one byte, so a record of 256 bytes or more (forwarded
// files carry whatever was received), or one too big for a buffer, is
// sent spanned like rxData rebuilds it: first segment SEGL(1) LRECL(2)
// data (ss = 10), then SEGL(1) data (01 middle, 11 last).
func (s *Session) dataRecords(rcb, cc byte, rec []byte) []records.Record {
	srcb := records.SRCBData | cc<<4
	if len(rec) <= 255 {
		r := records.Record{RCB: rcb, SRCB: srcb, Data: append([]byte{byte(len(rec))}, rec...)}
		if records.EncodedSize(r)+6 <= s.bufLimit {
			return []records.Record{r}
		}
	}
	if len(rec) > maxSpanned {
		s.log.Warn(fmt.Sprintf("record of %d bytes cut to %d", len(rec), maxSpanned))
		rec = rec[:maxSpanned]
	}
	// Segment size: at most 255 (SEGL is one byte) and small enough that
	// a segment (worst case after compression) fits an empty buffer.
	seg := 255
	for seg > 16 && records.EncodedSize(records.Record{RCB: rcb, Data: make([]byte, seg+3)})+6 > s.bufLimit {
		seg -= 16
	}
	var out []records.Record
	for off := 0; off < len(rec); off += seg {
		part := rec[off:min(off+seg, len(rec))]
		var data []byte
		var ss byte
		switch {
		case off == 0:
			ss = 0x08 // first
			data = append([]byte{byte(len(part)), byte(len(rec) >> 8), byte(len(rec))}, part...)
		case off+seg >= len(rec):
			ss = 0x0C // last
			data = append([]byte{byte(len(part))}, part...)
		default:
			ss = 0x04 // middle
			data = append([]byte{byte(len(part))}, part...)
		}
		out = append(out, records.Record{RCB: rcb, SRCB: srcb | ss, Data: data})
	}
	if len(out) == 1 {
		// A record that needs spanning but fits one segment: first+last
		// in one (the receiver needs a last segment), so split it.
		part := rec
		half := len(part) / 2
		out = []records.Record{
			{RCB: rcb, SRCB: srcb | 0x08, Data: append([]byte{byte(half), byte(len(rec) >> 8), byte(len(rec))}, part[:half]...)},
			{RCB: rcb, SRCB: srcb | 0x0C, Data: append([]byte{byte(len(part) - half)}, part[half:]...)},
		}
	}
	return out
}
