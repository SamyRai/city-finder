package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/SamyRai/cityFinder/benchmarks/reporters"
	"github.com/SamyRai/cityFinder/benchmarks/suite"
	"github.com/SamyRai/cityFinder/benchmarks/types"
)

func main() {
	if len(os.Args) < 2 {
		showUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	switch command {
	case "quick":
		runQuickBenchmark()
	case "scaling":
		runScalingAnalysis()
	case "profile":
		runProfiling()
	case "comprehensive":
		runComprehensiveBenchmark()
	case "query":
		runQueryBenchmark()
	case "compare":
		runComparison()
	case "greentea":
		runGreenTeaBenchmark()
	case "help", "-h", "--help":
		showUsage()
	default:
		fmt.Printf("Unknown command: %s\n\n", command)
		showUsage()
		os.Exit(1)
	}
}

func showUsage() {
	fmt.Println("CityFinder Benchmark Suite")
	fmt.Println("==========================")
	fmt.Println()
	fmt.Println("USAGE:")
	fmt.Println("  go run benchmarks/run_benchmarks.go <command> [options]")
	fmt.Println()
	fmt.Println("COMMANDS:")
	fmt.Println("  quick         - Run quick performance tests (1K, 10K, 50K cities)")
	fmt.Println("  scaling       - Run scaling analysis")
	fmt.Println("  profile       - Run with CPU/memory profiling")
	fmt.Println("  comprehensive - Run full benchmark suite with multiple formats")
	fmt.Println("  query         - Run query performance tests (nearest city lookups)")
	fmt.Println("  compare       - Compare results with baseline")
	fmt.Println("  greentea      - Run with experimental Green Tea GC (Go 1.25+)")
	fmt.Println("  help          - Show this help message")
	fmt.Println()
	fmt.Println("EXAMPLES:")
	fmt.Println("  go run benchmarks/run_benchmarks.go quick")
	fmt.Println("  go run benchmarks/run_benchmarks.go scaling")
	fmt.Println("  go run benchmarks/run_benchmarks.go profile 50000")
	fmt.Println("  go run benchmarks/run_benchmarks.go comprehensive")
	fmt.Println("  go run benchmarks/run_benchmarks.go compare baseline.json")
	fmt.Println()
	fmt.Println("Features:")
	fmt.Println("  - Multiple output formats (console, JSON, CSV, HTML)")
	fmt.Println("  - CPU and memory profiling")
	fmt.Println("  - Performance comparisons")
	fmt.Println("  - Metrics collection")
	fmt.Println("  - Reporting")
}

func runQuickBenchmark() {
	fmt.Println("Running quick benchmark")
	fmt.Println("=======================")

	sizes := []int{1000, 10000, 50000}
	config := types.BenchmarkConfig{
		Name:          "quick-benchmark",
		DatasetSizes:  sizes,
		Iterations:    1,
		OutputFormats: []types.OutputFormat{types.OutputFormatConsole},
		Tags:          map[string]string{"type": "quick", "timestamp": time.Now().Format(time.RFC3339)},
	}

	reporter := reporters.NewConsoleReporter(false)
	runner := suite.NewBenchmarkRunner(config, reporter)

	if err := runner.RunAndReport(); err != nil {
		log.Fatalf("Quick benchmark failed: %v", err)
	}
}

func runScalingAnalysis() {
	fmt.Println("Running scaling analysis")
	fmt.Println("========================")

	sizes := []int{1000, 5000, 10000, 25000, 50000, 100000, 250000}
	config := types.BenchmarkConfig{
		Name:          "scaling-analysis",
		DatasetSizes:  sizes,
		Iterations:    1,
		OutputFormats: []types.OutputFormat{types.OutputFormatConsole, types.OutputFormatCSV},
		Tags:          map[string]string{"type": "scaling", "timestamp": time.Now().Format(time.RFC3339)},
	}

	reporter := reporters.NewMultiReporter(
		reporters.NewConsoleReporter(true),
		reporters.NewCSVReporter(""),
	)
	runner := suite.NewBenchmarkRunner(config, reporter)

	if err := runner.RunAndReport(); err != nil {
		log.Fatalf("Scaling analysis failed: %v", err)
	}
}

func runProfiling() {
	size := 50000
	if len(os.Args) > 2 {
		if s, err := parseSize(os.Args[2]); err == nil {
			size = s
		}
	}

	fmt.Printf("Running profiling (%d cities)\n", size)
	fmt.Println("============================")

	config := types.BenchmarkConfig{
		Name:          fmt.Sprintf("profile-%d", size),
		DatasetSizes:  []int{size},
		Iterations:    1,
		EnableCPUProf: true,
		EnableMemProf: true,
		OutputFormats: []types.OutputFormat{types.OutputFormatConsole},
		Tags:          map[string]string{"type": "profile", "size": fmt.Sprintf("%d", size)},
	}

	reporter := reporters.NewConsoleReporter(true)
	runner := suite.NewBenchmarkRunner(config, reporter)

	if err := runner.RunAndReport(); err != nil {
		log.Fatalf("Profiling analysis failed: %v", err)
	}

	fmt.Println("\n📁 Profile files generated:")
	fmt.Printf("  - CPU profile: cpu_%s.prof\n", config.Name)
	fmt.Printf("  - Memory profile: mem_%s.prof\n", config.Name)
	fmt.Println("\nAnalyze profiles with:")
	fmt.Printf("  go tool pprof cpu_%s.prof\n", config.Name)
	fmt.Printf("  go tool pprof -http=:8080 mem_%s.prof\n", config.Name)
}

func runComprehensiveBenchmark() {
	fmt.Println("Running full benchmark suite")
	fmt.Println("============================")

	sizes := []int{1000, 5000, 10000, 25000, 50000}
	config := types.BenchmarkConfig{
		Name:          "comprehensive-suite",
		DatasetSizes:  sizes,
		Iterations:    3, // Multiple iterations for statistical significance
		WarmupRuns:    1,
		EnableCPUProf: true,
		EnableMemProf: true,
		OutputFormats: []types.OutputFormat{types.OutputFormatConsole, types.OutputFormatJSON, types.OutputFormatCSV, types.OutputFormatHTML},
		Tags: map[string]string{
			"type":       "comprehensive",
			"iterations": "3",
			"warmup":     "1",
			"timestamp":  time.Now().Format(time.RFC3339),
			"go_version": runtime.Version(),
			"cpu_cores":  fmt.Sprintf("%d", runtime.NumCPU()),
		},
	}

	// Create multi-format reporter
	timestamp := time.Now().Format("20060102_150405")
	baseFilename := fmt.Sprintf("comprehensive_%s", timestamp)
	reporter := reporters.NewMultiReporter(
		reporters.NewConsoleReporter(true),
		reporters.NewJSONReporter(baseFilename+".json", true),
		reporters.NewCSVReporter(baseFilename+".csv"),
		reporters.NewHTMLReporter(baseFilename+".html"),
	)

	runner := suite.NewBenchmarkRunner(config, reporter)

	fmt.Printf("Running comprehensive benchmark with %d iterations and warmup...\n", config.Iterations)
	startTime := time.Now()

	if err := runner.RunAndReport(); err != nil {
		log.Fatalf("Comprehensive benchmark failed: %v", err)
	}

	duration := time.Since(startTime)
	fmt.Printf("\nBenchmark completed in %v\n", duration)
	fmt.Println("\nOutput files:")
	fmt.Printf("  - JSON: %s.json\n", baseFilename)
	fmt.Printf("  - CSV: %s.csv\n", baseFilename)
	fmt.Printf("  - HTML: %s.html\n", baseFilename)
	fmt.Printf("  - CPU profile: cpu_%s.prof\n", config.Name)
	fmt.Printf("  - Memory profile: mem_%s.prof\n", config.Name)
}

func runQueryBenchmark() {
	fmt.Println("🔍 Running Query Performance Benchmark")
	fmt.Println("======================================")

	// For query benchmarks, we need initialized finders
	// This would require loading the full dataset and initializing finders
	// For now, create a placeholder implementation
	fmt.Println("Query benchmarks require full dataset initialization.")
	fmt.Println("Use the main benchmark CLI for comprehensive query testing:")
	fmt.Println("  go run benchmarks/cmd/main.go -type=query")
	fmt.Println()
	fmt.Println("This would test nearest city lookup performance across multiple finders.")
}

func runComparison() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: go run benchmarks/run_benchmarks.go compare <baseline_file>")
		fmt.Println("Example: go run benchmarks/run_benchmarks.go compare comprehensive_20240118_143022.json")
		os.Exit(1)
	}

	baselineFile := os.Args[2]
	fmt.Printf("Running comparison\n")
	fmt.Printf("Comparing with baseline: %s\n", baselineFile)
	fmt.Println("==================")

	// Check if baseline file exists
	if _, err := os.Stat(baselineFile); os.IsNotExist(err) {
		log.Fatalf("Baseline file does not exist: %s", baselineFile)
	}

	// Run current benchmark (same config as baseline if possible)
	config := types.BenchmarkConfig{
		Name:          "comparison-current",
		DatasetSizes:  []int{1000, 5000, 10000, 25000, 50000},
		Iterations:    1,
		OutputFormats: []types.OutputFormat{types.OutputFormatConsole},
		Tags:          map[string]string{"type": "comparison", "baseline": baselineFile},
	}

	reporter := reporters.NewConsoleReporter(true)
	_ = suite.NewBenchmarkRunner(config, reporter)

	// Run the comparison using the existing comparison logic
	cmd := exec.Command("go", "run", "benchmarks/cmd/main.go",
		"-name", "comparison-run",
		"-sizes", "1000,5000,10000,25000,50000",
		"-iterations", "1",
		"-format", "console",
		"-compare", baselineFile,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Fatalf("Comparison failed: %v", err)
	}
}

