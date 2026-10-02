package name

import (
	"runtime"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
)

// The lookup benchmarks below use distinct-name fixtures (benchDiverseCities,
// shared with prefix_bench_test.go and exact_tail_bench_test.go). The original
// version of this file built every benchmark over 100k copies of ONE name,
// which collapsed the name tables and the n-gram index to a handful of keys:
// exact lookups measured a hot single-bucket hit and fuzzy lookups a
// four-key index, neither representative of the production distribution.
// The full-sweep tail measurement for exact lookups lives in
// BenchmarkCityByNameExactTail (1M keys, per-query percentiles).

// BenchmarkBuildIndex benchmarks the index building process over distinct
// names, so the per-country name-table sort and CSR arrays pay their real
// cost instead of sorting one repeated key.
func BenchmarkBuildIndex(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1K", 1000},
		{"10K", 10000},
		{"100K", 100000},
		{"1M", 1000000},
	}

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			cities := benchDiverseCities(size.size)
			b.ReportAllocs()
			for b.Loop() {
				_ = BuildIndex(cities)
			}
		})
	}
}

// BenchmarkBuildIndexConcurrent benchmarks concurrent index building
func BenchmarkBuildIndexConcurrent(b *testing.B) {
	cities := benchDiverseCities(100000)
	b.ReportAllocs()
	for b.Loop() {
		_ = BuildIndex(cities)
	}
}

// BenchmarkCityByName benchmarks distinct-key exact hits at 100k keys,
// sweeping a fixed coprime stride so consecutive iterations touch different
// table regions (a small hot key set would hide cold bucket and hash costs).
// For the full 1M-key sweep with percentile metrics see
// BenchmarkCityByNameExactTail.
func BenchmarkCityByName(b *testing.B) {
	const keyCount = 100_000
	finder := buildDiverseIndex(b, keyCount)
	cities := benchDiverseCities(keyCount) // same generator: name/country pairs

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		c := cities[(i*97)%keyCount]
		if finder.CityByName(c.Name, c.Country) == nil {
			b.Fatalf("query %q (%s) must hit", c.Name, c.Country)
		}
		i++
	}
}

// BenchmarkCityByNameFuzzy benchmarks distance-1 typo lookups over 100k
// distinct names with the n-gram index prebuilt (WarmFuzzy + wait, untimed) —
// matching production, where the build never runs inside a request. Every
// query must resolve; a miss means the fixture or budget regressed.
func BenchmarkCityByNameFuzzy(b *testing.B) {
	const keyCount = 100_000
	finder := buildDiverseIndex(b, keyCount)
	finder.WarmFuzzy()
	waitFuzzyBuilt(b, finder)
	queries := benchFuzzyQueries(keyCount)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		q := queries[i%len(queries)]
		if finder.CityByName(q.name, q.country) == nil {
			b.Fatalf("distance-1 typo %q (%s) must resolve", q.name, q.country)
		}
		i++
	}
}

// BenchmarkAddCity benchmarks adding a single city through the overflow
// path (direct table writes are what BuildIndex uses internally).
func BenchmarkAddCity(b *testing.B) {
	finder := NewNameFinder()
	city := city.SpatialCity{
		City: city.City{
			Name:    "Test City",
			Country: "TC",
		},
		AltNames: []string{"Alt1", "Alt2"},
	}

	b.ReportAllocs()
	for b.Loop() {
		finder.AddCity(city)
	}
}

// BenchmarkSerializeIndex benchmarks index serialization
func BenchmarkSerializeIndex(b *testing.B) {
	cities := benchDiverseCities(10000)
	finder := BuildIndex(cities)

	b.ReportAllocs()
	for b.Loop() {
		// Use a temporary file for each iteration
		tmpfile := b.TempDir() + "/test_index.gob"
		_ = finder.SerializeIndex(tmpfile)
	}
}

// BenchmarkDeserializeIndex benchmarks index deserialization
func BenchmarkDeserializeIndex(b *testing.B) {
	cities := benchDiverseCities(10000)
	finder := BuildIndex(cities)
	tmpfile := b.TempDir() + "/test_index.gob"
	_ = finder.SerializeIndex(tmpfile)

	b.ReportAllocs()
	for b.Loop() {
		_, _ = DeserializeIndex(tmpfile)
	}
}

// BenchmarkMemoryUsage measures memory usage for different index sizes
func BenchmarkMemoryUsage(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"10K", 10000},
		{"100K", 100000},
		{"1M", 1000000},
	}

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			var m1, m2 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m1)

			cities := benchDiverseCities(size.size)
			finder := BuildIndex(cities)

			runtime.GC()
			runtime.ReadMemStats(&m2)

			b.ReportMetric(float64(m2.Alloc-m1.Alloc)/1024/1024, "MB/op")
			b.ReportMetric(float64(m2.TotalAlloc-m1.TotalAlloc)/1024/1024, "MB_total/op")

			// KeepAlive, not `_ = finder`: a blank assignment does not extend
			// the index's lifetime, so without this the second GC can reclaim
			// it before m2 and report a near-zero retained heap.
			runtime.KeepAlive(finder)
		})
	}
}
