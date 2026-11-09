// Copyright 2026 by moshix

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gonje/internal/config"
	"gonje/internal/instance"
	"gonje/internal/logx"
	"gonje/internal/nje/link"
	"gonje/internal/node"
)

// cliOpts are the parsed command line.
type cliOpts struct {
	config  string
	logPath string
	split   optBool
	version bool
	wait    int      // -w: seconds to wait for command responses
	mode    string   // "" = interactive, else send|cmd|msg (batch)
	args    []string // batch operands
}

// parseArgs reads the command line. -c is the config file, except that
// "-c send|cmd|msg" selects batch mode (the user's syntax); -f names the
// config file in both modes.
func parseArgs(args []string, stderr io.Writer) (*cliOpts, error) {
	o := &cliOpts{}
	fs := flag.NewFlagSet("gonje", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := fs.String("c", "", "config file, or batch action send|cmd|msg")
	f := fs.String("f", "", "config file (default gonje.cnf)")
	fs.StringVar(&o.logPath, "log", "gonje.log", "log file")
	fs.Var(&o.split, "split", "start in split view (-split=false: single pane; default from config)")
	fs.BoolVar(&o.version, "v", false, "print the version and exit")
	fs.IntVar(&o.wait, "w", 30, "batch cmd: seconds to wait for responses")
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: gonje [-c|-f gonje.cnf] [-split=true|false] [-log gonje.log] [-v]
       gonje [-f gonje.cnf] -c send <file> <node> <user>
       gonje [-f gonje.cnf] -c cmd [-w secs] <node> <command>
       gonje [-f gonje.cnf] -c msg <node> <user> <text>
batch exit codes: 0 done, 1 error, 3 interrupted (SIGINT/SIGTERM)
batch mode needs a direct link to <node> (no routing)
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	o.config = "gonje.cnf"
	switch *c {
	case "send", "cmd", "msg":
		o.mode, o.args = *c, fs.Args()
	case "":
	default:
		o.config = *c
	}
	if *f != "" {
		o.config = *f
	}
	if o.wait < 1 {
		return nil, fmt.Errorf("-w must be at least 1 second")
	}
	if o.mode == "" && len(fs.Args()) > 0 && !o.version {
		return nil, fmt.Errorf("unexpected arguments %q (batch mode is -c send|cmd|msg ...)", fs.Args())
	}
	return o, nil
}

// batchJob is one validated batch action.
type batchJob struct {
	mode  string
	node  string
	user  string
	file  string
	text  string
	wait  time.Duration // cmd: total wait for responses
	quiet time.Duration // cmd: finish this long after the last response
	grace time.Duration // msg: wait after the NMR was written
	setup time.Duration // connect + signon timeout
	// cfgPath: the config file (to find a running interactive gonje
	// when our listener port is busy).
	cfgPath string
}

// newBatchJob checks the operands of a batch action:
//
//	send <file> <node> <user>
//	cmd  <node> <command text...>
//	msg  <node> <user> <text...>
func newBatchJob(o *cliOpts) (*batchJob, error) {
	j := &batchJob{mode: o.mode, wait: time.Duration(o.wait) * time.Second,
		quiet: 5 * time.Second, grace: time.Second, setup: 60 * time.Second}
	a := o.args
	switch o.mode {
	case "send":
		if len(a) != 3 {
			return nil, fmt.Errorf("usage: gonje -c send <file> <node> <user>")
		}
		j.file, j.node, j.user = a[0], a[1], a[2]
	case "cmd":
		if len(a) < 2 {
			return nil, fmt.Errorf("usage: gonje -c cmd <node> <command>")
		}
		j.node, j.text = a[0], strings.Join(a[1:], " ")
	case "msg":
		if len(a) < 3 {
			return nil, fmt.Errorf("usage: gonje -c msg <node> <user> <text>")
		}
		j.node, j.user, j.text = a[0], a[1], strings.Join(a[2:], " ")
	}
	j.node = strings.ToUpper(j.node)
	return j, nil
}

// batch runs one job: it dials the one remote (no listener, no UI),
// waits for signon, does the action, then closes WITHOUT signoff.
// Everything the operator should see goes to out (stderr) as plain
// lines; the log gets it too.
type batch struct {
	j      *batchJob
	n      *node.Node
	out    io.Writer
	log    *logx.Logger
	events <-chan link.Event
	up     bool      // signed on: from now on a lost link is fatal
	got    int       // command responses received from the node
	last   time.Time // when the last one arrived
	intr   <-chan os.Signal
	// interrupted: SIGINT/SIGTERM arrived; fail() then reports
	// "interrupted" and exits with exitInterrupted.
	interrupted bool
}

// Batch exit codes.
const (
	exitOK          = 0
	exitError       = 1
	exitInterrupted = 3
)

// batchSignals subscribes to the signals that interrupt a batch job (a
// variable so tests can send a fake signal).
var batchSignals = func() (<-chan os.Signal, func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	return c, func() { signal.Stop(c) }
}

func (b *batch) say(s string) {
	fmt.Fprintln(b.out, "gonje: "+s)
	b.log.Info("batch: " + s)
}

func (b *batch) fail(s string) int {
	if b.interrupted {
		b.say("interrupted")
		return exitInterrupted
	}
	b.say("error: " + s)
	return exitError
}

// show prints inbound traffic that arrives while connected.
func (b *batch) show(ev link.Event) {
	switch ev.Kind {
	case link.EvMsgIn, link.EvRspIn, link.EvCmdIn:
		fmt.Fprintf(b.out, "%s: %s\n", ev.Node, ev.Text)
		if ev.Node == b.j.node && ev.Kind == link.EvRspIn {
			// Only real responses count (the node classifies a message
			// addressed to us after our command as one); an unrelated
			// message is shown but does not end the wait. Counted here
			// so a fast response that arrives while we are still
			// waiting for the command to be written is not missed.
			b.got++
			b.last = time.Now()
		}
	case link.EvFileIn:
		b.say("received file " + ev.Text)
	case link.EvError:
		b.say(ev.Node + ": " + ev.Text)
	}
}

// waitEvent shows events until match returns done (or an error), the
// link goes down, or the timeout expires. tick runs every 20 ms and may
// also finish the wait.
func (b *batch) waitEvent(timeout time.Duration, match func(link.Event) (bool, error), tick func() bool) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case ev := <-b.events:
			b.show(ev)
			if b.up && ev.Kind == link.EvLink && ev.Node == b.j.node && strings.HasPrefix(ev.Text, "link down") {
				return fmt.Errorf("%s: %s", b.j.node, ev.Text)
			}
			if match != nil {
				if done, err := match(ev); done || err != nil {
					return err
				}
			}
		case <-t.C:
			if tick != nil && tick() {
				return nil
			}
		case <-deadline.C:
			return fmt.Errorf("timeout after %s", timeout)
		case <-b.intr:
			b.interrupted = true
			return fmt.Errorf("interrupted")
		}
	}
}

