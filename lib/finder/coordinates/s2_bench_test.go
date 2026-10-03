package coordinates

import (
	"io"
	"log"
	"math"
	"math/rand"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/SamyRai/cityFinder/internal/testfixture"
	"github.com/SamyRai/cityFinder/lib/city"
)

// Benchmark fixtures. Two worlds are used, chosen per question:
//
//   - generateDistinctCities: points uniform in lat/lon degrees, all distinct.
//     Used where only N matters (index lifecycle, scaling-by-N curves).
//   - anchoredOceanFixture (s2_anchored_test.go): 200k cities clustered on
//     seven synthetic continents with a heavy-tailed population ladder — the
//     closest in-repo shape to GeoNames (land-clustered, mostly-ocean globe).
//     Used for the query benchmarks that make production-shaped claims.
//
// A former fixture placed i%360 longitudes, so above 360 cities every point
// was duplicated ~N/360 times; that degeneracy inflated query cost and hid
// the true scaling, and it was removed with the benchmarks that used it.

// generateDistinctCities creates count points that are distinct on the sphere
// (seeded, deterministic).
func generateDistinctCities(count int) []city.SpatialCity {
	rng := rand.New(rand.NewSource(7))
	return testfixture.Cities(count, testfixture.Spec{
		Name:    testfixture.Const("Distinct City"),
		Country: testfixture.Const("TC"),
		Lat:     func(int) float64 { return math.Round((rng.Float64()*180-90)*1e6) / 1e6 },
		Lon:     func(int) float64 { return math.Round((rng.Float64()*360-180)*1e6) / 1e6 },
	})
}

// benchQueryPoint is one query coordinate.
type benchQueryPoint struct{ lat, lon float64 }

// benchQueryCount sizes the query sets: large enough that a sweep does not
// sit in one warm corner of the index (cycling three fixed points — the old
// workload — measured a cache- and branch-predictor-friendly special case),
// small enough to generate in microseconds.
const benchQueryCount = 4096

// uniformSphereQueries returns n seeded query points uniformly distributed
// over the sphere's surface (latitude via asin of a uniform z), the same
// "random global coordinates" shape the production latency passes use. On a
// land-clustered world most of them are over open water.
func uniformSphereQueries(n int, seed int64) []benchQueryPoint {
	rng := rand.New(rand.NewSource(seed))
	points := make([]benchQueryPoint, n)
	for i := range points {
		points[i] = benchQueryPoint{
			lat: math.Asin(2*rng.Float64()-1) * 180 / math.Pi,
			lon: rng.Float64()*360 - 180,
		}
	}
	return points
}

// silenceIndexLogs discards the package's log output (BuildIndex logs
// progress, (de)serialization logs timings) for the rest of the test or benchmark.
// The log calls still format their messages — only the terminal write is
// dropped — so measured ops keep their logging CPU while the output stays
// parseable by benchstat.
func silenceIndexLogs(b testing.TB) {
	b.Helper()
	old := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(old) })
}

// buildBenchIndex builds an index over cities, failing the test or benchmark on error.
func buildBenchIndex(b testing.TB, cities []city.SpatialCity) *S2Finder {
	b.Helper()
	finder, err := BuildIndex(cities)
	if err != nil {
		b.Fatal(err)
	}
	return finder
}

var benchSizes = []struct {
	name string
	size int
}{
	{"1K", 1_000},
	{"10K", 10_000},
	{"100K", 100_000},
	{"1M", 1_000_000},
}

// BenchmarkBuildIndex benchmarks S2 index building over N distinct points.
// BuildIndex does not mutate its input, so every iteration builds from
// identical data; the measured op includes the eager ShapeIndex.Build() a
// real boot pays.
func BenchmarkBuildIndex(b *testing.B) {
	silenceIndexLogs(b)
	for _, size := range benchSizes {
		b.Run(size.name, func(b *testing.B) {
			cities := generateDistinctCities(size.size)
			b.ReportAllocs()
			for b.Loop() {
				buildBenchIndex(b, cities)
			}
		})
	}
}

// BenchmarkSerializeIndex benchmarks serialization of a 100k-point index to
// one fixed path (SerializeIndex writes a part file and renames it over the
// target, as every rebuild does). Creating a fresh temp dir per iteration —
// the former shape — put directory creation inside the measured op and left
// b.N files on disk until cleanup.
func BenchmarkSerializeIndex(b *testing.B) {
	silenceIndexLogs(b)
	finder := buildBenchIndex(b, generateDistinctCities(100_000))
	path := filepath.Join(b.TempDir(), "test_s2_index.gob")

	b.ReportAllocs()
	for b.Loop() {
		if err := finder.SerializeIndex(path); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDeserializeIndex benchmarks deserialization of a 100k-point index.
// After the first iteration the file is served from a warm OS page cache:
// this measures decode + index rebuild CPU, not cold disk I/O (a cold boot
// from a fresh volume pays disk reads on top).
func BenchmarkDeserializeIndex(b *testing.B) {
	silenceIndexLogs(b)
	finder := buildBenchIndex(b, generateDistinctCities(100_000))
	path := filepath.Join(b.TempDir(), "test_s2_index.gob")
	if err := finder.SerializeIndex(path); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := DeserializeIndex(path); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMemoryUsage reports the heap an index RETAINS after a build
// (retained-MB: HeapAlloc delta across a forced GC, finder kept alive) and
// the bytes the build allocated in total (alloc-MB/op, transient garbage
// included — the s2 library warns construction can transiently use ~20x the
// final index). The input fixture is generated before the first reading so
// it is excluded from both. GCs and MemStats reads run off the clock; ns/op
// is the build time.
func BenchmarkMemoryUsage(b *testing.B) {
	silenceIndexLogs(b)
	for _, size := range benchSizes[1:] {
		b.Run(size.name, func(b *testing.B) {
			cities := generateDistinctCities(size.size)
			var retained, allocated float64
			for b.Loop() {
				b.StopTimer()
				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				b.StartTimer()

				finder := buildBenchIndex(b, cities)

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

// BenchmarkTopPopulationsOf measures the boot-time top-K table construction
// over 1M cities where every city is populated (the selection's worst case).
func BenchmarkTopPopulationsOf(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	cities := make([]city.City, 1_000_000)
	for i := range cities {
		cities[i] = city.City{Latitude: rng.Float64()*180 - 90, Longitude: rng.Float64()*360 - 180, Population: int32(rng.Intn(10_000_000) + 1)}
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(topPopulationsOf(cities)) != topPopulationK {
			b.Fatal("short table")
		}
	}
}
