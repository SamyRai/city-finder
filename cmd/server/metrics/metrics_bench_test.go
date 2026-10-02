package metrics

import (
	"testing"
	"time"
)

// benchPopulated returns a registry shaped like a live server: the six
// documented routes each observed across the statuses the handlers actually
// return, plus the operational gauge and counter the /metrics endpoint
// maintains. Render's cost depends on this state, so the benchmark measures
// a representative scrape, not an empty registry.
func benchPopulated() *Registry {
	reg := NewRegistry()
	paths := []string{"/healthz", "/nearest", "/nearest/batch", "/autocomplete", "/coordinates", "/postalCode"}
	statuses := []int{200, 400, 404, 503}
	for _, p := range paths {
		for _, s := range statuses {
			reg.ObserveRequest(p, s, time.Duration(len(p)*s%997)*time.Microsecond)
		}
	}
	reg.SetGauge("fuzzy_build_state", 2)
	reg.SetGauge("go_goroutines", 42)
	reg.SetGauge("go_heap_alloc_bytes", 4.49e9)
	reg.AddCounter("fuzzy_budget_trips_total", 7)
	return reg
}

// renderSink keeps Render's output alive so the compiler cannot drop the
// call it is supposed to be measuring.
var renderSink string

// BenchmarkRender measures one full Prometheus text exposition of a
// populated registry — the per-scrape serialization cost paid on GET
// /metrics.
func BenchmarkRender(b *testing.B) {
	reg := benchPopulated()
	b.ReportAllocs()
	for b.Loop() {
		renderSink = reg.Render()
	}
}

// BenchmarkObserveRequest measures one request observation: the counter
// increment plus the histogram bucket scan every completed request pays
// inside the metrics middleware.
func BenchmarkObserveRequest(b *testing.B) {
	reg := benchPopulated()
	b.ReportAllocs()
	for b.Loop() {
		reg.ObserveRequest("/nearest", 200, 250*time.Microsecond)
	}
}
