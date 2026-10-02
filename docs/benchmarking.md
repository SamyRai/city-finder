# Benchmarking

How performance is measured in this repository, and the rules every
benchmark and every performance claim must follow. The measured results
themselves live in [performance.md](performance.md).

> The question to ask of any benchmark is not "is it correct?" but **"what
> exact claim would a 20 % improvement here let me make — and what else could
> produce the same number?"**

## 1. Three questions for every benchmark and every perf PR

| Question | What it covers | Where this repo enforces it |
|---|---|---|
| **Mechanically valid?** | timers, dead-code elimination, state carried between iterations, races in the harness | §3 rules; `bench.sh smoke` in CI |
| **Statistically credible?** | repetitions, noise, interleaving, uncertainty, multiple testing | §5 protocol; `bench.sh ab` |
| **Representative?** | data, concurrency, caches, middleware, cold vs warm, scale | §2 layers; per-benchmark doc comments; §7 inventory |

A benchmark can pass the first two perfectly and still fail the third. That
is the failure mode people underestimate most. A `-37.84 % ± 0.23 %, p=0.000`
result proves nothing if it measured the wrong thing.

Before writing a benchmark, write the experiment down:

> *If I change X, under workload Y, metric Z should improve, while A/B/C stay
> acceptable.*

## 2. Layers: what each kind of measurement can claim

```
production pass ─► HTTP app (core / production) ─► lib micro-benchmarks ─► pprof / trace
 (full GeoNames)      (cmd/server/app)               (lib/...)
```

| Layer | Where | Can claim | Cannot claim |
|---|---|---|---|
| **Production pass** | one-off harness over the full 13.47M-row dump: `initializer.Initialize` timed in-process, 10k random global points, 1k real name/postal keys | per-version latency distributions, boot time, heap, RSS at real scale on that machine | network latency, behaviour under load |
| **HTTP app** | `cmd/server/app` benchmarks, `core` and `production` variants | per-request in-process cost of handlers + middleware (production − core = middleware cost) | TCP/TLS/kernel cost, throughput under load, tail latency at saturation |
| **Micro** | `lib/...` benchmarks | relative change of one operation on synthetic fixtures, on one machine | production latency, absolute numbers, anything about another machine |
| **Profiles** | `-cpuprofile`, `PPROF_ADDR`, `pprof -base` | *where* time and allocations go | *whether* a change is faster (a benchmark answers that) |

The rules that follow from this table:

- A micro-benchmark win is validated at the layer the claim is about. "/nearest
  is faster" needs the HTTP or production pass, not only `BenchmarkNearestDistance`.
- Throughput is not latency. `RunParallel` ns/op and HTTP in-process numbers
  are **not** service latency. Latency under load needs an open-model load
  test (constant arrival rate, so a stalled server is not hidden by a client
  that politely waits — *coordinated omission*). Report offered load,
  achieved throughput, errors and p50/p95/p99 together, as a curve up to the
  saturation knee. No such load test is committed yet. Until one is, nothing
  in this repo supports a claim about latency under load.
- Name experiments after what they include. `BenchmarkNGramSearch` measures a
  cache-bypassed, maximal-skew d2 walk. It is not "fuzzy search performance".

## 3. Rules for writing benchmarks in this repo

Every rule below exists because the codebase once violated it. Rules 1–9 are
mechanical, 10–15 are about the workload.

1. **Use `for b.Loop()`** for serial benchmarks (Go ≥ 1.24). It keeps the
   compiler from eliminating the measured call, times only the loop, and runs
   the setup above it exactly once. Do not mix it with `b.N`.
2. **Use `b.RunParallel`** for concurrent benchmarks. Never call
   `b.Fatal`/`FailNow` inside a worker — use `b.Errorf` and `return`. Never
   call `StartTimer`/`StopTimer`/`ResetTimer` inside workers. Give each worker
   its own starting offset: workers sweeping the same keys in lockstep share
   warm cache lines and understate the cost of independent requests.
3. **Assert on the result** (`== nil → b.Fatal`). This catches fixture
   regressions (a benchmark silently measuring 404s or misses), and it keeps
   the result alive.
4. **Nothing in the timed loop that the operation does not do.** Precompute
   query strings and keys: a `fmt.Sprintf` per iteration adds an allocation
   that the operation does not pay. Build `httptest` requests per call only
   because their bodies are consumable.
5. **No filesystem setup in the timed loop.** Use one `b.TempDir()` and one
   fixed path. A `b.TempDir()` per iteration times directory creation and
   leaves b.N files behind.
