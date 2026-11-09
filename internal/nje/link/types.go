// Copyright 2026 by moshix

// Package link runs one NJE connection to one remote node: the OPEN/ACK
// exchange (connecting side), the SOH ENQ / DLE ACK0 / I / J signon in
// either role, and then the multileaving main loop (BCB/FCS handling,
// NMRs, SYSOUT stream transmit/receive, keepalive, signoff).
package link

import (
	"errors"
	"fmt"
	"time"

	"gonje/internal/nje/records"
)

// Kind classifies an Event for display and filtering.
type Kind int

// Event kinds.
const (
	EvInfo      Kind = iota
	EvError          // red
	EvLink           // yellow: connect, up, down
	EvSignon         // yellow: signon / signoff
	EvStream         // stream control (request/permit/ack)
	EvMsgIn          // cyan
	EvMsgOut         // cyan
	EvCmdIn          // magenta
	EvCmdOut         // magenta
	EvRspIn          // magenta
	EvRspOut         // magenta
	EvFileIn         // green
	EvFileOut        // green
	EvProgress       // transfer progress
	EvDump           // L3 record dump
	EvKeepalive      // L3, collapsed in the panel
)

// Panel levels (PLAN "Panel levels").
const (
	L1 = 1 // incoming commands + incoming files
	L2 = 2 // + messages, responses, outgoing, progress, link, errors
	L3 = 3 // + hex dumps of every record, keepalive summary
)

// Event is something worth showing in the event panel and the log.
type Event struct {
	Time  time.Time
	Kind  Kind
	Level int
	Node  string   // remote node the event relates to ("" = local)
	Text  string   // one-line summary
	Lines []string // optional detail (hex dump lines)
}

// Handler receives everything a session produces. Implementations must be
// safe for concurrent use (one session goroutine per remote).
type Handler interface {
	// Event reports a displayable/loggable event.
	Event(ev Event)
	// NMR delivers an inbound nodal message record.
	NMR(remote string, m *records.NMR)
	// Dataset delivers one complete received data set (at stream EOF).
	Dataset(remote string, ds *InboundDataset)
	// NextFile returns, without removing it, the next file queued for
	// this remote, or nil.
	// busy lists files already in flight (skipped).
	NextFile(remote string, busy []*OutboundFile) *OutboundFile
	// FileDone reports that f was accepted (err == nil) or rejected by
	// the peer. It is not called when the link drops mid-transfer: the
	// file stays queued and is resent after reconnect.
	FileDone(remote string, f *OutboundFile, err error)
}

// OutboundFile is a file ready to be sent as a one-dataset SYSOUT job.
type OutboundFile struct {
	ID       int
	Name     string // display name (local path)
	ToNode   string
	ToUser   string
	FromUser string
	JobName  string
	JobID    uint16
	DSName   string // NDHGNAME
	Proc     string // NDHGPROC (RSCS file name)
	Step     string // NDHGSTEP (RSCS file type)
	Binary   bool
	RecFM    byte
	LRECL    int
	Punch    bool     // send as a punch file (binary) instead of print
	OrigNode string   // origin node ("" = us); kept when forwarding
	Hops     uint16   // NJHGHOPS to send (forwarded files: received + 1)
	CC       byte     // carriage control kind of the records (CCNone...)
	Records  [][]byte // EBCDIC (text) or raw (binary) records, no CC
	Queued   time.Time
}

// Carriage control kinds of a received data record (SRCB bits 10cc....).
const (
	CCNone    = 0
	CCMachine = 1
	CCASA     = 2
	CCPage    = 3
)

// DataRecord is one received logical record.
type DataRecord struct {
	CC   byte // CCNone, CCMachine, CCASA, CCPage
	Data []byte
}

// InboundDataset is one received data set with its job context.
type InboundDataset struct {
	Stream  byte
	Job     *records.JobHeader     // nil if the sender sent none
	Header  *records.DatasetHeader // nil if the sender sent none
	Trailer *records.JobTrailer    // nil if the sender sent none
	Records []DataRecord
}

