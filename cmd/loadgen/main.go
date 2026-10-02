// Command loadgen runs an open-model (constant arrival rate) load test
// against a running city-finder server and prints the saturation curve. See
// docs/benchmarking.md "Load testing" and package internal/loadgen.
//
//	go run ./cmd/loadgen -url http://127.0.0.1:3000 -rates 500,1000,2000,4000 -window 30s
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/SamyRai/cityFinder/internal/loadgen"
)

type options struct {
	baseURL       string
	rates         []float64
	window        time.Duration
	warmup        time.Duration
	workload      string
	seed          int64
	maxInFlight   int
	timeout       time.Duration
	stopErrorRate float64
	jsonOut       string
}

func parseFlags(args []string) (options, error) {
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	var o options
	var rates string
	fs.StringVar(&o.baseURL, "url", "http://127.0.0.1:3000", "server base URL")
	fs.StringVar(&rates, "rates", "250,500,1000,2000", "comma-separated offered rates (requests/s), one step each, ascending")
	fs.DurationVar(&o.window, "window", 30*time.Second, "measurement window per rate")
	fs.DurationVar(&o.warmup, "warmup", 10*time.Second, "unrecorded warm-up at the first rate (0 = none)")
	fs.StringVar(&o.workload, "workload", "nearest", "one of: "+strings.Join(loadgen.WorkloadNames, ", "))
	fs.Int64Var(&o.seed, "seed", 42, "workload seed")
	fs.IntVar(&o.maxInFlight, "max-inflight", 4096, "in-flight cap; due requests beyond it count as dropped (errors)")
	fs.DurationVar(&o.timeout, "timeout", 5*time.Second, "per-request timeout (a timeout counts as an error)")
	fs.Float64Var(&o.stopErrorRate, "stop-error-rate", 0.05, "stop the sweep after a step whose error rate exceeds this (past the knee)")
	fs.StringVar(&o.jsonOut, "json", "", "also write the sweep as JSON to this file")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if !loadgen.HasWorkload(o.workload) {
		return o, fmt.Errorf("unknown -workload %q", o.workload)
	}
	for _, f := range strings.Split(rates, ",") {
		r, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil || r <= 0 {
			return o, fmt.Errorf("invalid rate %q", f)
		}
		o.rates = append(o.rates, r)
	}
	if o.window <= 0 {
		return o, fmt.Errorf("-window must be > 0")
	}
	return o, nil
}

func main() {
	o, err := parseFlags(os.Args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Enough paths that a step does not cycle a tiny hot set.
	wl, err := loadgen.NewWorkload(o.workload, 1<<16, o.seed)
	if err != nil {
		log.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{
		MaxIdleConns:        o.maxInFlight,
		MaxIdleConnsPerHost: o.maxInFlight,
		IdleConnTimeout:     90 * time.Second,
	}}
	base := strings.TrimRight(o.baseURL, "/")
	do := func(ctx context.Context, i int) loadgen.Outcome {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+wl.Paths[i%len(wl.Paths)], nil)
		if err != nil {
			return loadgen.Classify(0, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return loadgen.Classify(0, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return loadgen.Classify(resp.StatusCode, nil)
	}

	fmt.Fprintf(os.Stderr, "loadgen: %s workload=%s window=%s rates=%v max-inflight=%d client GOMAXPROCS=%d\n",
		base, o.workload, o.window, o.rates, o.maxInFlight, runtime.GOMAXPROCS(0))
	fmt.Fprintln(os.Stderr, "loadgen: the client shares the machine with nothing else, or its own CPU limits the result")

	step := func(rps float64, window time.Duration) []loadgen.Sample {
		return loadgen.RunStep(ctx, loadgen.StepConfig{RPS: rps, Window: window, MaxInFlight: o.maxInFlight, Timeout: o.timeout}, do)
	}
	if o.warmup > 0 {
		fmt.Fprintf(os.Stderr, "loadgen: warm-up %s at %.0f/s (not recorded)\n", o.warmup, o.rates[0])
		step(o.rates[0], o.warmup)
	}
	var sweep []loadgen.Summary
	for _, rps := range o.rates {
		if ctx.Err() != nil {
			break
		}
		s := loadgen.Summarize(rps, o.window, step(rps, o.window))
		sweep = append(sweep, s)
		fmt.Fprintf(os.Stderr, "loadgen: %.0f/s done (achieved %.1f/s, errors %.2f%%, p99 %s)\n",
			rps, s.AchievedRPS, 100*s.ErrorRate, s.P99)
		if s.ErrorRate > o.stopErrorRate {
			fmt.Fprintf(os.Stderr, "loadgen: error rate above %.2f%% — past the knee, stopping the sweep\n", 100*o.stopErrorRate)
			break
		}
	}
	if err := loadgen.WriteTable(os.Stdout, sweep); err != nil {
		log.Fatal(err)
	}
	if o.jsonOut != "" {
		data, err := json.MarshalIndent(map[string]any{
			"url": base, "workload": o.workload, "seed": o.seed, "window": o.window.String(),
			"max_inflight": o.maxInFlight, "timeout": o.timeout.String(), "steps": sweep,
		}, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(o.jsonOut, data, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
