package loadgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunStepChargesStallsToLatency checks the open-model property: the
// server freezes for 300 ms in the middle of a 1 s, 200/s step. A closed-loop
// client would send ONE request into the freeze and record one slow sample;
// an open-model client keeps issuing arrivals during it (~60 of them), and
// each records the wait.
func TestRunStepChargesStallsToLatency(t *testing.T) {
	const stall = 300 * time.Millisecond
	start := time.Now()
	stallFrom, stallUntil := start.Add(300*time.Millisecond), start.Add(300*time.Millisecond+stall)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if now := time.Now(); now.After(stallFrom) && now.Before(stallUntil) {
			time.Sleep(time.Until(stallUntil))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	do := func(ctx context.Context, _ int) Outcome {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			return Classify(0, err)
		}
		_ = resp.Body.Close()
		return Classify(resp.StatusCode, nil)
	}
	samples := RunStep(context.Background(), StepConfig{RPS: 200, Window: time.Second, MaxInFlight: 1000, Timeout: 5 * time.Second}, do)
	require.Len(t, samples, 200)

	slow := 0
	for _, s := range samples {
		require.Equal(t, OK, s.Outcome)
		if s.Latency > 50*time.Millisecond {
			slow++
		}
	}
	// ~60 arrivals fall inside the freeze; most wait > 50 ms. Allow slack for
	// scheduler jitter on shared CI runners, but far above the 1 a closed
	// loop would record.
	assert.GreaterOrEqual(t, slow, 30, "requests arriving during the stall must carry its wait")
}

func TestRunStepSchedulesRateTimesWindow(t *testing.T) {
	var calls atomic.Int64
	samples := RunStep(context.Background(), StepConfig{RPS: 400, Window: 250 * time.Millisecond},
		func(context.Context, int) Outcome { calls.Add(1); return OK })
	assert.Len(t, samples, 100)
	assert.EqualValues(t, 100, calls.Load())
}

func TestRunStepDropsBeyondInFlightCap(t *testing.T) {
	release := make(chan struct{})
	samples := make(chan []Sample, 1)
	go func() {
		samples <- RunStep(context.Background(), StepConfig{RPS: 1000, Window: 50 * time.Millisecond, MaxInFlight: 5},
			func(context.Context, int) Outcome { <-release; return OK })
	}()
	time.Sleep(150 * time.Millisecond) // every arrival is due by now
	close(release)
	got := <-samples

	s := Summarize(1000, 50*time.Millisecond, got)
	assert.Equal(t, 5, s.OK, "only MaxInFlight requests can be outstanding")
	assert.Equal(t, 45, s.Dropped, "the rest are drops, counted as errors")
}

func TestRunStepTimeoutCountsAsFailure(t *testing.T) {
	samples := RunStep(context.Background(), StepConfig{RPS: 100, Window: 50 * time.Millisecond, Timeout: 10 * time.Millisecond},
		func(ctx context.Context, _ int) Outcome { <-ctx.Done(); return Failed })
	s := Summarize(100, 50*time.Millisecond, samples)
	assert.Equal(t, s.Scheduled, s.Failed)
}

func TestRunStepCancelFailsRemaining(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	samples := RunStep(ctx, StepConfig{RPS: 100, Window: time.Second}, func(context.Context, int) Outcome { return OK })
	s := Summarize(100, time.Second, samples)
	assert.Len(t, samples, 100)
	assert.Greater(t, s.Failed, 50, "arrivals after cancellation are failures, not silently missing")
}