6. **Silence library logs, not log calls.** Index builds and (de)serialization
   log every call. Benchmarks redirect `log` output to `io.Discard`
   (`silenceBuildLogs`, `silenceIndexLogs`, ...). The calls still format, so
   their CPU stays in the measurement, and the output stays parseable by
   benchstat.
7. **Report custom metrics after the loop**, with units that say what they are
   (`retained-MB`, `alloc-MB/op`, `p99-ns`). A memory benchmark must keep its
   object alive (`runtime.KeepAlive`). Its GCs and `ReadMemStats` calls run
   off the clock.
8. **Allocation metrics explain cost; they are not the goal.** `0 allocs/op`
   is not "cheap", and removing an allocation must still show up in ns/op or
   at a higher layer. `testing.AllocsPerRun` forces `GOMAXPROCS=1` and does
   one warm-up call, so it says nothing about allocation under concurrency.
9. **Correctness runs separately.** `go test -race` (CI) for correctness;
   never benchmark a `-race` or coverage binary (2–20× CPU, 5–10× memory).
10. **Ask what iteration N inherits from iteration N−1.** If the answer is not
    "exactly the state I intend to measure", reset it off the clock or redesign.
    Examples fixed in this repo: `AddCity`/`AddPostalCode` re-adding one key
    (measured a growing list or an overwrite, not an insert); the fuzzy cache
    silently turning a "typo lookup" benchmark into a mix of misses, evictions
    and hits.
11. **Cold and warm are different benchmarks.** Never average them. The fuzzy
    lookup has `cache-churn` and `cache-hit` sub-benchmarks. Deserialization
    benchmarks state that they read through a warm OS page cache (decode CPU,
    not disk I/O). True process cold start (package init, first `sync.Once`,
    page cache) needs a new process per sample — a loop cannot reproduce it.
12. **Exclude setup only when production does not repeat it.** A reused
    finder, a pooled query object and a prebuilt fuzzy index are excluded
    because the server reuses them. The HTTP `production` variant includes the
    whole middleware chain because every request pays it.
13. **Representative inputs, not one constant.** Query sets are seeded and
    large (4096 uniformly random global points; 1M-key stride sweeps). Cycling
    three fixed points measures a cache- and branch-predictor-friendly special
    case. The `continents` fixture (200k land-clustered cities, heavy-tailed
    populations) is the closest in-repo shape to GeoNames.
14. **Benchmark complexity as a curve.** `BenchmarkNearestDistanceScaling/N=…`,
    `BenchmarkBuildIndex/{1K…1M}`. A single N cannot show O(n) turning into
    O(n²). Read concurrent benchmarks as a `-cpu 1,2,4,8` curve.
15. **Rename a benchmark when its workload changes.** benchstat matches by
    name. Keeping a name while changing what it measures creates a silent
    apples-to-oranges comparison against every older result file.

## 4. Running benchmarks

`benchmarks/bench.sh` owns the protocol. The Makefile wraps it.

```bash
make bench-env                                   # what the experiment runs on
make bench PKG=./lib/finder/name BENCH='CityByName$'          # 10 samples, one tree
make bench-ab BASE=origin/main PKG=./lib/finder/name BENCH='CityByNameFuzzy'
make bench-smoke                                 # every benchmark once (CI does this)
```

What the script guarantees:

- **Pinned toolchain.** `GOTOOLCHAIN` is resolved once and exported, so both
  sides of an A/B build with the same Go even if the base ref's `go.mod`
  differs.
- **Compile once, run the binary.** `go test -c`, then run from the package
  directory. Compilation never lands in a sample, and library logs go to
  `stderr.log`, never into the result file.
- **Interleaved A/B.** Base and head alternate every round, and the order flips
  each round, so thermal or background drift hits both sides equally.
- **Environment recorded.** Every result directory gets `env.txt`: toolchain,
  `GOAMD64`/`GOARM64`, `GOEXPERIMENT`, `GOMAXPROCS`/`GOGC`/`GOMEMLIMIT` env,
  PGO profile, commit (+dirty), CPU model, core count, cgroup CPU quota,
  governor and load average. It also warns when the machine is busy. The
  metadata is deliberately *not* written into the result files: benchstat
  splits files whose configuration keys differ into separate tables, which
  would silently suppress the comparison.
- **benchstat pinned** (`golang.org/x/perf/cmd/benchstat`, overridable via
  `BENCHSTAT`).

Results land in `bench-out/` (gitignored).

### Profiling

