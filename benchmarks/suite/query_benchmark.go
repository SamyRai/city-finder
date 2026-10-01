package suite

import (
	"fmt"
	"sync"
	"time"

	"github.com/SamyRai/cityFinder/benchmarks/types"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
)

// QueryBenchmarkConfig holds configuration for query benchmarks
type QueryBenchmarkConfig struct {
	TestLocations []TestLocation `json:"test_locations"`
	Concurrent    bool           `json:"concurrent"` // Run queries concurrently
	Iterations    int            `json:"iterations"` // Number of iterations per query
}

// TestLocation represents a test location for query benchmarks
type TestLocation struct {
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Expected string  `json:"expected"`
}

// DefaultTestLocations returns the standard test locations used for benchmarking
func DefaultTestLocations() []TestLocation {
	return []TestLocation{
		{40.7128, -74.0060, "New York"},
		{34.0522, -118.2437, "Los Angeles"},
		{41.8781, -87.6298, "Chicago"},
		{51.5074, -0.1278, "London"},
		{48.8566, 2.3522, "Paris"},
		{35.6895, 139.6917, "Tokyo"},
		{55.7558, 37.6176, "Moscow"},
		{-33.8688, 151.2093, "Sydney"},
		{39.9042, 116.4074, "Beijing"},
		{19.4326, -99.1332, "Mexico City"},
		{55.7963, 49.1088, "Kazan"},
		{54.5378, 52.7985, "Bugulma"},
	}
}

// QueryBenchmarkRunner manages query benchmark execution
type QueryBenchmarkRunner struct {
	config  QueryBenchmarkConfig
	finders map[string]*finder.Finder
}

// NewQueryBenchmarkRunnerInternal creates a new query benchmark runner
func NewQueryBenchmarkRunnerInternal(config QueryBenchmarkConfig, finders map[string]*finder.Finder) *QueryBenchmarkRunner {
	return &QueryBenchmarkRunner{
		config:  config,
		finders: finders,
	}
}

// RunQueryBenchmarks executes all query benchmarks
func (r *QueryBenchmarkRunner) RunQueryBenchmarks() ([]types.OperationResult, error) {
	var allResults []types.OperationResult

	// Run benchmarks for each finder
	for finderName, f := range r.finders {
		results, err := r.runFinderBenchmarks(finderName, f)
		if err != nil {
			return nil, fmt.Errorf("failed to run benchmarks for finder %s: %w", finderName, err)
		}
		allResults = append(allResults, results...)
	}

	return allResults, nil
}

// runFinderBenchmarks runs benchmarks for a specific finder
func (r *QueryBenchmarkRunner) runFinderBenchmarks(finderName string, f *finder.Finder) ([]types.OperationResult, error) {
	var results []types.OperationResult

	if r.config.Concurrent {
		// Run concurrent benchmarks
		concurrentResults, err := r.runConcurrentFinderBenchmarks(finderName, f)
		if err != nil {
			return nil, err
		}
		results = append(results, concurrentResults...)
	} else {
		// Run sequential benchmarks
		sequentialResults, err := r.runSequentialFinderBenchmarks(finderName, f)
		if err != nil {
			return nil, err
		}
		results = append(results, sequentialResults...)
	}

	return results, nil
}

// runConcurrentFinderBenchmarks runs benchmarks concurrently for all test locations
func (r *QueryBenchmarkRunner) runConcurrentFinderBenchmarks(finderName string, f *finder.Finder) ([]types.OperationResult, error) {
	var wg sync.WaitGroup
	resultsChan := make(chan types.OperationResult, len(r.config.TestLocations))

	// Start concurrent queries
	for _, loc := range r.config.TestLocations {
		wg.Add(1)
		go func(location TestLocation) {
			defer wg.Done()
			result, err := r.measureSingleQuery(finderName, f, location, true)
			if err != nil {
				// Create error result
				result = types.OperationResult{
					Name:   fmt.Sprintf("%s query for %s", finderName, location.Expected),
					Status: "failed",
					Error:  err.Error(),
				}
			}
			resultsChan <- result
		}(loc)
	}

	// Wait for all queries to complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	var results []types.OperationResult
	for result := range resultsChan {
		results = append(results, result)
	}

	// Aggregate results
	summaryResult := r.createSummaryResult(finderName, results, true)
	results = append(results, summaryResult)

	return results, nil
}

