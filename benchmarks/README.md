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

## In-repo Go benchmarks — inventory, baseline, and what to trust

Repeatable `go test` benchmarks live next to the code they measure (the
suite in this directory is a separate, fixture-bound tool — see the scope
caveat at the top). Run and compare with benchstat; never compare single
runs:

```bash
go test -run '^$' -bench 'BenchmarkCityByName$' -benchmem -count=10 \
  ./lib/finder/name | tee /tmp/now.txt
benchstat /tmp/baseline.txt /tmp/now.txt
```

Tip: `go test` merges the test binary's stderr into its own stdout, and
`BuildIndex` logs each call — for parseable output compile the binary and
separate the streams: `go test -c -o /tmp/pkg.test ./lib/finder/name &&
/tmp/pkg.test -test.run '^$' -test.bench . -test.benchmem -test.count=10
> out.txt 2>/dev/null`.

### Inventory

| Benchmark | Measures | Fixture | Notes |
|---|---|---|---|
| coordinates `NearestPlaceDistinctPoints` | nearest, rank=distance core | 100k distinct sphere points | the realistic distance-rank benchmark; legacy `NearestPlace` keeps the duplicated-point fixture for historical comparability |
| coordinates `NearestPlaceWithAdmin` (added 2026-10-02) | include=admin attribution read | 100k distinct + admin codes/names | distance and land-population sub-runs |
| coordinates `NearestByPopulationOceanQuery` | population-rank escalation worst case | 200k clustered synthetic world | mid-ocean vs populated; exercises the v1.3 anchored disc |
| coordinates `BuildIndex` / `SerializeIndex` / `DeserializeIndex` / `MemoryUsage` | index lifecycle | 1K–1M | BuildIndex logs per call (that log is part of the public op) |
| name `CityByNameExactTail` + `Parallel` | exact lookup, full 1M-key sweep | 1M distinct names / 20 countries | per-query p50/p99/p99.9 + RWMutex contention check |
| name `CityByName` / `CityByNameFuzzy` | exact hit; distance-1 fuzzy resolve | 100k distinct names | fixtures repaired 2026-10-02 (were 100k copies of ONE name — see below) |
| name `PrefixNames` (added 2026-10-02) | autocomplete prefix walk | 1M-name single-country table | sparse / dense-cap / miss sub-runs |
| name `NGramSearch` / `EnsureFuzzyBuilt` | budget-capped d2 walk; one-time n-gram build | 100k names sharing a 9-gram prefix | deliberately maximal skew: worst case for the candidate budget |
| name `AddCity` | post-build overflow insert | — | |
| postalCode `CityByPostalCode` (+ add/serialize) | postal hit | 1k–10k entries | |
| metrics `Render` / `ObserveRequest` (added 2026-10-02) | /metrics scrape serialization; per-request observation | populated registry (6 routes × 4 statuses) | |
| routes `BenchmarkHTTP*` (added 2026-10-02) | full fiber `app.Test` round-trip per route | 10k-city app | /nearest ×3 (distance/admin/population), /nearest/batch (10 pts), /coordinates, /autocomplete, /postalCode, /metrics |

### Baseline — 2026-10-02, Apple M2 (8 cores, 24 GB), darwin/arm64, Go 1.27.1

Tree = v1.3.0 plus this documentation/benchmark commit (no production
query-path changes; the only production file touched is the /metrics
handler). Sequential runs on a quiet machine, `-count=10` for the fast
rows (benchstat mean ± noise), fixed counts as noted.

Query path:

| Benchmark | Result | Allocs |
|---|---|---|
| nearest distance, 100k distinct points | 3.7 µs ± 38% | 978 B, 45 |
| nearest distance + admin attribution | 2.33 µs ± 1% | 978 B, 45 |
| nearest population (land, uniform world) | 141 µs ± 1% | 4.8 KiB, 172 |
| nearest population (mid-ocean, clustered world) | 4.8 ms ± 7% | 2.07 MiB, 49.7k |
| exact name hit, 100k distinct sweep | 241 ns ± 16% | 0 |
| exact name hit, 1M-key sweep (mean / p50 / p99 / p99.9) | 700 / 667 / 1291 / 7958 ns | 0 |
| exact name hit, 1M parallel | 167 ns per goroutine-op | 0 |
| fuzzy d1 typo resolve, 100k names | 140 µs ± 11% | 46 KiB, 55 |
| fuzzy d2 walk, 100k maximal-skew names (budget-capped) | 22.9 ms ± 12% | 2.33 MiB, 569 |
| autocomplete prefix, 1M-name table (sparse / dense / miss) | 358 / 217 / 150 ns | 240 B, 1 |
| postal hit, 1k entries | 176 ns ± 27% | 77 B, 2 |
| /metrics Render (populated) / ObserveRequest | 42 µs ± 31% / 47 ns ± 10% | 36.6 KiB, 214 / 0 |
| HTTP /nearest (distance / admin / population) | 14.1 / 14.3 / 107 µs | 12.5–15.6 KiB, 77–166 |
| HTTP /nearest/batch (10 points, parallel) | 60.4 µs ± 4% | 33 KiB, 496 |
| HTTP /coordinates, /autocomplete, /postalCode, /metrics | 12.2 / 16.6 / 11.2 / 15.7 µs | 11–16 KiB |

