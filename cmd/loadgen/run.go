package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/SamyRai/cityFinder/internal/loadgen"
)

// run executes the sweep described by o: progress goes to stderr, the result
// table to stdout, and the optional JSON file to o.jsonOut.
func run(ctx context.Context, o options, stdout, stderr io.Writer) error {
	// Enough paths that a step does not cycle a tiny hot set.
	wl, err := loadgen.NewWorkload(o.workload, 1<<16, o.seed)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{
		MaxIdleConns:        o.maxInFlight,
		MaxIdleConnsPerHost: o.maxInFlight,
		IdleConnTimeout:     90 * time.Second,
	}}
	defer client.CloseIdleConnections()
	base := strings.TrimRight(o.baseURL, "/")

	fmt.Fprintf(stderr, "loadgen: %s workload=%s window=%s rates=%v max-inflight=%d client GOMAXPROCS=%d\n",
		base, o.workload, o.window, o.rates, o.maxInFlight, runtime.GOMAXPROCS(0))
	fmt.Fprintln(stderr, "loadgen: the client shares the machine with nothing else, or its own CPU limits the result")

	sweep := runSweep(ctx, o, requester(client, base, wl), stderr)
	if err := loadgen.WriteTable(stdout, sweep); err != nil {
		return err
	}
	if o.jsonOut == "" {
		return nil
	}
	return writeJSON(o.jsonOut, base, o, sweep)
}

// requester returns the Request that GETs the workload's i-th path.
func requester(client *http.Client, base string, wl loadgen.Workload) loadgen.Request {
	return func(ctx context.Context, i int) loadgen.Outcome {
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
}

// runSweep runs the optional warm-up and then one step per rate, stopping
// early when ctx is cancelled or a step's error rate exceeds the threshold.
func runSweep(ctx context.Context, o options, do loadgen.Request, stderr io.Writer) []loadgen.Summary {
	step := func(rps float64, window time.Duration) []loadgen.Sample {
		return loadgen.RunStep(ctx, loadgen.StepConfig{RPS: rps, Window: window, MaxInFlight: o.maxInFlight, Timeout: o.timeout}, do)
	}
	if o.warmup > 0 {
		fmt.Fprintf(stderr, "loadgen: warm-up %s at %.0f/s (not recorded)\n", o.warmup, o.rates[0])
		step(o.rates[0], o.warmup)
	}
	var sweep []loadgen.Summary
	for _, rps := range o.rates {
		if ctx.Err() != nil {
			break
		}
		s := loadgen.Summarize(rps, o.window, step(rps, o.window))
		sweep = append(sweep, s)
		fmt.Fprintf(stderr, "loadgen: %.0f/s done (achieved %.1f/s, errors %.2f%%, p99 %s)\n",
			rps, s.AchievedRPS, 100*s.ErrorRate, s.P99)
		if s.ErrorRate > o.stopErrorRate {
			fmt.Fprintf(stderr, "loadgen: error rate above %.2f%% — past the knee, stopping the sweep\n", 100*o.stopErrorRate)
			break
		}
	}
	return sweep
}

func writeJSON(path, base string, o options, sweep []loadgen.Summary) error {
	data, err := json.MarshalIndent(map[string]any{
		"url": base, "workload": o.workload, "seed": o.seed, "window": o.window.String(),
		"max_inflight": o.maxInFlight, "timeout": o.timeout.String(), "steps": sweep,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
