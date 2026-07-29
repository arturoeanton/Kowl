package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/robertkrimen/otto/underscore"
)

// Process exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// supervisorInterval is how often Kowl re-resolves its patterns, so it can build a
// watcher around a file that appeared and drop one whose file is gone.
const supervisorInterval = time.Second

type options struct {
	Filename       []string      `short:"f" required:"true" long:"filename" description:"file, directory or glob to observe, repeatable"`
	Script         string        `short:"j" required:"true" long:"javascript" description:"JavaScript file holding the hooks"`
	Interval       time.Duration `short:"m" default:"1s" long:"interval" description:"poll interval, 0 disables polling"`
	FlagNotWatcher bool          `short:"w" long:"flagNotWatcher" description:"disable the filesystem watcher, leaving only polling"`
	Recursive      bool          `short:"r" long:"recursive" description:"watch every directory below a matched directory"`
	MaxWatches     int           `long:"max-watches" default:"4096" description:"how many paths may be watched at once"`
	Exclude        []string      `short:"x" long:"exclude" description:"skip matching paths, repeatable; no separator matches the base name"`
	Debounce       time.Duration `long:"debounce" default:"200ms" description:"quiet period before a burst of write events runs a hook, 0 disables"`
	SelfTrigger    bool          `long:"self-trigger" description:"let a hook that writes an observed file wake itself again"`
	HookTimeout    time.Duration `long:"hook-timeout" default:"30s" description:"how long a hook may run before it is interrupted"`
	ExecTimeout    time.Duration `long:"exec-timeout" default:"60s" description:"how long a kExec command may run"`
	HTTPTimeout    time.Duration `long:"http-timeout" default:"30s" description:"how long a kCli request may take"`
	MaxOutput      int           `long:"max-output" default:"1048576" description:"bytes of stdout and of stderr kept per kExec command"`
	LogLevel       string        `long:"log-level" default:"info" description:"debug, info, warn or error"`
	LogFormat      string        `long:"log-format" default:"text" description:"text or json"`
	Version        bool          `short:"V" long:"version" description:"print the version and exit"`
}

func init() {
	// otto's registry writes Entry.active without a lock, so enabling underscore from a
	// running VM races against every other VM being constructed. Enable it once, before
	// any goroutine exists. Importing the package already registers the source.
	underscore.Enable()
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args, validates the configuration and blocks until the process is
// interrupted. It returns the exit code instead of calling os.Exit so that argument
// handling is testable.
func run(args []string, stdout, stderr io.Writer) int {
	// Checked before parsing: -f and -j are required, and asking a program for its
	// version should not require telling it what to watch.
	if wantsVersion(args) {
		fmt.Fprintln(stdout, versionString())
		return exitOK
	}

	var opts options
	parser := flags.NewParser(&opts, flags.HelpFlag|flags.PassDoubleDash)
	extra, err := parser.ParseArgs(args)
	if err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) && flagsErr.Type == flags.ErrHelp {
			fmt.Fprintln(stdout, err)
			return exitOK
		}
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	// Kowl takes no positional arguments, so one is a mistake: a misplaced value, or a
	// path that was meant to follow -f. Accepting it silently watches the wrong thing.
	if len(extra) > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q: every path needs its own -f\n", extra[0])
		return exitUsage
	}

	level, err := ParseLevel(opts.LogLevel)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	format, err := ParseFormat(opts.LogFormat)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if opts.Interval < 0 {
		fmt.Fprintln(stderr, "-m must be zero or positive")
		return exitUsage
	}
	if opts.FlagNotWatcher && opts.Interval == 0 {
		fmt.Fprintln(stderr, "nothing to do: -w disables the watcher and -m 0 disables polling")
		return exitUsage
	}
	for name, value := range map[string]time.Duration{
		"--hook-timeout": opts.HookTimeout,
		"--exec-timeout": opts.ExecTimeout,
		"--http-timeout": opts.HTTPTimeout,
	} {
		if value <= 0 {
			fmt.Fprintf(stderr, "%s must be positive\n", name)
			return exitUsage
		}
	}
	if opts.Debounce < 0 {
		fmt.Fprintln(stderr, "--debounce must be zero or positive")
		return exitUsage
	}
	if opts.MaxOutput <= 0 {
		fmt.Fprintln(stderr, "--max-output must be positive")
		return exitUsage
	}
	if opts.MaxWatches <= 0 {
		fmt.Fprintln(stderr, "--max-watches must be positive")
		return exitUsage
	}
	if err := ValidatePatterns(opts.Filename); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if err := ValidatePatterns(opts.Exclude); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	logger := NewLogger(stderr, level, format)

	runner := NewRunner(opts.Script)
	runner.timeout = opts.HookTimeout
	runner.config = vmConfig{
		execTimeout: opts.ExecTimeout,
		httpTimeout: opts.HTTPTimeout,
		maxOutput:   opts.MaxOutput,
		logger:      logger,
	}

	hooks, err := runner.DefinedHooks()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if len(hooks) == 0 {
		fmt.Fprintf(stderr, "%s defines none of the known hooks (%s)\n", opts.Script, strings.Join(hookNames, ", "))
		return exitError
	}

	logger.Infof("watching %s with %s (hooks: %s)",
		strings.Join(opts.Filename, ", "), opts.Script, strings.Join(hooks, ", "))

	events := newDispatcher(runner.Run, logger, opts.Debounce, opts.SelfTrigger)

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	var wg sync.WaitGroup
	if !opts.FlagNotWatcher {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Supervise(ctx, WatchConfig{
				Patterns:   opts.Filename,
				Interval:   supervisorInterval,
				Recursive:  opts.Recursive,
				MaxWatches: opts.MaxWatches,
				Exclude:    opts.Exclude,
			}, events.Dispatch, logger)
		}()
	}
	if opts.Interval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Poll(ctx, opts.Filename, opts.Interval, events.Dispatch, logger)
		}()
	}

	<-ctx.Done()
	wg.Wait()
	events.Close()
	logger.Infof("stopped")
	return exitOK
}
