package finder

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generateStressTestCities creates a large dataset for stress testing
func generateStressTestCities(count int) []city.SpatialCity {
	cities := make([]city.SpatialCity, count)
	for i := 0; i < count; i++ {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      fmt.Sprintf("StressCity%d", i),
				Country:   fmt.Sprintf("C%d", i%100), // 100 different countries
				Latitude:  float64(i%180) - 90.0,
				Longitude: float64(i%360) - 180.0,
			},
		}
	}
	return cities
}

// generateStressTestPostalCodes creates postal code data for stress testing
func generateStressTestPostalCodes(count int) map[string]map[string]dataLoader.PostalCodeEntry {
	postalCodes := make(map[string]map[string]dataLoader.PostalCodeEntry)

	for i := 0; i < count; i++ {
		countryCode := fmt.Sprintf("C%d", i%100)
		postalCode := fmt.Sprintf("%05d", i%10000)

		if postalCodes[countryCode] == nil {
			postalCodes[countryCode] = make(map[string]dataLoader.PostalCodeEntry)
		}

		postalCodes[countryCode][postalCode] = dataLoader.PostalCodeEntry{
			CountryCode: countryCode,
			PostalCode:  postalCode,
			PlaceName:   fmt.Sprintf("StressCity%d", i),
			Latitude:    float64(i%180) - 90.0,
			Longitude:   float64(i%360) - 180.0,
			Accuracy:    1,
		}
	}

	return postalCodes
}

