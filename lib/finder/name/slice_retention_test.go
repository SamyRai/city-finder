package name

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
)

// generateUniqueCities returns count cities with unique names and no alternate
// names. Name uniqueness matters for memory measurements: interned strings must
// not collapse the per-city allocations being counted, and nil AltNames keep
// the measurement focused on the SpatialCity backing array itself.
func generateUniqueCities(count int) []city.SpatialCity {
	cities := make([]city.SpatialCity, count)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      fmt.Sprintf("RetentionCity%07d", i),
				Country:   fmt.Sprintf("C%02d", i%50),
				Latitude:  float64(i%9000) / 100,
				Longitude: float64(i%18000) / 100,
			},
		}
	}
	return cities
}

// BenchmarkProcessBatchStreamlined measures the sequential bulk-load path over
// a 100K-city slice with unique names. B/op and allocs/op capture the cost of
// the per-city heap copy that stops the index from pinning the loader slice.
func BenchmarkProcessBatchStreamlined(b *testing.B) {
	cities := generateUniqueCities(100000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f := NewFinderWithCapacity(estimateCapacity(cities))
		f.processBatchStreamlined(cities)
	}
}

// BenchmarkProcessBatchConcurrent measures the concurrent bulk-load path over
// a 100K-city slice with unique names (same metric, worker-pool shape).
func BenchmarkProcessBatchConcurrent(b *testing.B) {
	cities := generateUniqueCities(100000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f := NewFinderWithCapacity(estimateCapacity(cities))
		f.processBatchConcurrent(cities, runtime.NumCPU())
	}
}

// settleGC forces full garbage collection and sweeping so HeapAlloc readings
// reflect exactly the live object graph, not pending sweep work.
func settleGC() {
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
}

// TestBuildIndexDoesNotPinLoaderSlice proves the built index no longer keeps
// the loader's []SpatialCity backing array alive once the loader itself drops
// its reference.
//
// Measurement design notes (both are load-bearing):
//   - The slice must be LIVE at the first snapshot. In Go, a local variable is
//     GC-dead after its last use, and `cities = nil` is a dead store the
//     compiler may remove entirely; without a real use the array can already
//     be collectible at the "before" reading and the test measures nothing.
//     runtime.KeepAlive(cities) placed after the first snapshot is that real
//     use: the slice is reachable for the snapshot, unreachable after.
//   - The index (which we blame for the pin) must stay live for BOTH
//     snapshots via runtime.KeepAlive(finder) at the end, otherwise a
//     collected finder would "free" the array in the broken builds too.
//
// Before the fix (index storing &cities[i].City interior pointers), every
// interior pointer pins the whole backing allocation, freed stays near zero
// and this test fails.
func TestBuildIndexDoesNotPinLoaderSlice(t *testing.T) {
	const n = 100000
	const arrayBytes = n * 80 // SpatialCity = 48 (City) + 8 (Rect ptr) + 24 (AltNames)

	cities := generateUniqueCities(n)

	finder := BuildIndex(cities)
	settleGC()
	var withSlice runtime.MemStats
	runtime.ReadMemStats(&withSlice)
	runtime.KeepAlive(cities) // last use: slice live during the snapshot above

	settleGC()
	var dropped runtime.MemStats
	runtime.ReadMemStats(&dropped)

	freed := int64(withSlice.HeapAlloc) - int64(dropped.HeapAlloc)
	t.Logf("heap with slice+index: %d B; after loader drops slice: %d B; freed: %d B (slice array alone is %d B)",
		withSlice.HeapAlloc, dropped.HeapAlloc, freed, arrayBytes)

	if freed < int64(arrayBytes)*2/3 {
		t.Fatalf("dropping the loader slice freed only %d B of the expected ~%d B backing array; the index still pins the loader slice", freed, arrayBytes)
	}
	runtime.KeepAlive(finder)
}
