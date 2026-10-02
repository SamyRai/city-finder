package loadgen

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSummarizeAccountsForEveryOutcome(t *testing.T) {
	samples := []Sample{
		{Outcome: OK, Latency: 3 * time.Millisecond},
		{Outcome: OK, Latency: 1 * time.Millisecond},
		{Outcome: Failed, Latency: 1 * time.Microsecond}, // fast error must not improve latency
		{Outcome: Dropped},
		{Outcome: OK, Latency: 2 * time.Millisecond},
	}
	s := Summarize(5, time.Second, samples)

	assert.Equal(t, 5, s.Scheduled)
	assert.Equal(t, 3, s.OK)
	assert.Equal(t, 1, s.Failed)
	assert.Equal(t, 1, s.Dropped)
	assert.InDelta(t, 3.0, s.AchievedRPS, 1e-9)
	assert.InDelta(t, 0.4, s.ErrorRate, 1e-9)
	assert.Equal(t, 2*time.Millisecond, s.P50, "percentiles cover successful responses only")
	assert.Equal(t, 3*time.Millisecond, s.Max)
}

func TestPercentileNearestRank(t *testing.T) {
	sorted := make([]time.Duration, 1000)
	for i := range sorted {
		sorted[i] = time.Duration(i+1) * time.Microsecond
	}
	assert.Equal(t, 500*time.Microsecond, percentile(sorted, 0.50))
	assert.Equal(t, 990*time.Microsecond, percentile(sorted, 0.99))
	assert.Equal(t, 999*time.Microsecond, percentile(sorted, 0.999))
	assert.Equal(t, time.Microsecond, percentile(sorted[:1], 0.99))
}

func TestSummarizeEmpty(t *testing.T) {
	s := Summarize(10, time.Second, nil)
	assert.Zero(t, s.Scheduled)
	assert.Zero(t, s.P99)
	assert.Zero(t, s.ErrorRate)
}

func TestNotFoundIsServedButCountedSeparately(t *testing.T) {
	s := Summarize(2, time.Second, []Sample{
		{Outcome: OK, Latency: time.Millisecond},
		{Outcome: NotFound, Latency: 3 * time.Millisecond},
	})
	assert.Equal(t, 1, s.NotFound)
	assert.Zero(t, s.ErrorRate, "a served 404 is not a server failure")
	assert.InDelta(t, 2.0, s.AchievedRPS, 1e-9)
	assert.Equal(t, 3*time.Millisecond, s.Max)
}

func TestClassify(t *testing.T) {
	assert.Equal(t, OK, Classify(200, nil))
	assert.Equal(t, NotFound, Classify(404, nil))
	assert.Equal(t, Failed, Classify(400, nil), "a 400 means the workload is broken")
	assert.Equal(t, Failed, Classify(503, nil), "population gate saturation is a failure")
	assert.Equal(t, Failed, Classify(200, assert.AnError))
}
