package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
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

// parseFlags parses and validates the command line; it does no I/O beyond
// flag's usage output, so every rejection is testable.
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
	fs.Float64Var(&o.stopErrorRate, "stop-error-rate", 0.05, "stop the sweep after a step whose error rate exceeds this, in [0,1] (past the knee)")
	fs.StringVar(&o.jsonOut, "json", "", "also write the sweep as JSON to this file")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	var err error
	if o.rates, err = parseRates(rates); err != nil {
		return o, err
	}
	return o, o.validate()
}

func parseRates(s string) ([]float64, error) {
	var rates []float64
	for _, f := range strings.Split(s, ",") {
		r, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil || r <= 0 || math.IsInf(r, 0) || math.IsNaN(r) {
			return nil, fmt.Errorf("invalid rate %q", f)
		}
		if n := len(rates); n > 0 && r <= rates[n-1] {
			return nil, fmt.Errorf("-rates must be strictly ascending: %v follows %v", r, rates[n-1])
		}
		rates = append(rates, r)
	}
	return rates, nil
}

// validate rejects values that would hang the run or measure nothing.
func (o options) validate() error {
	u, err := url.Parse(o.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid -url %q: want http(s)://host[:port]", o.baseURL)
	}
	switch {
	case !loadgen.HasWorkload(o.workload):
		return fmt.Errorf("unknown -workload %q", o.workload)
	case o.window <= 0:
		return errors.New("-window must be > 0")
	case o.warmup < 0:
		return errors.New("-warmup must be >= 0")
	case o.maxInFlight <= 0:
		return errors.New("-max-inflight must be > 0")
	case o.timeout <= 0:
		return errors.New("-timeout must be > 0")
	case math.IsNaN(o.stopErrorRate) || o.stopErrorRate < 0 || o.stopErrorRate > 1:
		return errors.New("-stop-error-rate must be within [0,1]")
	}
	return nil
}
