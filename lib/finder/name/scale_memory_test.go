package name

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// TestScaleBuildMemory is the env-gated scale memory measurement for the
// exact-lookup structure: it loads the real GeoNames dump, builds the name
// index, settles GC, and reports the runtime.MemStats numbers the memory
// budget decisions are made against.
//
// Skipped unless CF_SCALE_DATASET points at the allCountries dump (config
// all_cities_file, e.g. datasets/allCountries_dump.txt — best given as an
// absolute path, the test binary's cwd is this package directory). The
// optional CF_SCALE_LIMIT bounds the rows loaded, for smoke-testing the
// harness itself; unset (or <= 0) means the whole file.
//
//	CF_SCALE_DATASET=$PWD/datasets/allCountries_dump.txt \
//	  go test ./lib/finder/name/ -run TestScaleBuildMemory -v -timeout 2h
//
// The run takes minutes and tens of GB of RSS; run measurements sequentially.
func TestScaleBuildMemory(t *testing.T) {
	dump := os.Getenv("CF_SCALE_DATASET")
	if dump == "" {
		t.Skip("CF_SCALE_DATASET not set; scale memory measurement is opt-in (needs the GeoNames dump)")
	}

	loadStart := time.Now()
	var (
		cities []city.SpatialCity
		err    error
	)
	if limit := atoiDefault(os.Getenv("CF_SCALE_LIMIT")); limit > 0 {
		cities, err = dataLoader.LoadGeoNamesCSVWithLimit(dump, limit)
	} else {
		cities, err = dataLoader.LoadGeoNamesCSV(dump)
	}
	if err != nil {
		t.Fatalf("loading %s: %v", dump, err)
	}

	runtime.GC()
	runtime.GC()
	var afterLoad runtime.MemStats
	runtime.ReadMemStats(&afterLoad)
	t.Logf("RESULT loader: cities=%d elapsed=%v HeapAlloc=%d HeapSys=%d TotalAlloc=%d",
		len(cities), time.Since(loadStart).Round(time.Millisecond),
		afterLoad.HeapAlloc, afterLoad.HeapSys, afterLoad.TotalAlloc)

	buildStart := time.Now()
	finder := BuildIndex(cities)
	buildElapsed := time.Since(buildStart)

	runtime.GC()
	runtime.GC()
	var afterBuild runtime.MemStats
	runtime.ReadMemStats(&afterBuild)
	t.Logf("RESULT build: cities=%d elapsed=%v HeapAlloc=%d HeapSys=%d TotalAlloc=%d",
		len(cities), buildElapsed.Round(time.Millisecond),
		afterBuild.HeapAlloc, afterBuild.HeapSys, afterBuild.TotalAlloc)

	runtime.KeepAlive(finder)
}

// atoiDefault parses a non-negative int with a zero default (never fails).
func atoiDefault(s string) int {
	var v int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		v = v*10 + int(r-'0')
	}
	return v
}
