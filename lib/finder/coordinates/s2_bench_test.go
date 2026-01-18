package coordinates

import (
	"runtime"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
)

// generateTestCities creates a slice of test cities for benchmarking
func generateTestCities(count int) []city.SpatialCity {
	cities := make([]city.SpatialCity, count)
	for i := 0; i < count; i++ {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      "Test City",
				Country:   "TC",
				Latitude:  float64(i%180) - 90.0,
				Longitude: float64(i%360) - 180.0,
			},
		}
	}
	return cities
}

// BenchmarkBuildIndex benchmarks S2 index building
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

	cfg := &config.S2{}

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			cities := generateTestCities(size.size)
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, _ = BuildIndex(cities, cfg)
			}
		})
	}
}

// BenchmarkNearestPlace benchmarks nearest city lookup
func BenchmarkNearestPlace(b *testing.B) {
	cities := generateTestCities(100000)
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

// BenchmarkSerializeIndex benchmarks index serialization
func BenchmarkSerializeIndex(b *testing.B) {
	cities := generateTestCities(100000)
	cfg := &config.S2{}
	finder, _ := BuildIndex(cities, cfg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tmpfile := b.TempDir() + "/test_s2_index.gob"
		_ = finder.SerializeIndex(tmpfile)
	}
}

// BenchmarkDeserializeIndex benchmarks index deserialization
func BenchmarkDeserializeIndex(b *testing.B) {
	cities := generateTestCities(100000)
	cfg := &config.S2{}
	finder, _ := BuildIndex(cities, cfg)
	tmpfile := b.TempDir() + "/test_s2_index.gob"
	_ = finder.SerializeIndex(tmpfile)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
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

	cfg := &config.S2{}

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			var m1, m2 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m1)

			cities := generateTestCities(size.size)
			finder, _ := BuildIndex(cities, cfg)

			runtime.GC()
			runtime.ReadMemStats(&m2)

			b.ReportMetric(float64(m2.Alloc-m1.Alloc)/1024/1024, "MB/op")
			b.ReportMetric(float64(m2.TotalAlloc-m1.TotalAlloc)/1024/1024, "MB_total/op")

			_ = finder // Keep reference to prevent GC
		})
	}
}
