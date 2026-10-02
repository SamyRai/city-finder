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
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: memreport gen|measure [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		fs := flag.NewFlagSet("gen", flag.ExitOnError)
		dir := fs.String("dir", "", "output directory")
		n := fs.Int("n", 2_000_000, "city rows")
		seed := fs.Int64("seed", 1, "generator seed")
		_ = fs.Parse(os.Args[2:])
		if *dir == "" {
			log.Fatal("-dir required")
		}
		if err := genDataset(*dir, *n, *seed); err != nil {
			log.Fatal(err)
		}
	case "measure":
		fs := flag.NewFlagSet("measure", flag.ExitOnError)
		cfg := fs.String("config", "", "config.json")
		dump := fs.String("dump", "", "write the query transcript here")
		profile := fs.String("profile", "", "write an inuse heap profile here")
		_ = fs.Parse(os.Args[2:])
		if *cfg == "" {
			log.Fatal("-config required")
		}
		if err := measure(*cfg, *dump, *profile); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown subcommand %q", os.Args[1])
	}
}