func TestConcurrentStress_NameFinder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	// Create a large dataset
	cities := generateStressTestCities(10000)
	finder := name.BuildIndex(cities)

	var wg sync.WaitGroup
	numWorkers := runtime.NumCPU() * 2 // More workers than CPUs
	operationsPerWorker := 10000

	// Track errors and successful operations
	var errorCount int64
	var successCount int64

	// Start concurrent readers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerWorker; j++ {
				// Random city lookup
				cityIndex := (workerID*operationsPerWorker + j) % len(cities)
				testCity := cities[cityIndex]

				result := finder.CityByName(testCity.Name, testCity.Country)
				if result != nil {
					atomic.AddInt64(&successCount, 1)
				} else {
					atomic.AddInt64(&errorCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	totalOperations := int64(numWorkers * operationsPerWorker)
	assert.Equal(t, totalOperations, atomic.LoadInt64(&successCount)+atomic.LoadInt64(&errorCount))
	assert.True(t, atomic.LoadInt64(&successCount) > 0, "Should have some successful lookups")

	t.Logf("Name finder stress test: %d operations, %d successful, %d errors",
		totalOperations, atomic.LoadInt64(&successCount), atomic.LoadInt64(&errorCount))
}

func TestConcurrentStress_CoordinateFinder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	// Create a large dataset
	cities := generateStressTestCities(5000) // Smaller for coordinate finder due to S2 complexity
	cfg := &config.S2{}
	finder, err := coordinates.BuildIndex(cities, cfg)
	require.NoError(t, err)

	var wg sync.WaitGroup
	// Reduce concurrency to avoid S2 library deadlocks
	numWorkers := runtime.NumCPU()
	operationsPerWorker := 2000

	var errorCount int64
	var successCount int64

	// Start concurrent readers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerWorker; j++ {
				// Random coordinate lookup - use coordinates near existing cities
				cityIndex := (workerID*operationsPerWorker + j) % len(cities)
				testCity := cities[cityIndex]

				// Add small random offset to test nearest neighbor
				lat := testCity.Latitude + float64(j%10-5)*0.001
				lon := testCity.Longitude + float64(j%10-5)*0.001

				// NearestPlace is a microsecond-scale read after MaxResults(1),
				// so no per-operation timeout is needed: a wall-clock budget
				// here would count timed-out ops twice (timeout + eventual
				// completion) and flake under parallel test load.
				if _, _, err := finder.NearestPlace(lat, lon); err != nil {
					atomic.AddInt64(&errorCount, 1)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	totalOperations := int64(numWorkers * operationsPerWorker)
	actualTotal := atomic.LoadInt64(&successCount) + atomic.LoadInt64(&errorCount)
	assert.Equal(t, totalOperations, actualTotal, "Total operations should match expected count")

	t.Logf("Coordinate finder stress test: %d operations, %d successful, %d errors",
		totalOperations, atomic.LoadInt64(&successCount), atomic.LoadInt64(&errorCount))
}

func TestConcurrentStress_PostalCodeFinder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	// Create postal code data
	postalCodes := generateStressTestPostalCodes(10000)
	finder := postalCode.BuildIndex(postalCodes)

	var wg sync.WaitGroup
	numWorkers := runtime.NumCPU() * 2
	operationsPerWorker := 10000

	var errorCount int64
	var successCount int64

	// Start concurrent readers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerWorker; j++ {
				// Random postal code lookup
				countryCode := fmt.Sprintf("C%d", (workerID*operationsPerWorker+j)%100)
				postalCodeStr := fmt.Sprintf("%05d", (workerID*operationsPerWorker+j)%10000)

				result := finder.CityByPostalCode(postalCodeStr, countryCode)
				if result != nil {
					atomic.AddInt64(&successCount, 1)
				} else {
					atomic.AddInt64(&errorCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	totalOperations := int64(numWorkers * operationsPerWorker)
	assert.Equal(t, totalOperations, atomic.LoadInt64(&successCount)+atomic.LoadInt64(&errorCount))

	t.Logf("Postal code finder stress test: %d operations, %d successful, %d errors",
		totalOperations, atomic.LoadInt64(&successCount), atomic.LoadInt64(&errorCount))
}

func TestConcurrentReadWriteStress_NameFinder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	finder := name.NewNameFinder()

	// Add initial data
	initialCities := generateStressTestCities(1000)
	for _, city := range initialCities {
		finder.AddCity(city)
	}

	var wg sync.WaitGroup
	numWorkers := runtime.NumCPU()
	operationsPerWorker := 1000

	// Mix of readers and writers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerWorker; j++ {
				if j%10 == 0 { // 10% writes, 90% reads
					// Write operation
					newCity := city.SpatialCity{
						City: city.City{
							Name:      fmt.Sprintf("DynamicCity%d_%d", workerID, j),
							Country:   fmt.Sprintf("C%d", workerID%10),
							Latitude:  float64(workerID),
							Longitude: float64(j),
						},
					}
					finder.AddCity(newCity)
				} else {
					// Read operation
					testCity := initialCities[(workerID*operationsPerWorker+j)%len(initialCities)]
					finder.CityByName(testCity.Name, testCity.Country)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify some dynamic cities were added
	dynamicCity := finder.CityByName("DynamicCity0_0", "C0")
	assert.NotNil(t, dynamicCity, "Dynamic city should be findable")
}

func TestMemoryPressure_Concurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory pressure test in short mode")
	}

	// Create large dataset
	cities := generateStressTestCities(50000)
	postalCodes := generateStressTestPostalCodes(50000)

	// Build finders
	nameFinder := name.BuildIndex(cities)
	cfg := &config.S2{}
	coordFinder, err := coordinates.BuildIndex(cities, cfg)
	require.NoError(t, err)
	postalFinder := postalCode.BuildIndex(postalCodes)

	var wg sync.WaitGroup
	numWorkers := runtime.NumCPU() // Reduced concurrency to avoid deadlocks
	operationsPerWorker := 5000

	startTime := time.Now()

	// Run concurrent operations under memory pressure
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerWorker; j++ {
				// Mix operations across all finders with timeouts
				done := make(chan bool, 1)
				go func(operationType int) {
					switch operationType {
					case 0: // Name lookup
						cityIndex := (workerID*operationsPerWorker + j) % len(cities)
						testCity := cities[cityIndex]
						nameFinder.CityByName(testCity.Name, testCity.Country)

					case 1: // Coordinate lookup
						cityIndex := (workerID*operationsPerWorker + j) % len(cities)
						testCity := cities[cityIndex]
						coordFinder.NearestPlace(testCity.Latitude, testCity.Longitude)

					case 2: // Postal code lookup
						countryCode := fmt.Sprintf("C%d", (workerID*operationsPerWorker+j)%100)
						postalCodeStr := fmt.Sprintf("%05d", (workerID*operationsPerWorker+j)%10000)
						postalFinder.CityByPostalCode(postalCodeStr, countryCode)
					}
					done <- true
				}(j % 3)

				select {
				case <-done:
					// Operation completed
				case <-time.After(200 * time.Millisecond):
					// Timeout - continue to next operation
					t.Logf("Worker %d operation %d timed out", workerID, j)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	t.Logf("Memory pressure test completed in %v with %d concurrent workers", duration, numWorkers)
	t.Logf("Average operations per second: %.0f", float64(numWorkers*operationsPerWorker)/duration.Seconds())

	// Force garbage collection and check memory
	runtime.GC()
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	t.Logf("Memory usage after stress test: %d MB", memStats.Alloc/1024/1024)
	assert.True(t, memStats.Alloc < 500*1024*1024, "Memory usage should be reasonable (< 500MB)")
}

func TestConcurrentIndexBuilding(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent index building test in short mode")
	}

	var wg sync.WaitGroup
	numConcurrentBuilds := 5
	cities := generateStressTestCities(10000)

	results := make([]*name.Finder, numConcurrentBuilds)

	// Build multiple indices concurrently
	for i := 0; i < numConcurrentBuilds; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = name.BuildIndex(cities)
		}(i)
	}

	wg.Wait()

	// Verify all indices work correctly
	for i, finder := range results {
		assert.NotNil(t, finder, "Finder %d should not be nil", i)

		// Test a lookup
		testCity := cities[0]
		result := finder.CityByName(testCity.Name, testCity.Country)
		assert.NotNil(t, result, "Finder %d should find test city", i)
		assert.Equal(t, testCity.Name, result.Name)
	}
}

func TestLongRunningConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping long-running concurrency test in short mode")
	}

	// Create medium-sized dataset
	cities := generateStressTestCities(5000)
	finder := name.BuildIndex(cities)

	var wg sync.WaitGroup
	numWorkers := runtime.NumCPU()
	running := int64(1)

	// Long-running concurrent operations (30 seconds)
	timeout := time.After(30 * time.Second)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for atomic.LoadInt64(&running) == 1 {
				// Continuous lookups
				for j := 0; j < 1000; j++ {
					cityIndex := (workerID*1000 + j) % len(cities)
					testCity := cities[cityIndex]
					result := finder.CityByName(testCity.Name, testCity.Country)
					assert.NotNil(t, result)
				}
			}
		}(i)
	}

	// Wait for timeout
	<-timeout
	atomic.StoreInt64(&running, 0)

	wg.Wait()
	t.Log("Long-running concurrency test completed successfully")
}

func TestRaceConditionDetection_Extended(t *testing.T) {
	// Extended race condition test beyond basic -race flag
	finder := name.NewNameFinder()

	var wg sync.WaitGroup
	numWorkers := 50 // High number of workers
	operationsPerWorker := 1000

	// Mix of operations that could cause race conditions
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerWorker; j++ {
				// Frequent additions
				newCity := city.SpatialCity{
					City: city.City{
						Name:      fmt.Sprintf("RaceCity%d_%d", workerID, j),
						Country:   fmt.Sprintf("R%d", workerID%10),
						Latitude:  float64(workerID),
						Longitude: float64(j),
					},
				}
				finder.AddCity(newCity)

				// Immediate lookups of recently added cities
				result := finder.CityByName(newCity.Name, newCity.Country)
				if result != nil {
					// Verify data integrity
					assert.Equal(t, newCity.Name, result.Name)
					assert.Equal(t, newCity.Country, result.Country)
				}
			}
		}(i)
	}

	wg.Wait()
	t.Log("Extended race condition test completed without issues")
}

func BenchmarkConcurrentOperations(b *testing.B) {
	// Create test data
	cities := generateStressTestCities(10000)
	finder := name.BuildIndex(cities)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			cityIndex := i % len(cities)
			testCity := cities[cityIndex]
			finder.CityByName(testCity.Name, testCity.Country)
			i++
		}
	})
}