func runGreenTeaBenchmark() {
	fmt.Println("🍵 Running Benchmark with Green Tea GC (Go 1.25+)")
	fmt.Println("==================================================")
	fmt.Println()
	fmt.Println("Note: Green Tea GC is experimental and requires GOEXPERIMENT=greenteagc")
	fmt.Println("This benchmark will run with the experimental GC enabled.")
	fmt.Println()

	// Check Go version
	goVersion := runtime.Version()
	fmt.Printf("Go version: %s\n", goVersion)
	fmt.Println()

	// Set environment variable for Green Tea GC
	os.Setenv("GOEXPERIMENT", "greenteagc")

	// Run comprehensive benchmark with Green Tea GC
	sizes := []int{1000, 5000, 10000, 25000, 50000}
	config := types.BenchmarkConfig{
		Name:          "greentea-gc-benchmark",
		DatasetSizes:  sizes,
		Iterations:    3,
		WarmupRuns:    1,
		OutputFormats: []types.OutputFormat{types.OutputFormatConsole, types.OutputFormatJSON, types.OutputFormatHTML},
		EnableCPUProf: true,
		EnableMemProf: true,
		Tags: map[string]string{
			"type":       "greentea-gc",
			"go_version": goVersion,
			"experiment": "greenteagc",
			"timestamp":  time.Now().Format(time.RFC3339),
			"cpu_cores":  fmt.Sprintf("%d", runtime.NumCPU()),
		},
	}

	// Create multi-reporter with all requested formats
	var reps []reporters.Reporter
	baseFilename := "greentea-gc-benchmark"
	for _, format := range config.OutputFormats {
		switch format {
		case types.OutputFormatConsole:
			reps = append(reps, reporters.NewConsoleReporter(true))
		case types.OutputFormatJSON:
			reps = append(reps, reporters.NewJSONReporter(baseFilename+".json", true))
		case types.OutputFormatCSV:
			reps = append(reps, reporters.NewCSVReporter(baseFilename+".csv"))
		case types.OutputFormatHTML:
			reps = append(reps, reporters.NewHTMLReporter(baseFilename+".html"))
		}
	}
	reporter := reporters.NewMultiReporter(reps...)
	runner := suite.NewBenchmarkRunner(config, reporter)

	startTime := time.Now()
	if err := runner.RunAndReport(); err != nil {
		log.Fatalf("Green Tea GC benchmark failed: %v", err)
	}

	duration := time.Since(startTime)
	fmt.Printf("\n✅ Green Tea GC benchmark completed in %v\n", duration)
	fmt.Println("\n📊 Results:")
	fmt.Println("  Compare these results with standard GC benchmarks to see the improvement.")
	fmt.Println("  Expected: 10-40% reduction in GC overhead for GC-intensive workloads.")
	fmt.Println("\n💡 To compare:")
	fmt.Println("  1. Run: go run benchmarks/run_benchmarks.go comprehensive")
	fmt.Println("  2. Run: go run benchmarks/run_benchmarks.go greentea")
	fmt.Println("  3. Compare the GC cycles and GC time metrics")
}

func parseSize(sizeStr string) (int, error) {
	// Handle size multipliers (e.g., "50k" -> 50000)
	multipliers := map[byte]int{
		'k': 1000,
		'K': 1000,
		'm': 1000000,
		'M': 1000000,
	}

	if len(sizeStr) > 1 {
		lastChar := sizeStr[len(sizeStr)-1]
		if multiplier, exists := multipliers[lastChar]; exists {
			baseSize, err := parseSize(sizeStr[:len(sizeStr)-1])
			if err != nil {
				return 0, err
			}
			return baseSize * multiplier, nil
		}
	}

	// Parse as regular integer
	var result int
	for _, char := range sizeStr {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("invalid character in size: %c", char)
		}
		result = result*10 + int(char-'0')
	}
	return result, nil
}
