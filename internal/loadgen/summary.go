package loadgen

import (
	"slices"
	"time"
)

// Outcome classifies one scheduled request.
type Outcome uint8

const (
	// OK is a completed request with a 2xx status.
	OK Outcome = iota
	// Failed is a completed request with a non-2xx status or a transport
	// error (including the client timeout).
	Failed
	// Dropped is a request that was due but never sent because the
	// in-flight cap was reached. It is a failure of the system under test
	// to keep up, so it counts as an error, never as a skipped sample.
	Dropped
	// NotFound is a completed 404: a correctly served "no such city" answer
	// (e.g. a workload name absent from the loaded dataset). It is served
	// work, so it enters throughput and the latency percentiles, but it is
	// counted separately so a workload/dataset mismatch stays visible.
	NotFound
)

// Classify maps an HTTP status (or a transport error) to an Outcome.
func Classify(status int, err error) Outcome {
	switch {
	case err != nil:
		return Failed
	case status >= 200 && status < 300:
		return OK
	case status == 404:
		return NotFound
	default:
		return Failed
	}
}

// Sample is the record of one scheduled request.
type Sample struct {
	Outcome Outcome
	// Latency is measured from the intended send time to the response (zero
	// for Dropped).
	Latency time.Duration
}

// Summary describes one Step.
type Summary struct {
	OfferedRPS  float64       `json:"offered_rps"`
	Window      time.Duration `json:"window_ns"`
	Scheduled   int           `json:"scheduled"`
	OK          int           `json:"ok"`
	NotFound    int           `json:"not_found"`
	Failed      int           `json:"failed"`
	Dropped     int           `json:"dropped"`
	AchievedRPS float64       `json:"achieved_rps"` // served (OK + NotFound) responses per second of window
	ErrorRate   float64       `json:"error_rate"`   // (failed + dropped) / scheduled
	P50         time.Duration `json:"p50_ns"`
	P90         time.Duration `json:"p90_ns"`
	P95         time.Duration `json:"p95_ns"`
	P99         time.Duration `json:"p99_ns"`
	P999        time.Duration `json:"p999_ns"`
	Max         time.Duration `json:"max_ns"`
}

// Summarize reduces a Step's samples. Latency percentiles are taken over
// served responses (OK and NotFound) only; failures and drops are reported as
// counts and in ErrorRate, so a server that answers fast with errors cannot
// look fast.
func Summarize(offeredRPS float64, window time.Duration, samples []Sample) Summary {
	s := Summary{OfferedRPS: offeredRPS, Window: window, Scheduled: len(samples)}
	latencies := make([]time.Duration, 0, len(samples))
	for _, x := range samples {
		switch x.Outcome {
		case OK:
			s.OK++
			latencies = append(latencies, x.Latency)
		case NotFound:
			s.NotFound++
			latencies = append(latencies, x.Latency)
		case Failed:
			s.Failed++
		case Dropped:
			s.Dropped++
		}
	}
	if window > 0 {
		s.AchievedRPS = float64(s.OK+s.NotFound) / window.Seconds()
	}
	if s.Scheduled > 0 {
		s.ErrorRate = float64(s.Failed+s.Dropped) / float64(s.Scheduled)
	}
	if len(latencies) == 0 {
		return s
	}
	slices.Sort(latencies)
	s.P50 = percentile(latencies, 0.50)
	s.P90 = percentile(latencies, 0.90)
	s.P95 = percentile(latencies, 0.95)
	s.P99 = percentile(latencies, 0.99)
	s.P999 = percentile(latencies, 0.999)
	s.Max = latencies[len(latencies)-1]
	return s
}

// percentile returns the nearest-rank percentile of sorted (ascending).
func percentile(sorted []time.Duration, p float64) time.Duration {
	rank := int(p*float64(len(sorted))+0.999999) - 1 // ceil(p*n) - 1
	rank = max(0, min(rank, len(sorted)-1))
	return sorted[rank]
}
