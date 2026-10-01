package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_CountersAndHistograms(t *testing.T) {
	r := NewRegistry()
	r.ObserveRequest("/nearest", 200, 500*time.Microsecond)
	r.ObserveRequest("/nearest", 200, 2*time.Second)
	r.ObserveRequest("/nearest", 404, 3*time.Millisecond)
	r.SetGauge("fuzzy_index_built", 1)
	r.AddCounter("fuzzy_budget_trips_total", 2)
	r.AddCounter("fuzzy_budget_trips_total", 1)

	out := r.Render()

	// Counters aggregate per path+status with bounded label cardinality.
	assert.Contains(t, out, `http_requests_total{path="/nearest",status="200"} 2`)
	assert.Contains(t, out, `http_requests_total{path="/nearest",status="404"} 1`)

	// Histograms are cumulative: observations of 500 µs, 3 ms and 2 s give
	// 2 under le="1" and all 3 under le="5"/le="10".
	assert.Contains(t, out, `http_request_duration_seconds_bucket{path="/nearest",le="1"} 2`)
	assert.Contains(t, out, `http_request_duration_seconds_bucket{path="/nearest",le="5"} 3`)
	assert.Contains(t, out, `http_request_duration_seconds_bucket{path="/nearest",le="10"} 3`)
	assert.Contains(t, out, `http_request_duration_seconds_bucket{path="/nearest",le="+Inf"} 3`)
	assert.Contains(t, out, `http_request_duration_seconds_count{path="/nearest"} 3`)

	// Bare counters render with counter semantics, gauges with gauge
	// semantics — a scraper relies on the TYPE to apply rate().
	assert.Contains(t, out, "# TYPE fuzzy_budget_trips_total counter\nfuzzy_budget_trips_total 3")
	assert.Contains(t, out, "# TYPE fuzzy_index_built gauge\nfuzzy_index_built 1")

	// Rendering is deterministic: two renders over unchanged state match.
	assert.Equal(t, out, r.Render())
}

func TestRegistry_ConcurrentObserve(t *testing.T) {
	r := NewRegistry()
	done := make(chan struct{})
	for w := 0; w < 4; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 250; i++ {
				r.ObserveRequest("/nearest", 200, time.Millisecond)
				r.AddCounter("fuzzy_budget_trips_total", 1)
			}
		}()
	}
	for w := 0; w < 4; w++ {
		<-done
	}
	out := r.Render()
	require.Contains(t, out, `http_requests_total{path="/nearest",status="200"} 1000`)
	require.Contains(t, out, "fuzzy_budget_trips_total 1000")
	assert.False(t, strings.Contains(out, "NaN"), "no NaN may leak into the exposition")
}