Lifecycle:

| Benchmark | Result |
|---|---|
| name BuildIndex, 1M distinct names | 1.95 s, 768 MB transient, 123 MB retained |
| name BuildIndex, 100K | 324 ms, 12.4 MB retained |
| name n-gram (fuzzy) build, 100k names | 139 ms |
| name AddCity (overflow path) | 166 ns |
| name Serialize/Deserialize, 10k names | 4.3 / 4.8 ms |
| S2 BuildIndex, 1M points | 423 ms, 1.52 GiB transient, 93 MB retained |
| S2 Serialize/Deserialize, 100k points | 22.8 / 72.2 ms |
| postal add / serialize / deserialize, 10k | 55 ns / 8.9 / 11.7 ms |

### What these numbers are for

The fixtures are synthetic (uniform sphere points, syllable names, US-style
postal grids) and deliberately smaller than production (13.47M GeoNames
rows clustered on land, real name skew, 35M name/alternate keys). Use the
in-repo benchmarks for REGRESSION DETECTION and relative comparison on one
machine — not as absolute production claims. Production-scale numbers come
from the passes recorded in docs/sprint/baseline-v1.md and the cross-engine
table above. Known fixture-vs-production deltas measured on 2026-10-02:
synthetic exact-lookup names are longer than real city names (700 ns mean
here vs 334 ns p50 prod on v1.0, same machine class); the d2 fuzzy
benchmark is a maximal-skew worst case (all names share a 9-gram prefix)
where production's mixed-typo p50 at 17.7M names is 3–5 ms; the ocean
fixture's anchored-disc win (~12×) does not transfer to prod (2–3×), where
the disc is dominated by the winner's own distance. The HTTP benchmarks
measure fiber's in-process `app.Test` round-trip — no TCP, no TLS, no
kernel network stack — so they bound the application's own overhead, not
network-exposed latency.

## Trust assessment (2026-10-02 review)

**Cross-engine table — trust it for the conclusions drawn, not as a
universal ranking.** Trustable: one shared query set, one shared dataset
row scope (the original 40.8% winner disagreement was root-caused to a
row-scope mismatch and re-measured to 0/1000 disagreement against
scipy cKDTree on identical scope), in-process latency on one host, and
per-engine build/memory reported alongside latency. Caveats: single host
(Apple M2, 24 GB, no CPU pinning, background load possible); engines
measured as a user would run them, not hand-tuned (Redis over a socket
with defaults, rtreego unwarmed); one workload only — nearest-neighbor
point queries, no range/polygon/batch workloads; the harnesses were
throwaway /tmp scripts, so the table and method notes above are the
durable record and cannot be re-run bit-exact. What the table supports:
at 13.47M points on one machine, an in-process S2 index answers nearest
queries ~2 orders of magnitude faster than networked Redis GEO and ~3
orders faster than an R-tree that degrades at this N, and its winners are
provably correct against an independent engine. What it does NOT support:
"S2 beats X everywhere" — different hardware, network placement, or
workloads can reorder the engines.

**In-repo baseline — trust it for regression detection.** Same-machine
sequential runs, benchstat means with noise over ≥10 counts for the fast
rows, `-benchmem` throughout; measurement defects found and fixed while
establishing it (GC-invalidated retained-heap metrics now use
`runtime.KeepAlive`; degenerate single-name name-index fixtures replaced
with distinct-name ones; the legacy duplicated-point nearest fixture is
kept only for historical comparability and superseded by
`NearestPlaceDistinctPoints`). Residual limits: laptop thermals and
background load (±38% worst row); synthetic fixture skew as itemized
above; `BuildIndex` rows include the library's per-call log line.

**Known-broken measurements, kept visible rather than hidden:** rtreego
p99 (8.4% nil results at 13.5M — engine unusable at that scale); Redis
GEO polar queries (capability gap, excluded from its distribution); the
run_benchmarks.go suite in this directory (10-row fixture — its "scaling"
and size caps are over the fixture, not real datasets).

## Committed baselines

The raw `benchstat`-parseable outputs behind the 2026-10-02 baseline tables
above are committed under `baselines/2026-10-02-m2/` (captured with the
separate-streams workflow described earlier). Use them as the comparison
side of a regression check:

```bash
benchstat benchmarks/baselines/2026-10-02-m2/bench-name-fast.txt /tmp/now.txt
```

Regenerate them with the commands from the inventory section; commit a new
dated directory alongside (never overwrite an old baseline) when the
machine, Go version, or fixture-generating code changes.
