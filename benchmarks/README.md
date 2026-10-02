# CityFinder Benchmark Suite

A benchmarking and profiling suite for the CityFinder project that measures performance, memory usage, and provides analysis of index building operations.

> **Scope caveat:** this suite loads only the 10-row `testdata/allCountries.txt` fixture; the configured dataset sizes are caps over that same fixture, not real 1K–250K datasets. Real scaling measurements live in `lib/finder/coordinates/s2_bench_test.go` and the `lib/finder/name` benchmarks.

## Features

- Multiple output formats: console, JSON, CSV, HTML
- CPU, memory, and execution tracing
- Performance metrics: throughput, latency, memory usage, GC statistics
- Scaling analysis: test performance across different dataset sizes
- Comparison tools: compare benchmark results across runs
- CLI with sensible defaults

## 📊 Metrics Collected

### Performance Metrics
- **Throughput**: Operations per second for each benchmark phase
- **Latency**: Time taken for each operation
- **Total Duration**: End-to-end benchmark time
- **Scaling Performance**: How performance changes with dataset size

### Memory Metrics
- **Peak Memory Usage**: Maximum memory consumed
- **Memory Allocation**: Total bytes allocated
- **GC Statistics**: Number of GC cycles and pause times
- **Memory Efficiency**: Memory usage per operation

### System Information
- **Go Version**: Runtime version information
- **CPU Cores**: Available processing cores
- **OS Information**: Operating system details

## 🛠️ Quick Start

### Run Quick Benchmark
```bash
go run benchmarks/run_benchmarks.go quick
```

### Run Scaling Analysis
```bash
go run benchmarks/run_benchmarks.go scaling
```

### Run with Profiling
```bash
go run benchmarks/run_benchmarks.go profile 50000
```

### Run Full Suite
```bash
go run benchmarks/run_benchmarks.go comprehensive
```

## 📈 Commands

| Command | Description | Example |
|---------|-------------|---------|
| `quick` | Fast performance check with 1K, 10K, 50K cities | `go run benchmarks/run_benchmarks.go quick` |
| `scaling` | Scaling analysis across multiple sizes | `go run benchmarks/run_benchmarks.go scaling` |
| `profile` | CPU/memory profiling for specific dataset size | `go run benchmarks/run_benchmarks.go profile 50000` |
| `comprehensive` | Full suite with multiple iterations and all output formats | `go run benchmarks/run_benchmarks.go comprehensive` |
| `query` | Placeholder — prints instructions only, runs no benchmark | `go run benchmarks/run_benchmarks.go query` |
| `greentea` | Comprehensive run with the experimental Green Tea GC (`GOEXPERIMENT=greenteagc`, Go 1.25+) | `go run benchmarks/run_benchmarks.go greentea` |
| `compare` | Compare current results with baseline | `go run benchmarks/run_benchmarks.go compare baseline.json` |

## 🎯 Advanced Usage

### Custom Dataset Sizes
```bash
# Run with specific sizes (plain integers, comma-separated)
go run benchmarks/cmd/main.go -sizes "1000,5000,25000,100000" -name "custom-test"
```

Sizes are parsed with `strconv.Atoi`; anything that is not a plain integer (e.g. `1k`) fails to parse and is silently dropped, falling back to the default sizes if nothing parses.

### Multiple Iterations
```bash
go run benchmarks/cmd/main.go -iterations 5 -warmup 2 -name "statistical-test"
```

### Profiling Options
```bash
# Enable all profiling
go run benchmarks/cmd/main.go -cpuprofile -memprofile -trace -name "full-profile"

# Analyze profiles
go tool pprof cpu_full-profile.prof
go tool pprof -http=:8080 mem_full-profile.prof
```

### Output Formats
```bash
# Multiple formats
go run benchmarks/cmd/main.go -format multi -output "my-benchmark"

# This creates:
# - my-benchmark.json
# - my-benchmark.csv
# - my-benchmark.html
# - Console output
```

### Component Selection
```bash
# Skip specific index building operations
go run benchmarks/cmd/main.go -skip-s2 -skip-postal -name "name-only"
```

## 📊 Output Formats

### Console Output
Real-time progress with formatted tables and summary statistics.

### JSON Output
Structured data for programmatic analysis and CI/CD integration.
```json
{
  "config": {...},
  "timestamp": "2024-01-18T14:30:22Z",
  "duration": 1500000000,
  "memory_usage": {...},
  "operations": [...],
  "system_info": {...}
}
```

### CSV Output
Spreadsheet-compatible format for data analysis.
```csv
Timestamp,BenchmarkName,Operation,Duration(ms),MemoryDelta(MB),ItemsProcessed,Throughput,Status
2024-01-18T14:30:22Z,scaling-test,Data Loading,123.45,10.2,50000,406143,completed
```

### HTML Output
Web reports with charts.

## 🔍 Analyzing Results

### Performance Analysis
```bash
# View scaling performance
go run benchmarks/run_benchmarks.go scaling

# Compare with previous run
go run benchmarks/run_benchmarks.go compare previous_results.json
```

