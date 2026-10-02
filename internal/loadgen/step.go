package loadgen

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Request issues the i-th request of a step and classifies the result (see
// Classify). Implementations must honour ctx (it carries the client timeout
// and the run's cancellation).
type Request func(ctx context.Context, i int) Outcome

// StepConfig is one constant-rate measurement window.
type StepConfig struct {
	RPS         float64       // offered arrival rate; must be > 0
	Window      time.Duration // how long the rate is offered; must be > 0
	MaxInFlight int           // in-flight cap; a due request beyond it is Dropped
	Timeout     time.Duration // per-request deadline (0 = none beyond ctx)
}

// RunStep offers cfg.RPS requests per second for cfg.Window, evenly spaced,
// and returns one Sample per scheduled request (index i = i-th arrival).
//
// Request i is due at start + i/RPS. Its latency is measured from that
// intended time, so when the scheduler (or the machine) falls behind, the
// lag is charged to the request instead of silently shifting the schedule.
// RunStep waits for every in-flight request before returning, so samples are
// complete; cancel ctx to abort early (pending requests then fail).
func RunStep(ctx context.Context, cfg StepConfig, do Request) []Sample {
	n := int(cfg.RPS * cfg.Window.Seconds())
	samples := make([]Sample, n)
	interval := time.Duration(float64(time.Second) / cfg.RPS)

	var (
		wg       sync.WaitGroup
		inFlight atomic.Int64
	)
	start := time.Now()
	for i := 0; i < n; i++ {
		intended := start.Add(time.Duration(i) * interval)
		if d := time.Until(intended); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				for j := i; j < n; j++ {
					samples[j] = Sample{Outcome: Failed}
				}
				wg.Wait()
				return samples
			}
		}
		if cfg.MaxInFlight > 0 && inFlight.Load() >= int64(cfg.MaxInFlight) {
			samples[i] = Sample{Outcome: Dropped}
			continue
		}
		inFlight.Add(1)
		wg.Add(1)
		go func(i int, intended time.Time) {
			defer wg.Done()
			defer inFlight.Add(-1)
			reqCtx := ctx
			if cfg.Timeout > 0 {
				var cancel context.CancelFunc
				reqCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
				defer cancel()
			}
			outcome := do(reqCtx, i)
			// Distinct index per goroutine: no synchronization needed.
			samples[i] = Sample{Outcome: outcome, Latency: time.Since(intended)}
		}(i, intended)
	}
	wg.Wait()
	return samples
}
