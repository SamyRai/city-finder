package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/SamyRai/cityFinder/benchmarks/reporters"
	"github.com/SamyRai/cityFinder/benchmarks/suite"
	"github.com/SamyRai/cityFinder/benchmarks/types"
)

func main() {
	// Parse command line flags
	var (
		outputFormat  = flag.String("format", "console", "Output format: console, json, csv, html")
		outputFile    = flag.String("output", "", "Output file path")
		datasetSizes  = flag.String("sizes", "1000,5000,10000", "Comma-separated list of dataset sizes")
		iterations    = flag.Int("iterations", 1, "Number of benchmark iterations")
		enableCPUProf = flag.Bool("cpuprofile", false, "Enable CPU profiling")
		enableMemProf = flag.Bool("memprofile", false, "Enable memory profiling")
		enableTrace   = flag.Bool("trace", false, "Enable execution tracing")
		skipS2        = flag.Bool("skip-s2", false, "Skip S2 index building")
		skipName      = flag.Bool("skip-name", false, "Skip name index building")
		skipPostal    = flag.Bool("skip-postal", false, "Skip postal code index building")
		warmupRuns    = flag.Int("warmup", 0, "Number of warmup runs")
		verbose       = flag.Bool("verbose", false, "Verbose output")
		benchmarkName = flag.String("name", "default", "Benchmark name")
		compare       = flag.String("compare", "", "Compare with previous results file")
	)
	flag.Parse()

	// Create benchmark configuration
	config := createBenchmarkConfig(*benchmarkName, *datasetSizes, *iterations,
		*warmupRuns, *enableCPUProf, *enableMemProf, *enableTrace,
		*skipS2, *skipName, *skipPostal)

	// Create reporter based on format
	reporter := createReporter(*outputFormat, *outputFile, *verbose)

	// Create and run benchmark
	runner := suite.NewBenchmarkRunner(config, reporter)

	if *compare != "" {
		// Run comparison mode
		if err := runComparison(*compare, runner); err != nil {
			log.Fatalf("Comparison failed: %v", err)
		}
	} else {
		// Run single benchmark
		if err := runner.RunAndReport(); err != nil {
			log.Fatalf("Benchmark failed: %v", err)
		}
	}
}

func createBenchmarkConfig(name, sizesStr string, iterations, warmup int,
	enableCPU, enableMem, enableTrace, skipS2, skipName, skipPostal bool) types.BenchmarkConfig {

	// Parse dataset sizes
	sizes := parseDatasetSizes(sizesStr)

	// Determine output formats
	formats := []types.OutputFormat{types.OutputFormatConsole}
	if strings.Contains(strings.ToLower(os.Getenv("BENCHMARK_FORMATS")), "json") {
		formats = append(formats, types.OutputFormatJSON)
	}

	return types.BenchmarkConfig{
		Name:          name,
		DatasetSizes:  sizes,
		EnableCPUProf: enableCPU,
		EnableMemProf: enableMem,
		EnableTrace:   enableTrace,
		OutputFormats: formats,
		Iterations:    iterations,
		WarmupRuns:    warmup,
		SkipComponents: map[string]bool{
			"s2":     skipS2,
			"name":   skipName,
			"postal": skipPostal,
		},
		ConfigPath: "config.json",
		Tags:       map[string]string{"version": "1.0.0"},
	}
}

func parseDatasetSizes(sizesStr string) []int {
	parts := strings.Split(sizesStr, ",")
	sizes := make([]int, 0, len(parts))

	for _, part := range parts {
		if size, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			sizes = append(sizes, size)
		}
	}

	if len(sizes) == 0 {
		return []int{1000, 5000, 10000} // Default sizes
	}

	return sizes
}

func createReporter(format, outputFile string, verbose bool) reporters.Reporter {
	switch strings.ToLower(format) {
	case "json":
		return reporters.NewJSONReporter(outputFile, true)
	case "csv":
		return reporters.NewCSVReporter(outputFile)
	case "html":
		return reporters.NewHTMLReporter(outputFile)
	case "multi":
		// Create multiple reporters
		var reps []reporters.Reporter
		if outputFile == "" {
			reps = append(reps, reporters.NewConsoleReporter(verbose))
			reps = append(reps, reporters.NewJSONReporter("", true))
			reps = append(reps, reporters.NewCSVReporter(""))
			reps = append(reps, reporters.NewHTMLReporter(""))
		} else {
			// Use outputFile as base name for multiple formats
			reps = append(reps, reporters.NewConsoleReporter(verbose))
			reps = append(reps, reporters.NewJSONReporter(outputFile+".json", true))
			reps = append(reps, reporters.NewCSVReporter(outputFile+".csv"))
			reps = append(reps, reporters.NewHTMLReporter(outputFile+".html"))
		}
		return reporters.NewMultiReporter(reps...)
	default:
		return reporters.NewConsoleReporter(verbose)
	}
}

