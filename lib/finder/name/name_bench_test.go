package name

import (
	"runtime"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
)

// generateTestCities creates a slice of test cities for benchmarking
func generateTestCities(count int) []city.SpatialCity {
	cities := make([]city.SpatialCity, count)
	for i := 0; i < count; i++ {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      "Test City",
				Country:   "TC",
				Latitude:  float64(i) / 100.0,
				Longitude: float64(i) / 100.0,
			},
			AltNames: []string{"Alt1", "Alt2", "Alt3"},
		}
	}
	return cities
}

// BenchmarkBuildIndex benchmarks the index building process
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
			cities := generateTestCities(size.size)
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_ = BuildIndex(cities)
			}
		})
	}
}

// BenchmarkBuildIndexConcurrent benchmarks concurrent index building
func BenchmarkBuildIndexConcurrent(b *testing.B) {
	cities := generateTestCities(100000)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = BuildIndex(cities)
	}
}

// BenchmarkCityByName benchmarks city lookup by name
func BenchmarkCityByName(b *testing.B) {
	cities := generateTestCities(100000)
	finder := BuildIndex(cities)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = finder.CityByName("Test City", "TC")
	}
}

// BenchmarkCityByNameFuzzy benchmarks fuzzy city lookup
func BenchmarkCityByNameFuzzy(b *testing.B) {
	cities := generateTestCities(100000)
	finder := BuildIndex(cities)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = finder.CityByName("Test Cit", "TC") // Intentional typo to trigger fuzzy search
	}
}

// BenchmarkAddCity benchmarks adding a single city
func BenchmarkAddCity(b *testing.B) {
	finder := NewNameFinder()
	city := city.SpatialCity{
		City: city.City{
			Name:    "Test City",
			Country: "TC",
		},
		AltNames: []string{"Alt1", "Alt2"},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		finder.AddCity(city)
	}
}

// BenchmarkSerializeIndex benchmarks index serialization
func BenchmarkSerializeIndex(b *testing.B) {
	cities := generateTestCities(10000)
	finder := BuildIndex(cities)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Use a temporary file for each iteration
		tmpfile := b.TempDir() + "/test_index.gob"
		_ = finder.SerializeIndex(tmpfile)
	}
}

// BenchmarkDeserializeIndex benchmarks index deserialization
func BenchmarkDeserializeIndex(b *testing.B) {
	cities := generateTestCities(10000)
	finder := BuildIndex(cities)
	tmpfile := b.TempDir() + "/test_index.gob"
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

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			var m1, m2 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m1)

			cities := generateTestCities(size.size)
			finder := BuildIndex(cities)

			runtime.GC()
			runtime.ReadMemStats(&m2)

			b.ReportMetric(float64(m2.Alloc-m1.Alloc)/1024/1024, "MB/op")
			b.ReportMetric(float64(m2.TotalAlloc-m1.TotalAlloc)/1024/1024, "MB_total/op")

			_ = finder // Keep reference to prevent GC
		})
	}
}
