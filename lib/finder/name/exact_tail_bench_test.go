package name

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
)

// exactTailCountries keys are assigned round-robin to these countries at
// build time; a query for key k belongs to country k % len(exactTailCountries).
var exactTailCountries = []string{
	"AD", "AR", "BR", "CA", "DE", "ES", "FR", "GB", "IT", "JP",
	"MX", "NL", "NO", "PL", "PT", "RU", "SE", "TR", "UA", "US",
}

// exactTailCities builds count distinct-name cities spread round-robin over
// exactTailCountries. Distinct names matter: the pre-existing
// BenchmarkCityByName re-queries one duplicated key, which measures a
// hot-cache single-bucket hit and cannot see the latency tail that real
// distinct-key traffic produces.
func exactTailCities(count int) []city.SpatialCity {
	cities := make([]city.SpatialCity, count)
	for i := 0; i < count; i++ {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      exactTailName(i),
				Country:   exactTailCountries[i%len(exactTailCountries)],
				Latitude:  float64(i%18000) / 100.0,
				Longitude: float64(i%36000)/100.0 - 180.0,
			},
		}
	}
	return cities
}

func exactTailName(i int) string {
	return fmt.Sprintf("Test City Number %d Springs", i)
}

// exactTailQueries picks hit queries deterministically (fixed stride coprime
// to the key count) so every run profiles the same key spread.
func exactTailQueries(keyCount, queryCount int) []struct{ name, country string } {
	const stride = 997
	queries := make([]struct{ name, country string }, queryCount)
	for i := 0; i < queryCount; i++ {
		key := (i * stride) % keyCount
		queries[i].name = exactTailName(key)
		queries[i].country = exactTailCountries[key%len(exactTailCountries)]
	}
	return queries
}

// BenchmarkCityByNameExactTail profiles the exact-name lookup path
// (CityByName phase 1) over distinct synthetic keys. All queries are hits, so
// the fuzzy phases never run. The query window sweeps every one of the 1M
// index keys once (stride order): cycling a small hot key set hides the tail
// (everything stays in L2), while a full sweep pays the cold map-bucket and
// string-hash costs that dominate the production p99. Per-op latencies are
// reported as p50/p99/p99.9 metrics, mirroring the production measurement
// methodology from the README Performance table.
func BenchmarkCityByNameExactTail(b *testing.B) {
	const keyCount = 1_000_000

	b.Log("building index (untimed)")
	finder := BuildIndex(exactTailCities(keyCount))
	queries := exactTailQueries(keyCount, keyCount)

	// Distribution measurement: every key timed once. The two time.Now()
	// calls add ~1% against a ~µs op.
	durations := make([]time.Duration, 0, len(queries))
	for _, q := range queries {
		start := time.Now()
		c := finder.CityByName(q.name, q.country)
		durations = append(durations, time.Since(start))
		if c == nil {
			b.Fatalf("query %q (%s) must hit", q.name, q.country)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	pct := func(p float64) float64 {
		idx := int(p * float64(len(durations)-1))
		return float64(durations[idx].Nanoseconds())
	}

	// Profile loop for -cpuprofile / -benchmem. b.Loop resets the timer on
	// its first call, so the setup and distribution phases above stay
	// untimed. Metrics are reported after the loop: b.ResetTimer deletes
	// user-reported metrics, so reporting before any reset would discard
	// them.
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		q := queries[i%len(queries)]
		if finder.CityByName(q.name, q.country) == nil {
			b.Fatalf("query %q (%s) must hit", q.name, q.country)
		}
		i++
	}

	b.ReportMetric(pct(0.50), "p50-ns")
	b.ReportMetric(pct(0.99), "p99-ns")
	b.ReportMetric(pct(0.999), "p999-ns")
}

// BenchmarkCityByNameExactTailParallel answers one specific question the
// sequential sweep cannot: does the Finder's RWMutex contend when many
// goroutines run exact lookups concurrently (the server's real shape)?
// ns/op here is per goroutine operation across all parallel workers.
func BenchmarkCityByNameExactTailParallel(b *testing.B) {
	const keyCount = 1_000_000

	b.Log("building index (untimed)")
	finder := BuildIndex(exactTailCities(keyCount))
	queries := exactTailQueries(keyCount, keyCount)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			q := queries[i%len(queries)]
			if finder.CityByName(q.name, q.country) == nil {
				b.Fatalf("query %q (%s) must hit", q.name, q.country)
			}
			i++
		}
	})
}
