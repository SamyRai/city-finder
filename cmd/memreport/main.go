// Command memreport measures city-finder's index footprint end to end and
// dumps a deterministic query transcript, so a change to index layout can be
// proven to save memory/disk WITHOUT changing a single answer.
//
//	memreport gen     -dir D -n 2000000          # synthetic GeoNames-format dataset
//	memreport measure -config D/config.json      # cold build (no indexes yet)
//	memreport measure -config D/config.json \
//	          -dump answers.jsonl -profile heap.pprof   # warm start + transcript
//
// Every `measure` should run in a fresh process: peak RSS (VmHWM) and heap
// figures are per process. It uses only the stable public API
// (initializer.Initialize and the finder facade), so the same tool runs
// against an older commit for the "before" side.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
)

const usage = "usage: memreport gen|measure [flags]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	default:
		log.Fatal(err)
	}
}

// errUsage marks a command line that cannot be run (as opposed to a run that
// failed).
var errUsage = errors.New("usage")

func usageErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errUsage}, a...)...)
}

// run dispatches one subcommand. Flag parsing and validation live in the
// parse* functions, so every rejection is testable without running anything.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("%s", usage)
	}
	switch args[0] {
	case "gen":
		o, err := parseGenFlags(args[1:], stderr)
		if err != nil {
			return err
		}
		return genDataset(o.dir, o.n, o.seed)
	case "measure":
		o, err := parseMeasureFlags(args[1:], stderr)
		if err != nil {
			return err
		}
		return measure(ctx, o, stdout)
	default:
		return usageErr("unknown subcommand %q\n%s", args[0], usage)
	}
}

type genOptions struct {
	dir  string
	n    int
	seed int64
}

func parseGenFlags(args []string, stderr io.Writer) (genOptions, error) {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o genOptions
	fs.StringVar(&o.dir, "dir", "", "output directory")
	fs.IntVar(&o.n, "n", 2_000_000, "city rows")
	fs.Int64Var(&o.seed, "seed", 1, "generator seed")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch {
	case fs.NArg() > 0:
		return o, usageErr("gen: unexpected argument %q", fs.Arg(0))
	case o.dir == "":
		return o, usageErr("gen: -dir required")
	case o.n <= 0:
		return o, usageErr("gen: -n must be > 0")
	}
	return o, nil
}

func parseMeasureFlags(args []string, stderr io.Writer) (measureOptions, error) {
	fs := flag.NewFlagSet("measure", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o measureOptions
	fs.StringVar(&o.cfgPath, "config", "", "config.json")
	fs.StringVar(&o.dumpPath, "dump", "", "write the query transcript here")
	fs.StringVar(&o.profilePath, "profile", "", "write an inuse heap profile here")
	fs.DurationVar(&o.fuzzyTimeout, "fuzzy-timeout", defaultFuzzyTimeout, "give up when the fuzzy index is still not built after this long")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch {
	case fs.NArg() > 0:
		return o, usageErr("measure: unexpected argument %q", fs.Arg(0))
	case o.cfgPath == "":
		return o, usageErr("measure: -config required")
	case o.fuzzyTimeout <= 0:
		return o, usageErr("measure: -fuzzy-timeout must be > 0")
	}
	return o, nil
}
