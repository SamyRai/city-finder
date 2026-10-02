package dataLoader

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// silenceLoaderLogs redirects the loader's per-call log output for the duration
// of a benchmark and restores it afterwards.
func silenceLoaderLogs(b *testing.B) {
	b.Helper()
	old := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(old) })
}

// writeSyntheticGeoNames writes n valid GeoNames-format lines into dir and
// returns the file path. Rows mirror allCountries.txt field layout (19
// tab-separated fields, alternatenames populated) so the live parser sees
// realistic work per row. Generation is benchmark setup, not the measured
// operation.
func writeSyntheticGeoNames(b *testing.B, dir string, n int) string {
	b.Helper()
	var sb strings.Builder
	sb.Grow(n * 128)
	for i := 0; i < n; i++ {
		lat := float64(i%1800)/10.0 - 90.0
		lon := float64(i%3600)/10.0 - 180.0
		fmt.Fprintf(&sb, "%d\tSyntheticCity%d\tSyntheticCity%d\tAlt%dA,Alt%dB\t%.4f\t%.4f\tT\tPPL\tC%02d\t\t\t\t\t\t\t0\t\t\t\t2023-01-01\n",
			i, i, i, i, i, lat, lon, i%100)
	}
	path := filepath.Join(dir, "synthetic_allCountries.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		b.Fatalf("write synthetic dataset: %v", err)
	}
	return path
}

// BenchmarkMemLiveLoad_Synthetic100k drives the live parse path
// (LoadGeoNamesCSVWithLimit, limit=0) over a 100,000-row synthetic GeoNames
// file generated once in setup. It measures the full per-row load cost
// (field extraction, float parsing, string/altnames allocations) at a scale
// where the per-row delta from removing the write-only Rect allocation is
// visible in B/op and allocs/op. The file is read through a warm OS page
// cache after the first iteration: this is parse cost, not disk I/O.
func BenchmarkMemLiveLoad_Synthetic100k(b *testing.B) {
	b.ReportAllocs()
	path := writeSyntheticGeoNames(b, b.TempDir(), 100_000)
	silenceLoaderLogs(b)
	for b.Loop() {
		cities, err := LoadGeoNamesCSVWithLimit(path, 0)
		if err != nil {
			b.Fatalf("load failed: %v", err)
		}
		if len(cities) != 100_000 {
			b.Fatalf("expected 100000 cities, got %d", len(cities))
		}
	}
}