```bash
# CPU profile of one benchmark. The profile covers the whole binary, fixture
# setup included; a long -benchtime makes the loop dominate it.
go test -run '^$' -bench 'BenchmarkNearestDistance$' -benchtime 10s \
    -cpuprofile cpu.pprof ./lib/finder/coordinates
go tool pprof -http=:8080 cpu.pprof

# Where did a regression's extra work appear? Diff two profiles.
go tool pprof -http=:8080 -base base.pprof head.pprof

# A live server under its real workload: opt-in, never on the API port
PPROF_ADDR=127.0.0.1:6060 ./nearestcityserver
curl -o cpu.pprof 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'
curl -o trace.out 'http://127.0.0.1:6060/debug/pprof/trace?seconds=5'
```

- A benchmark tells you *that* something changed; a profile diff tells you
  *where*. Profile again after every optimization, because the bottleneck moves.
- Profilers mislead too. Do not collect a CPU profile together with a precise
  memory profile, or block/mutex profiling together with a trace. If a profile
  contradicts what the code obviously does, validate it (another OS, `perf` on
  Linux, a trace, the benchmark delta) before optimizing its top frame.
- **PGO** (`make build-pgo`) needs a CPU profile of a *representative
  production workload* (`PPROF_ADDR` on a live server). It must never come
  from a micro-benchmark, which would teach the compiler that one loop is the
  whole program.

## 5. Statistics protocol

Decide all of this **before** looking at results:

| Setting | Default | Notes |
|---|---|---|
| Samples | 10 rounds | 20 when chasing a change under ~3 % |
| Sample length | `-benchtime 1s` | longer for µs-scale ops on noisy hosts |
| Significance | benchstat default (Mann–Whitney U, α = 0.05) | |
| Minimum relevant effect | state it up front | e.g. "≥ 5 % on /nearest p50 or it is not worth the complexity" |

- **Never re-run until p < 0.05.** If an experiment is inconclusive, record it
  as inconclusive: it means *this measurement system cannot resolve the
  change*, not that the change does nothing. Across many benchmarks, about 1
  in 20 will look significant by chance.
- **Statistical and practical significance are separate tests.** `-0.27 %,
  p=0.001` can be real and irrelevant. Gate on both: the difference is
  resolvable *and* larger than the minimum relevant effect.
- **A wide interval means no resolution, whatever the median says.** A
  benchmark whose own ± is near the effect size cannot decide anything.
  Statistics cannot rescue an unstable machine.
- **Interleave**, and keep raw result files (`bench-out/…`) with the PR when a
  performance claim is made.

What a machine can resolve:

| Expected effect | Requirement |
|---|---|
| > 10 % | an ordinary, quiet workstation; repeated samples |
| 3–10 % | 10+ interleaved samples + benchstat |
| 1–3 % | a dedicated, idle host (AC power, no thermal throttling), 20 interleaved samples |
| < 1 % | performance-lab conditions; also suspect code-layout/alignment effects — an unrelated edit can move a micro-benchmark by 1–2 % |

Shared CI runners and cloud containers sit at the bottom of this table. CI
therefore runs benchmarks only as a smoke test (§4) and never gates on their
numbers.

## 6. Toolchain, runtime and container configuration

These are part of the experiment.

- **The Go version is a variable.** Go 1.26 made the Green Tea GC the default.
  Go 1.27 brought size-specialized small allocations and a new `encoding/json`
  implementation. Comparing "old branch on 1.26" with "new branch on 1.27"
  measures the code change *plus* compiler, runtime, GC and stdlib changes.
  Benchmark a toolchain upgrade as its own experiment: same commit, two
  toolchains (`GOTOOLCHAIN=go1.26.x` vs `GOTOOLCHAIN=go1.27.x`, each building
  its own binary), interleaved.
- **`GOEXPERIMENT` is a build-time setting.** Setting it in a running process
  changes nothing. To compare GC implementations, build two binaries
  (`GOEXPERIMENT=nogreenteagc` opts out of Green Tea on Go 1.26+). The former
  `greentea` benchmark mode set it with `os.Setenv` at run time and compared a
  binary with itself.
- **`GOMAXPROCS` follows the container.** Since Go 1.25, `GOMAXPROCS` on Linux
  follows the cgroup CPU *limit* and updates when it changes. The Helm chart
  sets no CPU limit, so the runtime uses every node core. Pin `GOMAXPROCS`
  (chart `server.extraEnv`) for reproducible throughput. The population-rank
  gate is `min(GOMAXPROCS, 8)` and batch fan-out is `GOMAXPROCS`.
