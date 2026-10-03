package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{5 * 1024 * 1024, "5.00 MB"},
		{3 << 30, "3.00 GB"},
		{2 << 40, "2.00 TB"},
	} {
		assert.Equal(t, tc.want, formatBytes(tc.in), "%d", tc.in)
	}
}

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0.00 ms"},
		{1500 * time.Microsecond, "1.50 ms"},
		{time.Second, "1.00 s"},
		{59*time.Second + 500*time.Millisecond, "59.50 s"},
		{time.Minute, "1m 0s"},
		{125 * time.Second, "2m 5s"},
	} {
		assert.Equal(t, tc.want, formatDuration(tc.in), "%v", tc.in)
	}
}

func TestFormatNumber(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0K"},
		{12345, "12.3K"},
		{1_000_000, "1.0M"},
		{2_500_000, "2.5M"},
		{3_000_000_000, "3.0B"},
	} {
		assert.Equal(t, tc.want, formatNumber(tc.in), "%d", tc.in)
	}
}

func TestPrintResult(t *testing.T) {
	var buf bytes.Buffer
	printResult(&buf, OperationResult{
		Name:           "1. Step",
		Duration:       2 * time.Second,
		ItemsProcessed: 2000,
		Throughput:     1000,
		MemoryDelta:    2048,
		MemoryAfter:    MemoryStats{HeapAlloc: 1024},
		GCsDelta:       2,
	})
	out := buf.String()
	assert.Contains(t, out, "\n1. Step\n=======\n")
	assert.Contains(t, out, "Duration:        2.00 s")
	assert.Contains(t, out, "Items Processed: 2.0K")
	assert.Contains(t, out, "Throughput:      1.0K/sec")
	assert.Contains(t, out, "Memory Delta:    2.00 KB")
	assert.Contains(t, out, "Memory After:    1.00 KB")
	assert.Contains(t, out, "GC Cycles:       2")
	assert.Contains(t, out, "GC Time:         ~1.00 s (estimated)")

	buf.Reset()
	printResult(&buf, OperationResult{Name: "x"})
	assert.NotContains(t, buf.String(), "GC Time", "no GC cycles, no GC time line")
}

func TestPrintBannerAndSummary(t *testing.T) {
	var buf bytes.Buffer
	printBanner(&buf, "TITLE")
	assert.Equal(t, "======================================================================\n  TITLE\n======================================================================\n", buf.String())

	buf.Reset()
	printSummary(&buf, summary{mode: "test", cities: 1500, postalCodes: 3, total: time.Second})
	out := buf.String()
	assert.Contains(t, out, "Mode:               test")
	assert.Contains(t, out, "Total Cities:        1.5K")
	assert.NotContains(t, out, "Postal Index:", "no postal throughput, no postal line")

	buf.Reset()
	printSummary(&buf, summary{postal: OperationResult{Throughput: 2000}})
	assert.Contains(t, buf.String(), "Postal Index:     2.0K entries/sec")
}

func TestPrintUsageListsModes(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	assert.Contains(t, buf.String(), "test - ")
	assert.Contains(t, buf.String(), "prod - ")
}

func TestMeasureOperationReturnsOperationError(t *testing.T) {
	res, err := measureOperation("op", 10, func() error { return assert.AnError })
	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, "op", res.Name)
	assert.Equal(t, 10, res.ItemsProcessed)
}