func runComparison(baselineFile string, runner *suite.BenchmarkRunner) error {
	// Load baseline results
	baselineData, err := os.ReadFile(baselineFile)
	if err != nil {
		return fmt.Errorf("failed to read baseline file: %w", err)
	}

	var baseline types.BenchmarkResult
	if err := json.Unmarshal(baselineData, &baseline); err != nil {
		return fmt.Errorf("failed to parse baseline results: %w", err)
	}

	// Run current benchmark
	current, err := runner.Run()
	if err != nil {
		return fmt.Errorf("current benchmark failed: %w", err)
	}

	// Create comparison
	comparison := types.ComparisonResult{
		Baseline:    baseline,
		Current:     current,
		Comparisons: generateComparisons(baseline, current),
		Summary:     generateSummary(baseline, current),
	}

	// Report comparison
	consoleReporter := reporters.NewConsoleReporter(true)
	if err := consoleReporter.ReportComparison(comparison); err != nil {
		return fmt.Errorf("comparison reporting failed: %w", err)
	}

	return nil
}

func generateComparisons(baseline, current types.BenchmarkResult) []types.MetricComparison {
	var comparisons []types.MetricComparison

	// Compare total duration
	if baseline.Duration > 0 {
		durationChange := float64(current.Duration) - float64(baseline.Duration)
		durationPercent := (durationChange / float64(baseline.Duration)) * 100
		comparisons = append(comparisons, types.MetricComparison{
			Metric:        "Total Duration (ms)",
			Baseline:      float64(baseline.Duration.Nanoseconds()) / 1000000,
			Current:       float64(current.Duration.Nanoseconds()) / 1000000,
			Change:        durationChange / 1000000,
			PercentChange: durationPercent,
			Improvement:   durationChange < 0,
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
			Improvement:   memChange < 0,
		})
	}

	// Compare operations
	for i, currOp := range current.Operations {
		if i >= len(baseline.Operations) {
			continue
		}
		baseOp := baseline.Operations[i]

		if baseOp.Duration > 0 {
			durationChange := float64(currOp.Duration) - float64(baseOp.Duration)
			durationPercent := (durationChange / float64(baseOp.Duration)) * 100
			comparisons = append(comparisons, types.MetricComparison{
				Metric:        fmt.Sprintf("%s Duration (ms)", currOp.Name),
				Baseline:      float64(baseOp.Duration.Nanoseconds()) / 1000000,
				Current:       float64(currOp.Duration.Nanoseconds()) / 1000000,
				Change:        durationChange / 1000000,
				PercentChange: durationPercent,
				Improvement:   durationChange < 0,
			})
		}

		if baseOp.Throughput > 0 {
			throughputChange := currOp.Throughput - baseOp.Throughput
			throughputPercent := (throughputChange / baseOp.Throughput) * 100
			comparisons = append(comparisons, types.MetricComparison{
				Metric:        fmt.Sprintf("%s Throughput", currOp.Name),
				Baseline:      baseOp.Throughput,
				Current:       currOp.Throughput,
				Change:        throughputChange,
				PercentChange: throughputPercent,
				Improvement:   throughputChange > 0,
			})
		}
	}

	return comparisons
}

func generateSummary(baseline, current types.BenchmarkResult) types.ComparisonSummary {
	comparisons := generateComparisons(baseline, current)

	improvements := 0
	totalPercentChange := 0.0
	memDelta := 0.0

	for _, comp := range comparisons {
		if comp.Improvement {
			improvements++
		}
		totalPercentChange += comp.PercentChange

		if strings.Contains(comp.Metric, "Memory") {
			memDelta = comp.PercentChange
		}
	}

	avgPercentChange := totalPercentChange / float64(len(comparisons))

	return types.ComparisonSummary{
		OverallImprovement: avgPercentChange < 0,
		SignificantChanges: improvements,
		PerformanceDelta:   avgPercentChange,
		MemoryDelta:        memDelta,
	}
}