- **GC settings change the CPU/memory trade-off.** Benchmark under the
  production `GOGC`/`GOMEMLIMIT`/memory limit. `GOGC=off` makes any
  allocation-heavy change look free.

## 7. Inventory

All benchmarks live next to the code they measure and run with plain
`go test`. "Fixture" is the input. "Claims" is what a change in the number
supports.

| Benchmark | Measures | Fixture | Claims / caveats |
|---|---|---|---|
| coordinates `NearestDistance` | rank=distance nearest | 200k land-clustered `continents` world, 4096 uniform random global queries | the production methodology at 200k points; mostly ocean queries, like prod |
| coordinates `NearestDistanceScaling/N=1K…1M` | same query vs index size | N distinct uniform points | the *shape* of the cost curve |
| coordinates `NearestDistanceParallel` | concurrent distance queries, query-object pool | `continents`, per-worker offsets | aggregate throughput (ns/op = wall ÷ total ops); run with `-cpu` |
| coordinates `NearestWithAdmin/{distance,population}` | include=admin attribution | 100k distinct, admin codes, all populated | attribution overhead vs `NearestDistanceScaling/N=100K` |
| coordinates `NearestByPopulationOceanQuery/{mid-ocean,populated}` | population-rank escalation, two fixed points | `continents` | worst-case regression detection; fixture win does not transfer 1:1 to prod |
| coordinates `BuildIndex/{1K…1M}`, `Serialize/DeserializeIndex`, `MemoryUsage/{10K…1M}` | index lifecycle | N distinct points | deserialize = warm page cache; `retained-MB` vs `alloc-MB/op` |
| name `CityByName` | exact hit | 100k distinct names, stride sweep | |
| name `CityByNameExactTail` (+`Parallel`) | exact hit, full 1M-key sweep | 1M names / 20 countries | `p50-ns`/`p99-ns`/`p999-ns` from a first-touch sweep; Parallel = throughput + RWMutex contention |
| name `CityByNameFuzzy/{cache-churn,cache-hit}` | d1 typo through `CityByName` | 100k names, prebuilt n-grams | churn = distinct-typo steady state (search + insert + evict); hit = repeated typos |
| name `NGramSearch` | cache-bypassed d2 walk | 100k names sharing a 9-gram prefix | deliberately maximal skew; budget-capped worst case |
| name `EnsureFuzzyBuilt` | one n-gram build | 100k names | |
| name `PrefixNames/{sparse-hit,dense-hit,miss}` | autocomplete | 1M names, one country | |
| name `AddCity` | insert of a new city into the overflow | ≤ 4096-entry overflow | no production caller today |
| name `BuildIndex/{1K…1M}`, `Serialize/DeserializeIndex`, `MemoryUsage`, `ProcessBatch*` | index lifecycle | distinct names | includes BuildIndex's own trailing `runtime.GC()` |
| postalCode `CityByPostalCode`, `AddPostalCode`, `Serialize/DeserializeIndex` | postal lookup / lifecycle | 1k–10k distinct codes | |
| dataLoader `LoadGeoNamesCSV`, `MemLiveLoad_Synthetic100k` | dump parsing | 10-row fixture / 100k synthetic rows | warm page cache: parse cost, not I/O |
| metrics `Render`, `ObserveRequest` | /metrics serialization, per-request observation | populated registry | |
| app `HTTP*/{core,production}` | one in-process request per route | 10k-city app, seeded random paths | no TCP/TLS; production − core = middleware cost |

## 8. Baselines and corrections

Committed raw outputs live in `benchmarks/baselines/<date>-<host>/`. A new
baseline goes in a **new** dated directory (never overwrite one), captured on
a quiet dedicated machine:

```bash
benchmarks/bench.sh run -n 10 -o benchmarks/baselines/2026-MM-DD-<host> \
    ./lib/finder/coordinates ./lib/finder/name ./lib/finder/postalCode \
    ./cmd/server/metrics ./cmd/server/app
```

**`2026-10-02-m2` (Apple M2) — historical; do not use it as the comparison
side.**

- It was captured with Go **1.27.1**, while the module, CI and the image build
  with Go **1.26**. A comparison against it mixes code and toolchain changes.
  Its `bench-env.txt` also lacks the runtime configuration (`GOMAXPROCS`,
  `GOGC`, `GOEXPERIMENT`, commit).
