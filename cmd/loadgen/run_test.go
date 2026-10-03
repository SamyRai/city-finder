package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/internal/loadgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testOptions(url string) options {
	return options{
		baseURL: url, rates: []float64{50, 100}, window: 200 * time.Millisecond,
		workload: "nearest", seed: 7, maxInFlight: 64, timeout: time.Second, stopErrorRate: 0.5,
	}
}

// requesterFor builds the production Request for o against its base URL.
func requesterFor(t *testing.T, o options) loadgen.Request {
	t.Helper()
	wl, err := loadgen.NewWorkload(o.workload, 1024, o.seed)
	require.NoError(t, err)
	return requester(&http.Client{}, o.baseURL, wl)
}

func TestRun_AgainstHTTPTestServer(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	o := testOptions(srv.URL + "/")
	o.jsonOut = filepath.Join(t.TempDir(), "sweep.json")

	var stdout, stderr bytes.Buffer
	require.NoError(t, run(context.Background(), o, &stdout, &stderr))

	assert.Contains(t, stdout.String(), "offered/s")
	assert.Contains(t, stderr.String(), "50/s done")
	assert.Contains(t, stderr.String(), "100/s done")
	assert.EqualValues(t, 10+20, hits.Load())

	raw, err := os.ReadFile(o.jsonOut)
	require.NoError(t, err)
	var doc struct {
		URL   string           `json:"url"`
		Seed  int64            `json:"seed"`
		Steps []map[string]any `json:"steps"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.Equal(t, srv.URL, doc.URL)
	assert.EqualValues(t, 7, doc.Seed)
	require.Len(t, doc.Steps, 2)
	assert.EqualValues(t, 0, doc.Steps[0]["failed"])
}

func TestRunSweep_StopsPastTheKnee(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	o := testOptions(srv.URL)
	o.rates = []float64{50, 100, 200}
	o.stopErrorRate = 0.1

	var stderr bytes.Buffer
	sweep := runSweep(context.Background(), o, requesterFor(t, o), &stderr)
	require.Len(t, sweep, 1, "the first step already exceeds the threshold")
	assert.Equal(t, 1.0, sweep[0].ErrorRate)
	assert.Contains(t, stderr.String(), "past the knee")
}

func TestRunSweep_CountsNotFoundSeparately(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	o := testOptions(srv.URL)
	o.rates = []float64{50}

	sweep := runSweep(context.Background(), o, requesterFor(t, o), &bytes.Buffer{})
	require.Len(t, sweep, 1)
	assert.Equal(t, 10, sweep[0].NotFound)
	assert.Zero(t, sweep[0].ErrorRate)
}

func TestRunSweep_WarmupIsNotRecorded(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	o := testOptions(srv.URL)
	o.rates = []float64{50}
	o.warmup = 200 * time.Millisecond

	var stderr bytes.Buffer
	sweep := runSweep(context.Background(), o, requesterFor(t, o), &stderr)
	require.Len(t, sweep, 1)
	assert.Equal(t, 10, sweep[0].Scheduled)
	assert.EqualValues(t, 20, hits.Load(), "warm-up requests reach the server")
	assert.Contains(t, stderr.String(), "warm-up")
}

func TestRunSweep_CancelledContextStopsCleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	o := testOptions(srv.URL)
	o.rates = []float64{50, 100, 200}
	o.window = 10 * time.Second // a cancel must not wait this out

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	sweep := runSweep(ctx, o, requesterFor(t, o), &bytes.Buffer{})
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.LessOrEqual(t, len(sweep), 1, "no step starts after the cancel")
}

func TestRun_UnreachableServerCountsErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	o := testOptions(url)
	o.rates = []float64{50}

	var stdout bytes.Buffer
	require.NoError(t, run(context.Background(), o, &stdout, &bytes.Buffer{}))
	assert.Contains(t, stdout.String(), "100.00", "all requests failed")
}

func TestRun_JSONWriteFailureIsReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	o := testOptions(srv.URL)
	o.rates = []float64{50}
	o.jsonOut = filepath.Join(t.TempDir(), "missing-dir", "out.json")
	assert.Error(t, run(context.Background(), o, &bytes.Buffer{}, &bytes.Buffer{}))
}