// Stats is a snapshot of a session's counters. Counts are kept under
// the session's mutex (the reader goroutine updates some of them).
type Stats struct {
	Primary      bool
	Since        time.Time
	PeerBufSize  int
	LastActivity time.Time     // last block sent or received
	IdleDelay    time.Duration // current idle reply delay
	ActiveIn     int           // inbound transfers in progress
	ActiveOut    int           // outbound transfers in progress
	Draining     bool          // graceful drop in progress

	BlocksIn     int
	BlocksOut    int
	BytesIn      int64 // on the wire, TTB/TTR included
	BytesOut     int64
	KeepaliveIn  int // idle blocks: null buffers, DLE ACK0, empty TTBs
	KeepaliveOut int
	MsgsIn       int // message NMRs
	MsgsOut      int
	CmdsIn       int // command NMRs
	CmdsOut      int
	FilesIn      int
	FilesOut     int
	FileBytesIn  int64 // data record bytes of completed files
	FileBytesOut int64
	RecordsIn    int // data records on SYSIN/SYSOUT streams
	RecordsOut   int
	BCBErrors    int // inbound BCB sequence errors
	NAKsIn       int // NAK / SYN NAK received after signon
}

// Add returns the field-by-field sum of the counters (snapshot fields
// such as Since are taken from a).
func (a Stats) Add(b Stats) Stats {
	a.BlocksIn += b.BlocksIn
	a.BlocksOut += b.BlocksOut
	a.BytesIn += b.BytesIn
	a.BytesOut += b.BytesOut
	a.KeepaliveIn += b.KeepaliveIn
	a.KeepaliveOut += b.KeepaliveOut
	a.MsgsIn += b.MsgsIn
	a.MsgsOut += b.MsgsOut
	a.CmdsIn += b.CmdsIn
	a.CmdsOut += b.CmdsOut
	a.FilesIn += b.FilesIn
	a.FilesOut += b.FilesOut
	a.FileBytesIn += b.FileBytesIn
	a.FileBytesOut += b.FileBytesOut
	a.RecordsIn += b.RecordsIn
	a.RecordsOut += b.RecordsOut
	a.BCBErrors += b.BCBErrors
	a.NAKsIn += b.NAKsIn
	return a
}

// Sub returns a minus b for the counters (used for "stats reset" on a
// live session: the counts since the reset).
func (a Stats) Sub(b Stats) Stats {
	b.BlocksIn, b.BlocksOut, b.BytesIn, b.BytesOut = -b.BlocksIn, -b.BlocksOut, -b.BytesIn, -b.BytesOut
	b.KeepaliveIn, b.KeepaliveOut, b.MsgsIn, b.MsgsOut = -b.KeepaliveIn, -b.KeepaliveOut, -b.MsgsIn, -b.MsgsOut
	b.CmdsIn, b.CmdsOut, b.FilesIn, b.FilesOut = -b.CmdsIn, -b.CmdsOut, -b.FilesIn, -b.FilesOut
	b.FileBytesIn, b.FileBytesOut, b.RecordsIn, b.RecordsOut = -b.FileBytesIn, -b.FileBytesOut, -b.RecordsIn, -b.RecordsOut
	b.BCBErrors, b.NAKsIn = -b.BCBErrors, -b.NAKsIn
	return a.Add(b)
}

// NAKError is returned when the peer answers OPEN with NAK.
type NAKError struct{ Reason byte }

func (e *NAKError) Error() string { return fmt.Sprintf("peer sent NAK reason %02X", e.Reason) }

// Session end reasons that tell the node whether to reconnect.
var (
	ErrPeerSignoff = errors.New("peer signed off")
	ErrLocalDrop   = errors.New("dropped by operator")
	ErrDead        = errors.New("keepalive timeout: no traffic from peer")
	ErrPeerRestart = errors.New("peer restarted the line (SOH ENQ)")
	ErrClosed      = errors.New("link is not active")
	ErrBCBSequence = errors.New("BCB sequence error")
)