// stats returns the live session counters of the link.
func (b *batch) stats() link.Stats {
	for _, st := range b.n.Status() {
		if st.Name == b.j.node {
			return st.Stats
		}
	}
	return link.Stats{}
}

func runBatch(j *batchJob, cfg *config.Config, log *logx.Logger, out io.Writer, opts node.Options) int {
	b := &batch{j: j, out: out, log: log}
	intr, stop := batchSignals()
	defer stop()
	b.intr = intr
	if cfg.Remote(j.node) == nil {
		// Batch mode dials the node itself: routed destinations are not
		// supported (use the via node's name, or the interactive shell).
		return b.fail(fmt.Sprintf("no remote node %s in the configuration (batch mode needs a direct link)", j.node))
	}
	// The general "timeout" shortens the signon window if it is smaller.
	if t := time.Duration(cfg.Timeout) * time.Second; t > 0 && t < j.setup {
		j.setup = t
	}
	// Some peers (HOUCICS) prefer to call us and, right after a session,
	// ignore or NAK our dial while they dial us. So batch mode also
	// listens (for the target node only) and keeps redialling every 2-5 s
	// within the signon window; whichever signs on first wins.
	opts.Log = log
	opts.OnlyRemote = j.node
	opts.PreferInbound = true
	if opts.RetryMin == 0 {
		opts.RetryMin, opts.RetryMax = 2*time.Second, 5*time.Second
	}
	if opts.StartJitter == 0 {
		opts.StartJitter = 10 * time.Millisecond
	}
	b.n = node.New(cfg, opts)
	id, events := b.n.Bus().Subscribe(4096)
	defer b.n.Bus().Unsubscribe(id)
	b.events = events
	defer b.n.Stop() // closes the link WITHOUT signoff
	if err := b.n.Listen(); err != nil {
		// The port is busy (usually the interactive gonje): dial only,
		// and say who holds the port.
		why := err.Error()
		if j.cfgPath != "" {
			why = instance.Diagnose(cfg, j.cfgPath, err)
		}
		b.say(fmt.Sprintf("cannot listen on %s:%d (%s); dialling only", cfg.LocalIP, cfg.LocalPort, why))
	}
	b.n.Start()
	err := b.waitEvent(j.setup, func(ev link.Event) (bool, error) {
		if ev.Kind == link.EvLink && ev.Node == j.node && strings.HasPrefix(ev.Text, "connect failed") {
			b.say(j.node + ": " + ev.Text) // progress; we keep trying
		}
		return ev.Kind == link.EvLink && ev.Node == j.node && strings.HasPrefix(ev.Text, "link up"), nil
	}, nil)
	if err != nil {
		return b.fail(fmt.Sprintf("connect: no signon with %s: %v", j.node, err))
	}
	b.up = true
	b.say("connected to " + j.node)
	switch j.mode {
	case "send":
		return b.send()
	case "cmd":
		return b.cmd()
	}
	return b.msg()
}

