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
	"strconv"
	"syscall"

	"golang.org/x/term"

	"gonje/internal/config"
	"gonje/internal/node"
	"gonje/internal/shell"
)

// version is the gonje release version. build.sh reads this line to
// name the binaries, so keep the form: var version = "x.y"
var version = "0.4"

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

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fail(fmt.Sprintf("config %s: %v", *cfgPath, err))
	}
	if split.set {
		cfg.SplitView = split.val
	}
	log, err := openLog(*logPath, cfg, *cfgPath)
	if err != nil {
		return fail(err.Error())
	}
	defer log.Close()

	if o.mode != "" {
		// Batch mode: one action, plain lines on stderr, no listener/UI.
		j, err := newBatchJob(o)
		if err != nil {
			return fail(err.Error())
		}
		return runBatch(j, cfg, log, os.Stderr, node.Options{})
	}

	n := node.New(cfg, node.Options{Log: log})
	if err := n.Listen(); err != nil {
		return fail(fmt.Sprintf("listen on %s:%d: %v", cfg.LocalIP, cfg.LocalPort, err))
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
		Color:       term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "",
		Unicode:     shell.UseUnicode(os.Getenv),
		SendDir:     cfg.SendDir,
		RecvDir:     cfg.RecvDir,
		LogPath:     *logPath,
		Level:       cfg.PanelLevel,
		Split:       cfg.SplitView,
		SplitRatio:  cfg.SplitRatio,
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