// runSequentialFinderBenchmarks runs benchmarks sequentially for all test locations
func (r *QueryBenchmarkRunner) runSequentialFinderBenchmarks(finderName string, f *finder.Finder) ([]types.OperationResult, error) {
	var results []types.OperationResult

	// Run each query sequentially
	for _, loc := range r.config.TestLocations {
		result, err := r.measureSingleQuery(finderName, f, loc, false)
		if err != nil {
			result = types.OperationResult{
				Name:   fmt.Sprintf("%s query for %s", finderName, loc.Expected),
				Status: "failed",
				Error:  err.Error(),
			}
		}
		results = append(results, result)
	}

	// Aggregate results
	summaryResult := r.createSummaryResult(finderName, results, false)
	results = append(results, summaryResult)

	return results, nil
}

// measureSingleQuery measures the performance of a single query
func (r *QueryBenchmarkRunner) measureSingleQuery(finderName string, f *finder.Finder, loc TestLocation, concurrent bool) (types.OperationResult, error) {
	operationName := fmt.Sprintf("%s query for %s", finderName, loc.Expected)
	if concurrent {
		operationName += " (concurrent)"
	}

	// Note: For now, using simplified timing. In a full implementation,
	// this would integrate with the main profiler system.

	// Note: This is a simplified implementation. In a full implementation,
	// we'd want to integrate this with the main profiler system.
	start := time.Now()

	// Execute the query multiple times for statistical significance
	var totalDuration time.Duration
	var totalMemory uint64
	var successfulQueries int

	for i := 0; i < r.config.Iterations; i++ {
		queryStart := time.Now()
		nearestCity, _, err := f.FindNearestCity(loc.Lat, loc.Lon, coordinates.RankDistance)
		queryDuration := time.Since(queryStart)

		if err != nil {
			continue // Skip failed queries in averaging
		}

		totalDuration += queryDuration
		successfulQueries++

		// Basic memory measurement (simplified)
		// In a full implementation, this would use the profiler's memory tracking
		if nearestCity != nil {
			// Approximate memory usage
			totalMemory += uint64(len(nearestCity.Name)*2 + 32) // Rough estimate
		}
	}

	if successfulQueries == 0 {
		return types.OperationResult{
			Name:   operationName,
			Status: "failed",
			Error:  "no successful queries",
		}, fmt.Errorf("no successful queries")
	}

	avgDuration := totalDuration / time.Duration(successfulQueries)
	avgMemory := totalMemory / uint64(successfulQueries)
	throughput := float64(successfulQueries) / time.Since(start).Seconds()

	return types.OperationResult{
		Name:           operationName,
		Duration:       avgDuration,
		MemoryDelta:    avgMemory,
		ItemsProcessed: successfulQueries,
		Throughput:     throughput,
		Status:         "completed",
		Metadata: map[string]interface{}{
			"finder":     finderName,
			"location":   loc.Expected,
			"lat":        loc.Lat,
			"lon":        loc.Lon,
			"iterations": r.config.Iterations,
			"concurrent": concurrent,
		},
	}, nil
}

// createSummaryResult creates a summary result aggregating all queries for a finder
func (r *QueryBenchmarkRunner) createSummaryResult(finderName string, results []types.OperationResult, concurrent bool) types.OperationResult {
	var totalDuration time.Duration
	var totalMemory uint64
	var totalItems int
	var successfulResults int

	for _, result := range results {
		if result.Status == "completed" {
			totalDuration += result.Duration
			totalMemory += result.MemoryDelta
			totalItems += result.ItemsProcessed
			successfulResults++
		}
	}

	var avgDuration time.Duration
	var avgMemory uint64
	if successfulResults > 0 {
		avgDuration = totalDuration / time.Duration(successfulResults)
		avgMemory = totalMemory / uint64(successfulResults)
	}

	mode := "sequential"
	if concurrent {
		mode = "concurrent"
	}

	return types.OperationResult{
		Name:           fmt.Sprintf("%s queries summary (%s)", finderName, mode),
		Duration:       avgDuration,
		MemoryDelta:    avgMemory,
		ItemsProcessed: totalItems,
		Throughput:     float64(totalItems) / totalDuration.Seconds(),
		Status:         "completed",
		Metadata: map[string]interface{}{
			"finder":             finderName,
			"mode":               mode,
			"total_queries":      len(results),
			"successful_queries": successfulResults,
			"locations_tested":   len(r.config.TestLocations),
		},
	}
}