func (b *batch) msg() int {
	before := b.stats().MsgsOut
	if err := b.n.SendMessage(b.j.node, b.j.user, b.j.text); err != nil {
		return b.fail(err.Error())
	}
	// Written to the socket once the session counted it, then a grace
	// period so the close does not cut it off.
	if err := b.waitEvent(30*time.Second, nil, func() bool { return b.stats().MsgsOut > before }); err != nil {
		return b.fail("message not sent: " + err.Error())
	}
	_ = b.waitEvent(b.j.grace, nil, nil) // keep showing inbound traffic
	b.say(fmt.Sprintf("message sent to %s/%s", b.j.node, strings.ToUpper(b.j.user)))
	return 0
}

func (b *batch) cmd() int {
	before := b.stats().CmdsOut
	b.got = 0
	if err := b.n.SendCommand(b.j.node, b.j.text); err != nil {
		return b.fail(err.Error())
	}
	if err := b.waitEvent(30*time.Second, nil, func() bool { return b.stats().CmdsOut > before }); err != nil {
		return b.fail("command not sent: " + err.Error())
	}
	b.say("command sent to " + b.j.node + ", waiting for responses")
	// Responses can take 10-30 s: wait up to j.wait in total, but once
	// one has arrived, finish after j.quiet without another one.
	err := b.waitEvent(b.j.wait, nil, func() bool { return b.got > 0 && time.Since(b.last) >= b.j.quiet })
	got := b.got
	if got == 0 {
		return b.fail(fmt.Sprintf("no response from %s within %s", b.j.node, b.j.wait))
	}
	if b.interrupted || (err != nil && !strings.HasPrefix(err.Error(), "timeout")) {
		return b.fail(err.Error())
	}
	b.say(fmt.Sprintf("%d response line(s) from %s", got, b.j.node))
	return 0
}

func (b *batch) send() int {
	f, err := b.n.SendFile(b.j.node, b.j.user, b.j.file)
	if err != nil {
		return b.fail(err.Error())
	}
	var size int64
	for _, r := range f.Records {
		size += int64(len(r))
	}
	// At least 60 s, plus time for big files (20 KB/s worst case).
	timeout := 60*time.Second + time.Duration(size/(20<<10))*time.Second
	unconfirmed := false
	err = b.waitEvent(timeout, func(ev link.Event) (bool, error) {
		if ev.Node != b.j.node {
			return false, nil
		}
		switch {
		case ev.Kind == link.EvFileOut && strings.Contains(ev.Text, f.Name+" delivered"):
			return true, nil
		case ev.Kind == link.EvFileOut && strings.Contains(ev.Text, f.Name+" sent to") &&
			strings.Contains(ev.Text, "did not confirm"):
			unconfirmed = true
			return true, nil
		case ev.Kind == link.EvError && strings.Contains(ev.Text, "file "+f.Name+" failed"):
			return true, fmt.Errorf("%s", ev.Text)
		case ev.Kind == link.EvStream && strings.Contains(ev.Text, "stays queued"):
			return true, fmt.Errorf("transfer refused by %s", b.j.node)
		}
		return false, nil
	}, nil)
	if err != nil {
		return b.fail(err.Error())
	}
	if unconfirmed {
		b.say(fmt.Sprintf("%s sent to %s/%s (peer did not confirm receipt)", f.Name, f.ToNode, f.ToUser))
		return 0
	}
	b.say(fmt.Sprintf("%s sent to %s/%s (%d records)", f.Name, f.ToNode, f.ToUser, len(f.Records)))
	return 0
}

// openLog opens the log file and records config warnings.
func openLog(path string, cfg *config.Config, cfgPath string) (*logx.Logger, error) {
	log, err := logx.Open(path, logx.ParseLevel(cfg.LogLevel))
	if err != nil {
		return nil, err
	}
	log.Info(fmt.Sprintf("gonje %s starting as %s, config %s", node.Version, cfg.LocalNode, cfgPath))
	for _, w := range cfg.Warnings {
		log.Warn(w)
	}
	for _, d := range []string{cfg.SendDir, cfg.RecvDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Close()
			return nil, err
		}
	}
	return log, nil
}