### Profile Analysis
```bash
# CPU profiling
go tool pprof cpu_benchmark.prof
# Interactive web view
go tool pprof -http=:8080 cpu_benchmark.prof

# Memory profiling
go tool pprof -alloc_objects mem_benchmark.prof
go tool pprof -http=:8080 mem_benchmark.prof
```

### Memory Leak Detection
```bash
# Check for memory leaks
go tool pprof -alloc_space mem_benchmark.prof
```

## 🏗️ Architecture

```
benchmarks/
├── cmd/           # CLI interface
├── suite/         # Benchmark execution engine
├── reporters/     # Output format handlers
├── profilers/     # Performance profiling
├── types/         # Data structures
├── run_benchmarks.go  # Main entry point
└── README.md      # This file
```

### Key Components

- Suite runner: orchestrates benchmark execution
- Profilers: collect CPU, memory, and trace data
- Reporters: format and output results
- Types: define data structures for benchmarks

## 📋 Benchmark Phases

1. **Data Loading**: Load cities and postal codes from files
2. **S2 Index Building**: Construct spatial index for coordinate queries
3. **Name Index Building**: Build fuzzy search index for city names
4. **Postal Code Index**: Build postal code lookup index

## 🎯 Best Practices

### For Development
- Use `quick` command for fast iteration during development
- Use `profile` command to identify performance bottlenecks
- Use `scaling` command to understand performance characteristics

### For CI/CD
- Use JSON output for automated analysis
- Set up baseline comparisons to detect regressions
- Use multiple iterations for statistical analysis

### For Performance Analysis
- Enable profiling when investigating issues
- Use HTML output for detailed analysis reports
- Compare results across different hardware/configurations

## 🔧 Configuration

### Environment Variables
```bash
# Add JSON output on top of the console reporter (only "json" is
# honored; other format names in this variable are ignored)
export BENCHMARK_FORMATS="json"
```

## 🐛 Troubleshooting

### Common Issues

**High Memory Usage**
- Check if memory profiling is enabled
- Reduce dataset sizes for initial testing
- Use streaming options for large datasets

**Slow Performance**
- Ensure sufficient CPU cores available
- Check for background processes
- Use warmup runs for stable measurements

**Profile File Issues**
- Ensure write permissions in working directory
- Check available disk space
- Use `go tool pprof` commands for analysis

## 🤝 Contributing

### Adding New Benchmarks
1. Define benchmark configuration in `types/`
2. Implement benchmark logic in `suite/`
3. Add CLI support in `cmd/`
4. Update output formatters if needed

### Adding New Output Formats
1. Implement the `Reporter` interface
2. Add format handling in CLI
3. Update documentation

### Improving Profiling
1. Extend `profilers/` package
2. Add new metrics collection
3. Update result types

## 📈 Performance Tips

- **Use multiple iterations** for statistical significance
- **Enable warmup runs** for stable performance measurements
- **Profile memory usage** when optimizing for memory efficiency
- **Compare results** across different runs to detect regressions
- **Use appropriate dataset sizes** for your use case

## Integration

The benchmark suite integrates with:
- Go testing framework (standard `go test` benchmarks)
- CI/CD pipelines (JSON output for automated analysis)
- Performance monitoring (metrics collection)

---

For more information, see the individual package documentation or run `go run benchmarks/run_benchmarks.go help`.
## Cross-engine comparison (2026-10-02, v1.2.0) — re-run on golang/geo bumps

Method: full dump (13,465,092 loader-valid rows; raw parses see +7,112
country-less undersea/international rows the loader deliberately drops),
one shared 10k-query set (random global points, seed 42), in-process
latency, per-engine winner cross-validation against scipy cKDTree.

| Engine | Build | Memory | p50 | p90 | p99 |
|---|---|---|---|---|---|
| cityFinder v1.2 (Go, S2, full product) | 12.8 s | 4.5 GB resident | 11.8 µs | 37 µs | 141 µs |
| raw golang/geo S2 ClosestEdgeQuery | 3.7 s | ~5.5 GB peak | 5.6 µs | 14 µs | 29 µs |
| scipy cKDTree (Python/C, 3D unit vectors) | 4.7 s | ~1.4 GB | 9.2 µs | 18 µs | 66 µs |
| Redis 8 GEO (geohash, socket) | 237 s | 941 MB | 1.49 ms | 715 ms | 1.36 s |
| rtreego (Go R-tree) | 3 m 20 s | ~6.1 GB | 32 ms | 86 ms | — (8.4% nil) |
| brute force (Go) | — | 0.45 GB | 523 ms | 562 ms | 682 ms |

Findings to re-check when the geo pin moves: (1) this golang/geo version
only prunes at `MaxResults(1)` — a pruning-semantics change would alter
the latency profile silently; (2) winner agreement with cKDTree was exact
(0/1000 disagreements on identical row scope) — keep that as the oracle;
(3) Redis GEO rejects |lat| > 85.05° (Web-Mercator limit); rtreego
returns nil for 8.4% of queries at this scale (unusable). Harnesses were
throwaway (/tmp); the table above is the durable record.
