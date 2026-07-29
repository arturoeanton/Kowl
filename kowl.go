package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
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

// supervisorInterval is how often Kowl checks whether the observed file has appeared or
// disappeared, so it can rebuild the fsnotify watcher around it.
const supervisorInterval = time.Second

type options struct {
	Filename       string `short:"f" required:"true" long:"filename" description:"filename that wants to be observed"`
	Script         string `short:"j" required:"true" long:"javascript" description:"Js that wants that executes the actions"`
	Millisecond    int    `short:"m" default:"1000" long:"millisecond" description:"Poll interval in milliseconds, 0 disables polling"`
	FlagNotWatcher bool   `short:"w" long:"flagNotWatcher" description:"Watcher disable"`
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
	var opts options
	parser := flags.NewParser(&opts, flags.HelpFlag|flags.PassDoubleDash)
	if _, err := parser.ParseArgs(args); err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) && flagsErr.Type == flags.ErrHelp {
			fmt.Fprintln(stdout, err)
			return exitOK
		}
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	if opts.Millisecond < 0 {
		fmt.Fprintln(stderr, "-m must be zero or positive")
		return exitUsage
	}
	if opts.FlagNotWatcher && opts.Millisecond == 0 {
		fmt.Fprintln(stderr, "nothing to do: -w disables the watcher and -m 0 disables polling")
		return exitUsage
	}

	runner := NewRunner(opts.Script)
	hooks, err := runner.DefinedHooks()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if len(hooks) == 0 {
		fmt.Fprintf(stderr, "%s defines none of the known hooks (%s)\n", opts.Script, strings.Join(hookNames, ", "))
		return exitError
	}

	logger := log.New(stderr, "", log.LstdFlags)
	logger.Printf("watching %s with %s (hooks: %s)", opts.Filename, opts.Script, strings.Join(hooks, ", "))

	dispatch := func(op, name string) {
		if err := runner.Run(op, name); err != nil {
			// A script is only expected to implement the hooks it cares about; the
			// ones it defines were already reported at startup.
			if errors.Is(err, ErrHookNotDefined) {
				return
			}
			logger.Printf("%s %s: %v", op, name, err)
		}
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	var wg sync.WaitGroup
	if !opts.FlagNotWatcher {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Supervise(ctx, opts.Filename, supervisorInterval, dispatch, logger)
		}()
	}
	if opts.Millisecond > 0 {
		interval := time.Duration(opts.Millisecond) * time.Millisecond
		wg.Add(1)
		go func() {
			defer wg.Done()
			Poll(ctx, opts.Filename, interval, dispatch, logger)
		}()
	}

	<-ctx.Done()
	wg.Wait()
	logger.Println("stopped")
	return exitOK
}
