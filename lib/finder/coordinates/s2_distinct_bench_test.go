package coordinates

import (
	"math"
	"math/rand"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
)

// generateDistinctCities creates count points that are distinct on the sphere
// (seeded, deterministic). The shared generateTestCities fixture in
// s2_bench_test.go has period-360 coordinates, so above 360 cities every point
// is duplicated ~len/360 times; that degeneracy inflates query cost and hides
// the true scaling. This fixture measures the realistic case.
func generateDistinctCities(count int) []city.SpatialCity {
	rng := rand.New(rand.NewSource(7))
	cities := make([]city.SpatialCity, count)
	for i := 0; i < count; i++ {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      "Distinct City",
				Country:   "TC",
				Latitude:  math.Round((rng.Float64()*180-90)*1e6) / 1e6,
				Longitude: math.Round((rng.Float64()*360-180)*1e6) / 1e6,
			},
		}
	}
	return cities
}

// BenchmarkNearestPlaceDistinctPoints is the same workload as
// BenchmarkNearestPlace but over 100K distinct points instead of 360 distinct
// points duplicated ~278x.
func BenchmarkNearestPlaceDistinctPoints(b *testing.B) {
	cities := generateDistinctCities(100000)
	cfg := &config.S2{}
	finder, _ := BuildIndex(cities, cfg)

	testPoints := []struct {
		lat, lon float64
	}{
		{37.7749, -122.4194}, // San Francisco
		{40.7128, -74.0060},  // New York
		{51.5074, -0.1278},   // London
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		point := testPoints[i%len(testPoints)]
		_, _, _ = finder.NearestPlace(point.lat, point.lon)
	}
}
