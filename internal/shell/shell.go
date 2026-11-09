// Copyright 2026 by moshix

package shell

import (
	"fmt"
	"strings"
	"time"

	"gonje/internal/config"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/node"
)

// Backend is what the shell needs from the node (node.Node implements it;
// tests use a fake).
type Backend interface {
	Status() []node.LinkStatus
	Local() node.LocalInfo
	Connect(name string) error
	Drop(name string) error
	Drain(name string) (string, error)
	SendMessage(node, user, text string) error
	SendCommand(node, cmd string) error
	SendFile(node, user, path string) (*link.OutboundFile, error)
	SetLocalName(name string) (int, error)
	SetListen(ip string, port int) error
	SetTimeout(sec int) error
	RemoteConfig(name string) (config.Remote, error)
	DefineLink(rc config.Remote) error
	ChangeLink(rc config.Remote) error
	DeleteLink(name string) (int, error)
	Counters() node.Counters
	Routes() []node.RouteStatus
	SetRoute(rt config.Route) error
	DeleteRoute(dest string) error
	SetDefaultRoute(name string) error
	ResetCounters(name string) error
}

// Settings are the shell's static parameters.
type Settings struct {
	Version     string
	ConfigPath  string // file that "save" writes to (-c)
	SendDir     string
	RecvDir     string
	LogPath     string
	Level       int  // panel level 1-3
	Split       bool // start in split view
	SplitRatio  int  // bottom panel height in percent
	HistoryPath string
	Color       bool         // ANSI colors (tty and no NO_COLOR)
	Unicode     bool         // Unicode box drawing (else ASCII frames)
	MsgPane     int          // rows of the messages pane (default 6, min 3)
	MsgBell     bool         // ring the terminal bell on an incoming message
	Log         *logx.Logger // operator commands and their results
}

// Shell holds the mode-independent operator console state. All its
// methods run on the terminal goroutine.
type Shell struct {
	be      Backend
	set     Settings
	level   int
	split   bool
	events  *Panel // incoming events (bottom pane in split view)
	results *Panel // command results (top pane in split view)
	mixed   *Panel // results and events interleaved (split off)
	msgs    *Panel // incoming messages and responses (own pane)
	editor  *Editor
	box     Box
	now     func() time.Time

	rows, cols  int // terminal size of the last Render
	width       int // inner width of the last Render (help layout)
	bottomLines int // events pane size set with Ctrl-G/Ctrl-V (0 = ratio)
	noteText    string
	noteUntil   time.Time
	quitting    bool
	focus       int          // scroll focus (focusCommands/Events/Messages)
	msgLines    int          // messages pane height
	msgNew      bool         // a message arrived while the pane was scrolled back
	bell        bool         // ring the bell at the next draw
	pg          *pager       // non-nil while long output is being paged
	held        []link.Event // events held back while paging (single pane)
}

// New creates a shell.
func New(be Backend, set Settings) *Shell {
	if set.Level < 1 || set.Level > 3 {
		set.Level = 1
	}
	if set.SplitRatio < 10 || set.SplitRatio > 90 {
		set.SplitRatio = 40
	}
	if set.Version == "" {
		set.Version = node.Version
	}
	box := asciiBox
	if set.Unicode {
		box = unicodeBox
	}
	return &Shell{be: be, set: set, level: set.Level, split: set.Split,
		events: NewPanel(PanelMax), results: NewPanel(ResultsMax), mixed: NewPanel(ResultsMax),
		msgs: NewPanel(PanelMax), msgLines: msgPaneDefault(set.MsgPane),
		editor: NewEditor(LoadHistory(set.HistoryPath)), box: box, now: time.Now}
}

// Level returns the current panel level.
func (s *Shell) Level() int { return s.level }

// addResult shows command output in the results pane and the mixed pane.
func (s *Shell) addResult(lines ...Line) {
	s.results.Add(lines...)
	s.mixed.Add(lines...)
}

// versionText is just "gonje 0.5": no Go compiler or platform details.
func (s *Shell) versionText() string {
	return "gonje " + s.set.Version
}

// Banner is shown at startup: version, local node, listener and links.
func (s *Shell) Banner() []Line {
	li := s.be.Local()
	listen := li.Listen
	if listen == "" {
		listen = "(not listening)"
	}
	return []Line{
		SegLine(Seg{"gonje " + s.set.Version, BoldCyan}),
		SegLine(Seg{"local node ", Default}, Seg{li.Name, BoldYellow}, Seg{" listening on ", Default},
			Seg{listen, Cyan}, Seg{fmt.Sprintf(", %d link(s) defined", li.Links), Default}),
		{Text: "type help for commands, quit to exit", Color: Dim},
	}
}

// prompt returns the prompt text and its colored form, e.g.
// "NODEA [1/2]> " with the name highlighted and the link count colored
// by how many links are up.
func (s *Shell) prompt() (string, []Seg) {
	li := s.be.Local()
	c := Green
	switch {
	case li.Links > 0 && li.Up == 0:
		c = Red
	case li.Up < li.Links:
		c = Yellow
	}
	count := fmt.Sprintf("[%d/%d]", li.Up, li.Links)
	segs := []Seg{{li.Name, BoldCyan}, {" ", Default}, {count, c}, {"> ", Default}}
	return li.Name + " " + count + "> ", segs
}

// ---------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------

