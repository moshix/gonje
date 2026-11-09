// Copyright 2026 by moshix

// Command gonje is an NJE-over-TCP/IP node with an operator shell.
//
//	gonje [-c|-f gonje.cnf] [-split=true|false] [-log gonje.log]
//	gonje [-f gonje.cnf] -c send <file> <node> <user>
//	gonje [-f gonje.cnf] [-w secs] -c cmd <node> <command...>
//	gonje [-f gonje.cnf] -c msg <node> <user> <text...>
//	gonje -v
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"

	"gonje/internal/config"
	"gonje/internal/instance"
	"gonje/internal/node"
	"gonje/internal/shell"
)

// version is the gonje release version. build.sh reads this line to
// name the binaries, so keep the form: var version = "x.y"
var version = "0.9.1"

func main() {
	node.Version = version
	os.Exit(run())
}

// fail prints an error to stderr.
func fail(msg string) int {
	fmt.Fprintln(os.Stderr, "gonje: "+msg)
	return 1
}

func run() int {
	o, err := parseArgs(os.Args[1:], os.Stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return fail(err.Error())
	}
	if o.version {
		fmt.Println("gonje " + version)
		return 0
	}
	cfgPath, logPath := &o.config, &o.logPath
	split := o.split

	// -o tokens follow the config file; with -o the file is optional
	// unless -c/-f named it.
	cfg, hasFile, err := config.LoadWithOptions(*cfgPath, o.explicit || len(o.opts) == 0, o.opts)
	if err != nil {
		if strings.HasPrefix(err.Error(), "-o") {
			return fail(err.Error())
		}
		return fail(fmt.Sprintf("config %s: %v", *cfgPath, err))
	}
	// lockPath is the config file for the pid lock and port diagnosis
	// ("" without a file: gonje.pid in the current directory).
	lockPath := *cfgPath
	if !hasFile {
		lockPath = ""
	}
	// gonje.state (learned DLE STX framing) lives next to gonje.pid.
	statePath := filepath.Join(filepath.Dir(instance.PidPath(lockPath)), node.StateFile)
	if split.set {
		cfg.SplitView = split.val
	}
	log, err := openLog(*logPath, cfg, lockPath)
	if err != nil {
		return fail(err.Error())
	}
	defer log.Close()
	for _, opt := range o.opts {
		// Never the raw text: it may hold passwords.
		log.Info("-o " + config.MaskSecrets(opt))
	}

	if o.mode != "" {
		// Batch mode: one action, plain lines on stderr, no listener/UI.
		j, err := newBatchJob(o)
		if err != nil {
			return fail(err.Error())
		}
		j.cfgPath = lockPath
		return runBatch(j, cfg, log, os.Stderr, node.Options{StatePath: statePath})
	}

	// One interactive gonje per config: the pid file next to the config
	// is checked (and taken) before binding, so a second instance is
	// refused even when it would bind another address.
	lock, stale, err := instance.Acquire(lockPath, instance.NewInfo(version, cfg, lockPath))
	if err != nil {
		log.Error(err.Error())
		return fail(err.Error())
	}
	if stale != nil {
		log.Info(fmt.Sprintf("removed stale %s of pid %d (started %s)", instance.PidFile, stale.PID,
			stale.Started.Format("2006-01-02 15:04")))
	}
	defer lock.Release()
	n := node.New(cfg, node.Options{Log: log, StatePath: statePath})
	if err := n.Listen(); err != nil {
		msg := instance.Diagnose(cfg, lockPath, err)
		log.Error("listen: " + msg)
		return fail(msg)
	}
	id, events := n.Bus().Subscribe(4096)
	defer n.Bus().Unsubscribe(id)
	n.Start()

	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sig
		close(done)
	}()

	sh := shell.New(n, shell.Settings{
		Version:     version,
		ConfigPath:  *cfgPath,
		ConfigInit:  configInit(hasFile, o.opts),
		Color:       term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "",
		Unicode:     shell.UseUnicode(os.Getenv),
		SendDir:     cfg.SendDir,
		RecvDir:     cfg.RecvDir,
		LogPath:     *logPath,
		Level:       cfg.PanelLevel,
		Split:       cfg.SplitView,
		SplitRatio:  cfg.SplitRatio,
		MsgPane:     cfg.MsgPane,
		MsgBell:     cfg.MsgBell,
		HistoryPath: shell.DefaultHistoryPath(),
		Log:         log,
	})
	// A panic on this goroutine unwinds through Shell.Run, whose deferred
	// handlers restore the terminal; here we log it and sign off cleanly.
	// Protocol goroutines recover their own panics (link/node).
	defer func() {
		if r := recover(); r != nil {
			log.Error(fmt.Sprintf("panic: %v", r))
			n.Stop()
			lock.Release() // os.Exit skips the deferred release
			fmt.Fprintf(os.Stderr, "gonje: internal error: %v (see log)\n", r)
			os.Exit(2)
		}
	}()
	err = sh.Run(os.Stdin, os.Stdout, events, done)
	n.Stop()
	log.Info("gonje stopped")
	if err != nil {
		return fail(err.Error())
	}
	return 0
}

// optBool is a boolean flag that remembers whether it was given, so
// -split / -split=true / -split=false override the config file and its
// absence keeps the config value.
type optBool struct{ set, val bool }

func (o *optBool) String() string   { return strconv.FormatBool(o.val) }
func (o *optBool) IsBoolFlag() bool { return true }
func (o *optBool) Set(v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	o.set, o.val = true, b
	return nil
}

// configInit is the text of a new config file written by the shell's
// "save" when gonje runs without one: the -o options, one token per line.
func configInit(hasFile bool, opts []string) string {
	if hasFile || len(opts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# gonje.cnf written by gonje from its -o options\n")
	for _, o := range opts {
		for _, line := range strings.Split(o, "\n") {
			sb.WriteString(strings.TrimSpace(line) + "\n")
		}
	}
	return sb.String()
}