- Most of its benchmarks were redesigned in the 2026-10 benchmark review
  (renamed or with changed workloads): `NearestPlace`,
  `NearestPlaceDistinctPoints`, `NearestPlaceWithAdmin`, `CityByNameFuzzy`,
  `AddCity`, `AddPostalCode`, `MemoryUsage`, all `HTTP*` routes, and the
  coordinates lifecycle fixtures (formerly duplicated points). Use `bench.sh ab`
  between two commits on one machine instead.

Readings of that baseline that were wrong and are corrected here:

| Earlier claim | Correction |
|---|---|
| "exact name hit, 1M parallel: 167 ns per goroutine-op" | `RunParallel` ns/op is wall-clock ÷ total ops: ~6M lookups/s aggregate across 8 workers, not a per-lookup latency |
| "nearest distance, 100k distinct: 3.7 µs ± 38 %" (and the README's "~3–5 µs" built on it) | ±38 % means that row had no resolution. In the same run, the strictly-more-work `WithAdmin` variant measured 2.33 µs ± 1 % — environmental drift or code layout, not a property of the code. The workload (three hot query points) was also unrepresentative |
| "fuzzy d1 typo resolve, 100k names: 140 µs" | one number mixing cache misses, O(n) evictions and hits. Now split into `cache-churn` / `cache-hit` |
| "name AddCity: 166 ns" | measured amortized append onto one ever-growing key, not an insert |
| `MemoryUsage` ns/op | the old shape ignored b.N and timed fixture generation and GCs; only its MB metrics meant anything |
| HTTP rows "bound the application's overhead" | they ran a bare `fiber.New()` without ETag, recovery, access log or metrics: the production stack was never measured. Now `production` vs `core` |

## 9. Cross-engine comparison (2026-10-02, v1.2.0)

Method: full dump (13,465,092 loader-valid rows; raw parses see +7,112
country-less undersea/international rows the loader deliberately drops), one
shared 10k-query set (random global points, seed 42), in-process latency,
per-engine winner cross-validation against scipy cKDTree. Single host (Apple
M2, 24 GB, no CPU pinning). The harnesses were throwaway scripts: this table
and these notes are the durable record and cannot be re-run bit-exact.

| Engine | Build | Memory | p50 | p90 | p99 |
|---|---|---|---|---|---|
| cityFinder v1.2 (Go, S2, full product) | 12.8 s | 4.5 GB resident | 11.8 µs | 37 µs | 141 µs |
| raw golang/geo S2 ClosestEdgeQuery | 3.7 s | ~5.5 GB peak | 5.6 µs | 14 µs | 29 µs |
| scipy cKDTree (Python/C, 3D unit vectors) | 4.7 s | ~1.4 GB | 9.2 µs | 18 µs | 66 µs |
| Redis 8 GEO (geohash, socket) | 237 s | 941 MB | 1.49 ms | 715 ms | 1.36 s |
| rtreego (Go R-tree) | 3 m 20 s | ~6.1 GB | 32 ms | 86 ms | — (8.4 % nil) |
| brute force (Go) | — | 0.45 GB | 523 ms | 562 ms | 682 ms |

What it supports: at 13.47M points on one machine, an in-process S2 index
answers nearest queries ~2 orders of magnitude faster than networked Redis GEO
and ~3 orders faster than an R-tree that degrades at this N. Its winners agree
exactly with an independent engine (0/1000 disagreements on identical row
scope; an earlier 40.8 % disagreement was root-caused to a row-scope
mismatch). What it does **not** support: "S2 beats X everywhere". Hardware,
network placement or workload (range, polygon, batch) can reorder the engines.
Engines ran as a user would run them, not hand-tuned. Redis GEO rejects
|lat| > 85.05° (polar queries excluded from its distribution), and rtreego
returns nil for 8.4 % of queries at this scale.

Re-check on every `golang/geo` bump: this version only prunes at
`MaxResults(1)`, so a change in pruning semantics would silently alter the
latency profile. Keep cKDTree agreement as the oracle.

## 10. What was removed and why

The former `benchmarks/` Go harness (`run_benchmarks.go`, `cmd/`, `suite/`,
`reporters/`, `profilers/`, `types/`) was deleted in the 2026-10 review. It
loaded the 10-row `testdata/allCountries.txt` for every "dataset size" and
reported the result as a 1K–250K "scaling analysis". It called 3 iterations
"statistical significance", compared single runs by percentage, and its
`greentea` mode set `GOEXPERIMENT` at run time, where it has no effect. It
produced precise-looking numbers for questions it never measured.
`benchmarks/bench.sh` and the `go test` benchmarks replace it.
