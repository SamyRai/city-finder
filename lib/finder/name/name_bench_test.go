package name

import (
	"fmt"
	"path/filepath"
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
//
// Iteration-state rule for every benchmark in this package: iteration N must
// inherit exactly the state the benchmark claims to measure from iteration
// N-1. Where an operation mutates the finder (AddCity, the fuzzy cache), the
// benchmark either resets that state off the clock or measures a documented
// steady state — see the per-benchmark comments.

// BenchmarkBuildIndex benchmarks the index building process over distinct
// names, so the per-country name-table sort and CSR arrays pay their real
// cost instead of sorting one repeated key. BuildIndex does not mutate its
// input, so every iteration builds from identical data. The measured op
// includes BuildIndex's own log lines and its trailing runtime.GC() — both
// are part of what a caller pays.
func BenchmarkBuildIndex(b *testing.B) {
	for _, size := range []struct {
		name string
		size int
	}{
		{"1K", 1000},
		{"10K", 10000},
		{"100K", 100000},
		{"1M", 1000000},
	} {
		b.Run(size.name, func(b *testing.B) {
			cities := benchDiverseCities(size.size)
			silenceBuildLogs(b)
			b.ReportAllocs()
			for b.Loop() {
				if BuildIndex(cities) == nil {
					b.Fatal("BuildIndex returned nil")
				}
			}
		})
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

// BenchmarkCityByNameFuzzy benchmarks distance-1 typo lookups through the
// public CityByName path over 100k distinct names, with the n-gram index
// prebuilt (WarmFuzzy + wait, untimed) — matching production, where the build
// never runs inside a request. Every query must resolve; a miss means the
// fixture or budget regressed.
//
// The fuzzy result cache makes "a typo lookup" two different workloads, so
// they are measured separately rather than averaged into one number:
//
//   - cache-churn: every query is distinct and the cache is pre-filled to its
//     cap off the clock, so each timed iteration pays the full search plus a
//     cache insert that must evict. This is the steady state of a long tail
//     of distinct typos (the common production shape for user input).
//   - cache-hit: a small query set that fits in the cache, pre-warmed off the
//     clock, so each timed iteration is a cache hit. This is the repeated-
//     typo steady state.
func BenchmarkCityByNameFuzzy(b *testing.B) {
	const keyCount = 100_000
	finder := buildDiverseIndex(b, keyCount)
	finder.WarmFuzzy()
	waitFuzzyBuilt(b, finder)
	queries := benchFuzzyQueries(keyCount)

	b.Run("cache-churn", func(b *testing.B) {
		// Fill the cache to its cap with the first maxFuzzyCacheEntries
		// queries; the timed loop starts after them, and the sweep is
		// 10x longer than the cache, so a timed query is never a hit.
		for _, q := range queries[:maxFuzzyCacheEntries] {
			finder.CityByName(q.name, q.country)
		}
		b.ReportAllocs()
		i := maxFuzzyCacheEntries
		for b.Loop() {
			q := queries[i%len(queries)]
			if finder.CityByName(q.name, q.country) == nil {
				b.Fatalf("distance-1 typo %q (%s) must resolve", q.name, q.country)
			}
			i++
		}
	})

	b.Run("cache-hit", func(b *testing.B) {
		hot := queries[:1024]
		for _, q := range hot {
			finder.CityByName(q.name, q.country)
		}
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			q := hot[i%len(hot)]
			if finder.CityByName(q.name, q.country) == nil {
				b.Fatalf("distance-1 typo %q (%s) must resolve", q.name, q.country)
			}
			i++
		}
	})
}

// BenchmarkAddCity measures one post-build AddCity of a NEW distinct city
// into a finder whose overflow holds at most addCityBatch entries. A single
// repeated city would measure something else: its overflow id list, the
// distinct-city table and the fuzzy overflow would all grow without bound
// across iterations (amortized append growth of one hot key). The finder is
// therefore replaced off the clock every addCityBatch iterations.
func BenchmarkAddCity(b *testing.B) {
	const addCityBatch = 4096
	batch := make([]city.SpatialCity, addCityBatch)
	for i := range batch {
		batch[i] = city.SpatialCity{
			City:     city.City{Name: fmt.Sprintf("Added City %04d", i), Country: "TC"},
			AltNames: []string{fmt.Sprintf("Alt A %04d", i), fmt.Sprintf("Alt B %04d", i)},
		}
	}

	b.ReportAllocs()
	finder := NewNameFinder()
	i := 0
	for b.Loop() {
		if i == addCityBatch {
			b.StopTimer()
			finder, i = NewNameFinder(), 0
			b.StartTimer()
		}
		finder.AddCity(batch[i])
		i++
	}
}

// BenchmarkSerializeIndex benchmarks index serialization of a 10k-name index
// to one fixed path (SerializeIndex writes a part file and renames it over
// the target, exactly as the initializer does on every rebuild).
func BenchmarkSerializeIndex(b *testing.B) {
	finder := buildDiverseIndex(b, 10000)
	path := filepath.Join(b.TempDir(), "test_index.gob")

	b.ReportAllocs()
	for b.Loop() {
		if err := finder.SerializeIndex(path); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDeserializeIndex benchmarks index deserialization of a 10k-name
// index. The file is read through a warm OS page cache after the first
// iteration: this measures decode CPU, not cold disk I/O.
func BenchmarkDeserializeIndex(b *testing.B) {
	for _, n := range []int{10_000, 200_000} {
		b.Run(fmt.Sprintf("cities=%d", n), func(b *testing.B) {
			finder := buildDiverseIndex(b, n)
			path := filepath.Join(b.TempDir(), "test_index.gob")
			if err := finder.SerializeIndex(path); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := DeserializeIndex(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMemoryUsage reports the heap an index RETAINS after a build
// (retained-MB, measured after a forced GC with the finder kept alive) and
// the bytes the build allocated in total (alloc-MB/op, transient garbage
// included). The input fixture is generated before the first reading so it
// is excluded from both. The GCs and MemStats reads run off the clock; ns/op
// is the build time.
func BenchmarkMemoryUsage(b *testing.B) {
	for _, size := range []struct {
		name string
		size int
	}{
		{"10K", 10000},
		{"100K", 100000},
		{"1M", 1000000},
	} {
		b.Run(size.name, func(b *testing.B) {
			cities := benchDiverseCities(size.size)
			silenceBuildLogs(b)
			var retained, allocated float64
			for b.Loop() {
				b.StopTimer()
				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				b.StartTimer()

				finder := BuildIndex(cities)

				b.StopTimer()
				runtime.GC()
				runtime.ReadMemStats(&after)
				// KeepAlive, not `_ = finder`: a blank assignment does not
				// extend the index's lifetime, so without this the GC above
				// could reclaim it and report a near-zero retained heap.
				runtime.KeepAlive(finder)
				retained = float64(after.HeapAlloc) - float64(before.HeapAlloc)
				allocated = float64(after.TotalAlloc - before.TotalAlloc)
				b.StartTimer()
			}
			b.ReportMetric(retained/(1<<20), "retained-MB")
			b.ReportMetric(allocated/(1<<20), "alloc-MB/op")
		})
	}
}
