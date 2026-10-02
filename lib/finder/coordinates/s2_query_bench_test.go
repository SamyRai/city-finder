package coordinates

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// Query benchmarks. Every timed iteration asserts on the result: besides
// catching fixture regressions, consuming the result keeps the call from
// being optimized into something cheaper than what a caller pays.

// BenchmarkNearestDistance measures rank=distance nearest queries on the
// clustered "continents" world with uniformly random global query points —
// the production latency methodology (random global coordinates over
// land-clustered data) at 200k instead of 13.47M points.
func BenchmarkNearestDistance(b *testing.B) {
	silenceIndexLogs(b)
	finder := buildBenchIndex(b, anchoredOceanFixture(b))
	queries := uniformSphereQueries(benchQueryCount, 42)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		q := queries[i%len(queries)]
		if c, _, err := finder.NearestPlace(q.lat, q.lon, RankDistance); err != nil || c == nil {
			b.Fatalf("query %+v: city %v, err %v", q, c, err)
		}
		i++
	}
}

// BenchmarkNearestDistanceScaling is the same query over N distinct
// uniformly spread points, as a curve: query cost grows with index size, and
// a single fixed N cannot show whether that growth stays logarithmic. Read
// the sub-results together (ns/op against N), not individually.
func BenchmarkNearestDistanceScaling(b *testing.B) {
	silenceIndexLogs(b)
	queries := uniformSphereQueries(benchQueryCount, 42)
	for _, size := range benchSizes {
		b.Run("N="+size.name, func(b *testing.B) {
			finder := buildBenchIndex(b, generateDistinctCities(size.size))
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				q := queries[i%len(queries)]
				if c, _, err := finder.NearestPlace(q.lat, q.lon, RankDistance); err != nil || c == nil {
					b.Fatalf("query %+v: city %v, err %v", q, c, err)
				}
				i++
			}
		})
	}
}

// BenchmarkNearestDistanceParallel runs the distance query from GOMAXPROCS
// goroutines at once — the server's shape, and the path that exercises the
// per-finder sync.Pool of query objects under concurrency.
//
// ns/op is wall-clock time over TOTAL operations (aggregate throughput:
// 1e9/ns/op queries per second for the process), not per-query latency. Run
// it as a scaling curve to see contention:
//
//	go test -run '^$' -bench 'NearestDistanceParallel$' -cpu 1,2,4,8 ./lib/finder/coordinates
//
// Workers start at different offsets of the query set so they do not walk
// the same index cells in lockstep.
func BenchmarkNearestDistanceParallel(b *testing.B) {
	silenceIndexLogs(b)
	finder := buildBenchIndex(b, anchoredOceanFixture(b))
	queries := uniformSphereQueries(benchQueryCount, 42)

	var worker atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := int(worker.Add(1)*523) % len(queries)
		for pb.Next() {
			q := queries[i%len(queries)]
			if c, _, err := finder.NearestPlace(q.lat, q.lon, RankDistance); err != nil || c == nil {
				// Fatal must not be called from a RunParallel worker.
				b.Errorf("query %+v: city %v, err %v", q, c, err)
				return
			}
			i++
		}
	})
}

// BenchmarkNearestWithAdmin measures the include=admin read path
// (NearestPlaceWithAdmin) over 100K distinct points with admin codes on every
// city and a small Admin1Names map, so attribution pays its real table
// lookups. Compare the distance sub-run against BenchmarkNearestDistanceScaling
// /N=100K on the same machine and run for the attribution overhead.
//
// The population sub-run covers the land-shaped population query on a
// uniformly populated world (every city populated, so the escalation ladder
// certifies early) — the complement to BenchmarkNearestByPopulationOceanQuery,
// which measures the far-from-land worst case.
func BenchmarkNearestWithAdmin(b *testing.B) {
	silenceIndexLogs(b)
	cities := generateDistinctCities(100_000)
	names := make(map[string]string)
	for i := range cities {
		cities[i].Admin1Code = fmt.Sprintf("%02d", i%100)
		cities[i].Admin2Code = fmt.Sprintf("%03d", i%1000)
		cities[i].Population = int32(1000 + i%90000)
		names[fmt.Sprintf("TC.%02d", i%100)] = fmt.Sprintf("Region %d", i%100)
	}
	finder := buildBenchIndex(b, cities)
	finder.Admin1Names = names
	queries := uniformSphereQueries(benchQueryCount, 42)

	for _, mode := range []struct {
		name string
		rank Rank
	}{
		{"distance", RankDistance},
		{"population", RankPopulation},
	} {
		b.Run(mode.name, func(b *testing.B) {
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				q := queries[i%len(queries)]
				_, _, admin, err := finder.NearestPlaceWithAdmin(q.lat, q.lon, mode.rank)
				if err != nil {
					b.Fatalf("lookup failed: %v", err)
				}
				if admin.Admin1Code == "" {
					b.Fatal("attribution must resolve on this fixture")
				}
				i++
			}
		})
	}
}

// BenchmarkNearestByPopulationOceanQuery benchmarks the population-ranked
// query at two FIXED points on the 200k-city clustered world — deliberately
// single-point experiments, not a distribution:
//
//   - mid-ocean (0, -140): the cheap escalation discs are near-empty over open
//     water, so the cost is dominated by whatever strategy resolves the query
//     after them (the v1.3 top-K-anchored disc). This is the worst case.
//   - populated (35, 100): the land-query path for comparison.
//
// The fixture's anchored-disc win does not transfer 1:1 to production scale
// (see docs/performance.md); use it for regression detection on this path.
func BenchmarkNearestByPopulationOceanQuery(b *testing.B) {
	silenceIndexLogs(b)
	finder := buildBenchIndex(b, anchoredOceanFixture(b))

	for _, p := range []struct {
		name     string
		lat, lon float64
	}{
		{"mid-ocean", 0.0, -140.0},
		{"populated", 35.0, 100.0},
	} {
		b.Run(p.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if c, _, err := finder.NearestPlace(p.lat, p.lon, RankPopulation); err != nil || c == nil {
					b.Fatalf("city %v, err %v", c, err)
				}
			}
		})
	}
}
