// Package metrics is a dependency-free Prometheus text-exposition registry.
//
// The server exposes exactly what operators need to watch the documented
// worst cases (population-rank scan tail, fuzzy budget trips, fuzzy build
// state) without pulling in a client library: counters, gauges and one
// histogram family, rendered in the Prometheus text format on GET /metrics.
// Label cardinality is bounded by design — request metrics are keyed by
// route pattern and status code, never by user input.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// durationBuckets are the histogram boundaries for request latency, chosen
// around the measured service profile: exact lookups land in the 1–10 µs
// band, fuzzy in the 1–100 ms band, and the population-rank tail in the
// 0.5–10 s band.
var durationBuckets = []float64{
	0.00001, 0.0001, 0.001, 0.01, 0.05, 0.1, 0.5, 1, 5, 10,
}

type counterKey struct {
	path   string
	status int
}

type registry struct {
	mu sync.Mutex

	requests      map[counterKey]int64
	durations     map[string]*histogram
	durationOrder []string // first-seen order for stable rendering

	gauges       map[string]float64
	gaugeOrder   []string
	counters     map[string]int64
	counterOrder []string
}

type histogram struct {
	counts []int64
	sum    float64
	total  int64
}

// Registry is safe for concurrent use.
type Registry struct{ reg *registry }

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{reg: &registry{
		requests:  make(map[counterKey]int64),
		durations: make(map[string]*histogram),
		gauges:    make(map[string]float64),
		counters:  make(map[string]int64),
	}}
}

// ObserveRequest records one completed request: route pattern, response
// status code, and handler latency.
func (r *Registry) ObserveRequest(path string, status int, d time.Duration) {
	r.reg.mu.Lock()
	defer r.reg.mu.Unlock()
	r.reg.requests[counterKey{path, status}]++
	h, ok := r.reg.durations[path]
	if !ok {
		h = &histogram{counts: make([]int64, len(durationBuckets))}
		r.reg.durations[path] = h
		r.reg.durationOrder = append(r.reg.durationOrder, path)
	}
	sec := d.Seconds()
	h.sum += sec
	h.total++
	for i, upper := range durationBuckets {
		if sec <= upper {
			h.counts[i]++
		}
	}
}

// SetGauge sets a named gauge value. The name must be a valid metric name
// (caller's responsibility); gauges are for operational state such as the
// fuzzy build phase.
func (r *Registry) SetGauge(name string, value float64) {
	r.reg.mu.Lock()
	defer r.reg.mu.Unlock()
	if _, ok := r.reg.gauges[name]; !ok {
		r.reg.gaugeOrder = append(r.reg.gaugeOrder, name)
	}
	r.reg.gauges[name] = value
}

// AddCounter accumulates a bare counter (no labels), used for state events
// surfaced by the library such as fuzzy budget trips. The first Add defines
// the metric; it renders with counter semantics (monotonic).
func (r *Registry) AddCounter(name string, delta int64) {
	r.reg.mu.Lock()
	defer r.reg.mu.Unlock()
	if _, ok := r.reg.counters[name]; !ok {
		r.reg.counterOrder = append(r.reg.counterOrder, name)
	}
	r.reg.counters[name] += delta
}

// Render produces the Prometheus text exposition (format version 0.0.4).
// Metric families are emitted in a deterministic order (counters sorted by
// path then status, histograms and gauges in first-seen order) so repeated
// scrapes diff cleanly.
func (r *Registry) Render() string {
	r.reg.mu.Lock()
	defer r.reg.mu.Unlock()

	var b strings.Builder

	b.WriteString("# HELP http_requests_total Completed HTTP requests.\n")
	b.WriteString("# TYPE http_requests_total counter\n")
	keys := make([]counterKey, 0, len(r.reg.requests))
	for k := range r.reg.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].path != keys[j].path {
			return keys[i].path < keys[j].path
		}
		return keys[i].status < keys[j].status
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "http_requests_total{path=%q,status=\"%d\"} %d\n", k.path, k.status, r.reg.requests[k])
	}

	b.WriteString("# HELP http_request_duration_seconds Handler latency by route.\n")
	b.WriteString("# TYPE http_request_duration_seconds histogram\n")
	for _, path := range r.reg.durationOrder {
		h := r.reg.durations[path]
		for i, upper := range durationBuckets {
			// counts[i] already holds the cumulative observations <= upper.
			fmt.Fprintf(&b, "http_request_duration_seconds_bucket{path=%q,le=\"%g\"} %d\n", path, upper, h.counts[i])
		}
		fmt.Fprintf(&b, "http_request_duration_seconds_bucket{path=%q,le=\"+Inf\"} %d\n", path, h.total)
		fmt.Fprintf(&b, "http_request_duration_seconds_sum{path=%q} %g\n", path, h.sum)
		fmt.Fprintf(&b, "http_request_duration_seconds_count{path=%q} %d\n", path, h.total)
	}

	for _, name := range r.reg.gaugeOrder {
		fmt.Fprintf(&b, "# TYPE %s gauge\n%s %g\n", name, name, r.reg.gauges[name])
	}

	for _, name := range r.reg.counterOrder {
		fmt.Fprintf(&b, "# TYPE %s counter\n%s %d\n", name, name, r.reg.counters[name])
	}

	return b.String()
}
