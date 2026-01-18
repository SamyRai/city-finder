package suite

import (
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/SamyRai/cityFinder/benchmarks/profilers"
	"github.com/SamyRai/cityFinder/benchmarks/reporters"
	"github.com/SamyRai/cityFinder/benchmarks/types"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

// BenchmarkRunner manages the execution of benchmark suites
type BenchmarkRunner struct {
	config      types.BenchmarkConfig
	reporter    reporters.Reporter
	profiler    *profilers.Profiler
	queryConfig *QueryBenchmarkConfig
	finders     map[string]*finder.Finder
}

// NewBenchmarkRunner creates a new benchmark runner
func NewBenchmarkRunner(config types.BenchmarkConfig, reporter reporters.Reporter) *BenchmarkRunner {
	return &BenchmarkRunner{
		config:   config,
		reporter: reporter,
	}
}

// NewQueryBenchmarkRunner creates a new benchmark runner for query benchmarks
func NewQueryBenchmarkRunner(config types.BenchmarkConfig, queryConfig QueryBenchmarkConfig, finders map[string]*finder.Finder, reporter reporters.Reporter) *BenchmarkRunner {
	return &BenchmarkRunner{
		config:      config,
		queryConfig: &queryConfig,
		finders:     finders,
		reporter:    reporter,
	}
}

// Run executes the benchmark suite
func (r *BenchmarkRunner) Run() (types.BenchmarkResult, error) {
	log.Printf("Starting benchmark: %s", r.config.Name)

	// Initialize profiler
	profiler, err := profilers.NewProfiler(r.config)
	if err != nil {
		return types.BenchmarkResult{}, fmt.Errorf("failed to initialize profiler: %w", err)
	}
	r.profiler = profiler
	defer func() {
		if err := r.profiler.Stop(); err != nil {
			log.Printf("Error stopping profiler: %v", err)
		}
	}()

	result := types.BenchmarkResult{
		Config:     r.config,
		Timestamp:  time.Now(),
		SystemInfo: r.getSystemInfo(),
		Operations: make([]types.OperationResult, 0),
		Metadata:   make(map[string]interface{}),
	}

	// Check if this is a query benchmark or index benchmark
	if r.queryConfig != nil && r.finders != nil {
		// Run query benchmarks
		queryResults, err := r.runQueryBenchmarks()
		if err != nil {
			return types.BenchmarkResult{}, fmt.Errorf("query benchmarks failed: %w", err)
		}
		result.Operations = append(result.Operations, queryResults...)
	} else {
		// Run index benchmarks (original functionality)
		// Run warmup iterations if specified
		for i := 0; i < r.config.WarmupRuns; i++ {
			log.Printf("Warmup run %d/%d", i+1, r.config.WarmupRuns)
			if err := r.runWarmup(); err != nil {
				log.Printf("Warmup failed: %v", err)
			}
		}

		// Run benchmark iterations
		startTime := time.Now()
		for iteration := 0; iteration < r.config.Iterations; iteration++ {
			log.Printf("Running iteration %d/%d", iteration+1, r.config.Iterations)

			iterationResult, err := r.runIteration()
			if err != nil {
				log.Printf("Iteration %d failed: %v", iteration+1, err)
				continue
			}

			result.Operations = append(result.Operations, iterationResult...)
		}

		result.Duration = time.Since(startTime)
	}

	result.MemoryUsage = r.profiler.GetCurrentMemoryStats()
	result.CPUUsage = r.profiler.GetCPUStats()

	// Capture final memory profile
	if r.config.EnableMemProf {
		if err := r.profiler.CaptureMemoryProfile(); err != nil {
			log.Printf("Failed to capture memory profile: %v", err)
		}
	}

	log.Printf("Benchmark completed in %v", result.Duration)
	return result, nil
}

// runQueryBenchmarks executes query benchmarks
func (r *BenchmarkRunner) runQueryBenchmarks() ([]types.OperationResult, error) {
	runner := NewQueryBenchmarkRunnerInternal(*r.queryConfig, r.finders)
	return runner.RunQueryBenchmarks()
}

func (r *BenchmarkRunner) runWarmup() error {
	// Implement warmup logic - run a quick version of the benchmark
	// This helps stabilize performance measurements
	return r.runQuickBenchmark(100) // Small dataset for warmup
}

func (r *BenchmarkRunner) runIteration() ([]types.OperationResult, error) {
	var results []types.OperationResult

	// Run benchmarks for each dataset size
	for _, size := range r.config.DatasetSizes {
		sizeResults, err := r.runBenchmarkForSize(size)
		if err != nil {
			return nil, fmt.Errorf("benchmark failed for size %d: %w", size, err)
		}
		results = append(results, sizeResults...)
	}

	return results, nil
}

func (r *BenchmarkRunner) runBenchmarkForSize(size int) ([]types.OperationResult, error) {
	var results []types.OperationResult

	// Data loading operation
	loadResult, cities, postalCodes, err := r.measureDataLoading(size)
	if err != nil {
		return nil, fmt.Errorf("data loading failed: %w", err)
	}
	results = append(results, loadResult)

	// S2 index building (if not skipped)
	if !r.config.SkipComponents["s2"] {
		s2Result, err := r.measureS2IndexBuilding(cities)
		if err != nil {
			return nil, fmt.Errorf("S2 index building failed: %w", err)
		}
		results = append(results, s2Result)
	}

	// Name index building (if not skipped)
	if !r.config.SkipComponents["name"] {
		nameResult, err := r.measureNameIndexBuilding(cities)
		if err != nil {
			return nil, fmt.Errorf("name index building failed: %w", err)
		}
		results = append(results, nameResult)
	}

	// Postal code index building (if not skipped)
	if !r.config.SkipComponents["postal"] {
		postalResult, err := r.measurePostalIndexBuilding(postalCodes)
		if err != nil {
			return nil, fmt.Errorf("postal index building failed: %w", err)
		}
		results = append(results, postalResult)
	}

	return results, nil
}

func (r *BenchmarkRunner) measureDataLoading(size int) (types.OperationResult, []city.SpatialCity, map[string]map[string]dataLoader.PostalCodeEntry, error) {
	var cities []city.SpatialCity
	var postalCodes map[string]map[string]dataLoader.PostalCodeEntry

	result, err := r.profiler.MeasureOperation(fmt.Sprintf("Data Loading (%d cities)", size), func() error {
		// Load cities data
		var err error
		cities, err = dataLoader.LoadGeoNamesCSVWithLimit("testdata/allCountries.txt", size)
		if err != nil {
			return fmt.Errorf("failed to load cities: %w", err)
		}

		// Load postal codes data
		postalCodes, err = dataLoader.LoadPostalCodes("testdata/zipCodes.txt")
		if err != nil {
			log.Printf("Warning: failed to load postal codes: %v", err)
			postalCodes = make(map[string]map[string]dataLoader.PostalCodeEntry)
		}

		return nil
	})

	if err != nil {
		return types.OperationResult{}, nil, nil, err
	}

	result.ItemsProcessed = len(cities)
	result.Throughput = float64(result.ItemsProcessed) / result.Duration.Seconds()
	result.Metadata = map[string]interface{}{"dataset_size": size}

	return result, cities, postalCodes, nil
}

func (r *BenchmarkRunner) measureS2IndexBuilding(cities []city.SpatialCity) (types.OperationResult, error) {
	result, err := r.profiler.MeasureOperation("S2 Index Building", func() error {
		// Build S2 index from cities
		s2Config := &config.S2{
			MinLevel: 10,
			MaxLevel: 16,
			MaxCells: 8,
		}

		_, err := coordinates.BuildIndex(cities, s2Config)
		if err != nil {
			return fmt.Errorf("failed to build S2 index: %w", err)
		}
		return nil
	})

	if err != nil {
		return types.OperationResult{}, err
	}

	result.ItemsProcessed = len(cities)
	result.Throughput = float64(result.ItemsProcessed) / result.Duration.Seconds()
	result.Metadata = map[string]interface{}{"index_type": "s2"}

	return result, nil
}

func (r *BenchmarkRunner) measureNameIndexBuilding(cities []city.SpatialCity) (types.OperationResult, error) {
	result, err := r.profiler.MeasureOperation("Name Index Building", func() error {
		// Use the optimized bulk BuildIndex function instead of individual AddCity calls
		name.BuildIndex(cities)
		return nil
	})

	if err != nil {
		return types.OperationResult{}, err
	}

	result.ItemsProcessed = len(cities)
	result.Throughput = float64(result.ItemsProcessed) / result.Duration.Seconds()
	result.Metadata = map[string]interface{}{"index_type": "name"}

	return result, nil
}

func (r *BenchmarkRunner) measurePostalIndexBuilding(postalCodes map[string]map[string]dataLoader.PostalCodeEntry) (types.OperationResult, error) {
	// Count total entries for reporting
	totalEntries := 0
	for _, countryCodes := range postalCodes {
		totalEntries += len(countryCodes)
	}

	result, err := r.profiler.MeasureOperation("Postal Code Index Building", func() error {
		// Build postal code index
		postalCode.BuildIndex(postalCodes)
		return nil
	})

	if err != nil {
		return types.OperationResult{}, err
	}

	result.ItemsProcessed = totalEntries
	result.Throughput = float64(result.ItemsProcessed) / result.Duration.Seconds()
	result.Metadata = map[string]interface{}{"index_type": "postal"}

	return result, nil
}

func (r *BenchmarkRunner) runQuickBenchmark(size int) error {
	// Quick benchmark for warmup
	_, _, _, err := r.measureDataLoading(size)
	return err
}

func (r *BenchmarkRunner) getSystemInfo() types.SystemInfo {
	return types.SystemInfo{
		GoVersion:    runtime.Version(),
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		CPUCores:     runtime.NumCPU(),
		MemoryTotal:  0,           // Not easily available in a cross-platform way
		Hostname:     "localhost", // Could be improved with actual hostname
	}
}

// RunAndReport runs the benchmark and reports results
func (r *BenchmarkRunner) RunAndReport() error {
	result, err := r.Run()
	if err != nil {
		return fmt.Errorf("benchmark execution failed: %w", err)
	}

	if err := r.reporter.Report(result); err != nil {
		return fmt.Errorf("reporting failed: %w", err)
	}

	return nil
}

// BenchmarkSuiteRunner manages multiple benchmark configurations
type BenchmarkSuiteRunner struct {
	suite    types.BenchmarkSuite
	reporter reporters.Reporter
	results  []types.BenchmarkResult
}

// NewBenchmarkSuiteRunner creates a new suite runner
func NewBenchmarkSuiteRunner(suite types.BenchmarkSuite, reporter reporters.Reporter) *BenchmarkSuiteRunner {
	return &BenchmarkSuiteRunner{
		suite:    suite,
		reporter: reporter,
		results:  make([]types.BenchmarkResult, 0),
	}
}

// RunSuite executes all benchmarks in the suite
func (r *BenchmarkSuiteRunner) RunSuite() error {
	log.Printf("Starting benchmark suite: %s", r.suite.Name)
	log.Printf("Description: %s", r.suite.Description)

	for i, config := range r.suite.Benchmarks {
		log.Printf("Running benchmark %d/%d: %s", i+1, len(r.suite.Benchmarks), config.Name)

		runner := NewBenchmarkRunner(config, r.reporter)
		result, err := runner.Run()
		if err != nil {
			log.Printf("Benchmark %s failed: %v", config.Name, err)
			continue
		}

		r.results = append(r.results, result)
	}

	log.Printf("Benchmark suite completed. Ran %d benchmarks.", len(r.results))
	return nil
}

// GetResults returns all benchmark results
func (r *BenchmarkSuiteRunner) GetResults() []types.BenchmarkResult {
	return r.results
}

// CompareResults compares the last two benchmark results
func (r *BenchmarkSuiteRunner) CompareResults() (types.ComparisonResult, error) {
	if len(r.results) < 2 {
		return types.ComparisonResult{}, fmt.Errorf("need at least 2 results to compare")
	}

	current := r.results[len(r.results)-1]
	baseline := r.results[len(r.results)-2]

	comparison := types.ComparisonResult{
		Baseline:    baseline,
		Current:     current,
		Comparisons: r.generateComparisons(baseline, current),
		Summary:     r.generateSummary(baseline, current),
	}

	return comparison, nil
}

func (r *BenchmarkSuiteRunner) generateComparisons(baseline, current types.BenchmarkResult) []types.MetricComparison {
	var comparisons []types.MetricComparison

	// Compare total duration
	if baseline.Duration > 0 {
		durationChange := float64(current.Duration) - float64(baseline.Duration)
		durationPercent := (durationChange / float64(baseline.Duration)) * 100
		comparisons = append(comparisons, types.MetricComparison{
			Metric:        "Total Duration",
			Baseline:      float64(baseline.Duration.Milliseconds()),
			Current:       float64(current.Duration.Milliseconds()),
			Change:        durationChange,
			PercentChange: durationPercent,
			Improvement:   durationChange < 0, // Lower duration is better
		})
	}

	// Compare memory usage
	if baseline.MemoryUsage.Peak > 0 {
		memChange := float64(current.MemoryUsage.Peak) - float64(baseline.MemoryUsage.Peak)
		memPercent := (memChange / float64(baseline.MemoryUsage.Peak)) * 100
		comparisons = append(comparisons, types.MetricComparison{
			Metric:        "Peak Memory (MB)",
			Baseline:      float64(baseline.MemoryUsage.Peak) / 1024 / 1024,
			Current:       float64(current.MemoryUsage.Peak) / 1024 / 1024,
			Change:        memChange / 1024 / 1024,
			PercentChange: memPercent,
			Improvement:   memChange < 0, // Lower memory is better
		})
	}

	// Compare GC cycles
	if baseline.MemoryUsage.GCCycles > 0 {
		gcChange := float64(current.MemoryUsage.GCCycles) - float64(baseline.MemoryUsage.GCCycles)
		gcPercent := (gcChange / float64(baseline.MemoryUsage.GCCycles)) * 100
		comparisons = append(comparisons, types.MetricComparison{
			Metric:        "GC Cycles",
			Baseline:      float64(baseline.MemoryUsage.GCCycles),
			Current:       float64(current.MemoryUsage.GCCycles),
			Change:        gcChange,
			PercentChange: gcPercent,
			Improvement:   gcChange < 0, // Fewer GC cycles is better
		})
	}

	return comparisons
}

func (r *BenchmarkSuiteRunner) generateSummary(baseline, current types.BenchmarkResult) types.ComparisonSummary {
	comparisons := r.generateComparisons(baseline, current)

	improvements := 0
	totalPercentChange := 0.0

	for _, comp := range comparisons {
		if comp.Improvement {
			improvements++
		}
		totalPercentChange += comp.PercentChange
	}

	avgPercentChange := totalPercentChange / float64(len(comparisons))

	return types.ComparisonSummary{
		OverallImprovement: avgPercentChange < 0, // Negative change means improvement
		SignificantChanges: improvements,
		PerformanceDelta:   avgPercentChange,
		MemoryDelta:        0, // Could be calculated from memory comparisons
	}
}
