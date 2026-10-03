// Command loadgen runs an open-model (constant arrival rate) load test
// against a running city-finder server and prints the saturation curve. See
// docs/benchmarking.md "Load testing" and package internal/loadgen.
//
//	go run ./cmd/loadgen -url http://127.0.0.1:3000 -rates 500,1000,2000,4000 -window 30s
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	o, err := parseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o, os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
}