// eventStyle maps an event kind to its tag and color.
func eventStyle(k link.Kind) (string, Color) {
	switch k {
	case link.EvError:
		return "ERR", Red
	case link.EvLink, link.EvSignon:
		return "LINK", Yellow
	case link.EvStream:
		return "STRM", Yellow
	case link.EvMsgIn, link.EvMsgOut:
		return "MSG", Cyan
	case link.EvCmdIn, link.EvCmdOut:
		return "CMD", Magenta
	case link.EvRspIn, link.EvRspOut:
		return "RSP", Magenta
	case link.EvFileIn, link.EvFileOut:
		return "FILE", Green
	case link.EvProgress:
		return "PROG", Green
	case link.EvDump:
		return "DUMP", Dim
	}
	return "INFO", Default
}

// direction marks inbound/outbound events.
func direction(k link.Kind) string {
	switch k {
	case link.EvMsgIn, link.EvCmdIn, link.EvRspIn, link.EvFileIn:
		return "<"
	case link.EvMsgOut, link.EvCmdOut, link.EvRspOut, link.EvFileOut:
		return ">"
	}
	return " "
}

// dumpSegs colors one hex dump line: offset dim, hex normal, the EBCDIC
// character column (from column 56, see ebcdic.HexDump) in yellow.
func dumpSegs(l string) Line {
	const textAt = 56
	if len(l) < 6 {
		return Line{Text: "  " + l, Color: Dim, Cont: "  "}
	}
	segs := []Seg{{"  ", Default}, {l[:4], Dim}}
	if len(l) > textAt {
		segs = append(segs, Seg{l[4:textAt], Default}, Seg{l[textAt:], Yellow})
	} else {
		segs = append(segs, Seg{l[4:], Default})
	}
	ln := SegLine(segs...)
	ln.Color, ln.Cont = Dim, "  "
	return ln
}

// FormatEvent renders an event as logical lines (wrapped to the screen
// width only when drawn):
//
//	08:51:02 MSG  < NODEB from NODEA/ALICE to BOB: hello
//	  0000  E3 C5 ...                       (L3 dump lines)
func FormatEvent(ev link.Event) []Line {
	tag, color := eventStyle(ev.Kind)
	head := SegLine(Seg{ev.Time.Format("15:04:05") + " ", Dim},
		Seg{fmt.Sprintf("%-4s %s ", tag, direction(ev.Kind)), color},
		Seg{ev.Node, Bold}, Seg{" " + ev.Text, color})
	head.Color, head.Cont = color, "         "
	out := []Line{head}
	for _, l := range ev.Lines {
		out = append(out, dumpSegs(l))
	}
	return out
}

// HandleEvent filters an event by panel level and stores it in the
// events and mixed panes. It reports whether anything changed.
func (s *Shell) HandleEvent(ev link.Event) bool {
	if ev.Level > s.level || s.holdEvent(ev) {
		return false
	}
	if ev.Kind == link.EvKeepalive {
		clock := ev.Time.Format("15:04:05")
		s.events.AddKeepalive(ev.Node, clock)
		s.mixed.AddKeepalive(ev.Node, clock)
		return true
	}
	lines := FormatEvent(ev)
	if ev.Kind == link.EvMsgIn || ev.Kind == link.EvRspIn {
		// Messages and responses have their own pane (not duplicated in
		// the events pane).
		if s.msgs.Scroll() > 0 {
			s.msgNew = true
		}
		lines[0].BlinkUntil = s.now().Add(blinkFor)
		s.msgs.Add(lines...)
		s.bell = s.set.MsgBell
		return true
	}
	s.events.Add(lines...)
	s.mixed.Add(lines...)
	return true
}

// msgPaneDefault is the configured messages pane height or 6.
func msgPaneDefault(n int) int {
	if n < minPane {
		return 6
	}
	return n
}

// ---------------------------------------------------------------------
// Command execution
// ---------------------------------------------------------------------

// Result of a command.
type Result struct {
	Lines []Line
	Quit  bool
	Split int // +1 switch to split view, -1 to single pane, 0 no change
}

// isErrorLine tells error results (logged at Error level) from other
// red lines such as a DOWN link in the status table.
func isErrorLine(text string) bool {
	for _, p := range []string{"error: ", "usage: ", "unknown command "} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

func ok(text string) Line { return Line{Text: text, Color: Green} }

// errText is a red error line with a bold "error:" prefix.
func errText(msg string) Line {
	l := SegLine(Seg{"error: ", BoldRed}, Seg{msg, Red})
	l.Color = Red
	return l
}

func errLine(err error) Line { return errText(err.Error()) }

// warnLine is a yellow warning.
func warnLine(text string) Line { return Line{Text: text, Color: Yellow} }

// Exec runs one command line. The command and every result line are
// written to the log (errors at Error level, never with colors), so the
// log shows what the operator did and what the screen said.
func (s *Shell) Exec(line string) Result {
	if strings.TrimSpace(line) != "" {
		s.set.Log.Info("operator: " + line)
	}
	res := s.exec(line)
	for i, l := range res.Lines {
		if res.Lines[i].Cont == "" {
			res.Lines[i].Cont = "  "
		}
		text := strings.TrimRight(l.Text, " ")
		if isErrorLine(text) {
			s.set.Log.Error("shell: " + text)
		} else {
			s.set.Log.Info("shell: " + text)
		}
	}
	return res
}

// submit runs a command line and shows it with its output. Submitting
// snaps every pane back to live.
func (s *Shell) submit(line string) Result {
	ptext, psegs := s.prompt()
	echo := SegLine(append(psegs, Seg{line, Bold})...)
	echo.Text = ptext + line
	s.addResult(echo)
	res := s.Exec(line)
	s.showResult(res.Lines, 1) // the echo line takes one row of the page
	s.events.Live()
	s.mixed.Live()
	s.results.Live()
	return res
}
