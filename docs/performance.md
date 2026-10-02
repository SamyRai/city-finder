# Performance

Measured production-scale behaviour, per version. How these numbers are
produced, and what they can and cannot support, is in
[benchmarking.md](benchmarking.md). The in-repo micro-benchmarks are for
regression detection on one machine. They are **not** the source of any
number on this page.

## Method and scope

All figures come from the full GeoNames dump: 13.47M cities, 17.7M unique
names (≈18.7M (country, name) keys). They were measured on one machine (Apple
Silicon, darwin/arm64) with an in-process harness:

- `initializer.Initialize` timed for boot;
- **nearest**: 10k uniformly random global coordinates (mostly ocean);
- **name / postal**: 1k real keys sampled from the loaded indexes;
- peak RSS via `/usr/bin/time -l`.

Consequences:

- These are **in-process** latencies of the library call. HTTP framing,
  middleware, the network and queueing come on top (see the `app` benchmarks
  for the in-process HTTP share).
- They are single-client numbers, **not latency under load**. No committed
  open-model load test exists yet, so no claim here covers p99 at a given
  request rate.
- One machine, single runs per pass. Treat differences under ~10 % between
  columns as unresolved.

## Results

| Metric | v1.3 (2026-10-02) | v1.2 | v1.1 | pre-v1.0 (v1 indexes) |
|---|---|---|---|---|
| Warm start (indexes on disk) | — | **23.3 s** (concurrent decode; includes the deserialize-time flatten sort) | 19.4–20.9 s | 43–61 s |
| Heap after warm start (post-GC) | — | **4.49 GB** (flattened name index) | 5.7 GB | 6.3–7.5 GB |
| Cold rebuild (datasets present) | — | **2 m 26 s, peak RSS 9.25 GB** | ~3 min, ≈12.6 GB peak | — |
| Fuzzy n-gram build | — | **29 s, background at boot** | ~30–90 s, in the first typo request | disabled at this scale |
| `FindNearestCity` rank=distance | **p50 9.4 µs, p99 78 µs** (pooled query objects) | p50 11.9 µs, p99 103 µs | p50 10 µs, p99 ~76–95 µs | p50 20 µs, p99 177–619 µs |
| `FindNearestCity` rank=population, ocean points | **1.0–9.5 s** (n=5, top-K-anchored disc) | 8.5–14.3 s | ~10 s | n/a |
| `FindNearestCity` rank=population, land | ms-class | ms-class | 0.3–40 ms | n/a |
| `CityByName` exact | — | p50 ~3 µs, max ~13 µs | p50 0.33 µs, p99 1.8 µs | p50 0.67–9 µs |
| `CityByName` fuzzy (1–2-edit typos) | — | p50 5.1 ms, p99 101 ms, max 614 ms; 1000/1000 resolved | p50 7–21 ms; 1000/1000 resolved | disabled |
| `PrefixNames` (autocomplete, 3-char prefixes) | — | p50 10.5 µs, p99 94 µs | n/a | n/a |
| `CityByPostalCode` | — | p50 0.79 µs (141 real keys) | p50 0.4 µs | p50 0.5 µs |
| Index files (name / S2 / postal) | — | **531 MB / 279 MB / 26 MB** | 559 / 280–293 / 26–28 MB | 1.6 GB / 519 MB / 98 MB |

"—" means the row was not re-measured for that version; the previous
column still applies. The ocean population-rank row is n=5 points; read it as
a range, not a distribution.

## What drives these numbers

- **Distance rank** is an `s2.ClosestEdgeQuery` with `MaxResults(1)`, which
  prunes instead of collecting a result per indexed point. Results are checked
  against a brute-force great-circle oracle (`s2_oracle_test.go`). The
  ShapeIndex is built eagerly at startup, so the first query pays no
  construction. Since v1.3 the query objects are recycled through a per-finder
  `sync.Pool`.
- **Population rank** is exact over the whole dataset: the gravity score
  `population / (d² + 1)` is maximized via a radius that escalates from 10 km
  until no city outside it can beat the in-radius best. A top-4096 population
  table bounds that search, and the v1.3 top-K-anchored disc removes the
  terminal full-sphere scan for certified queries. Ocean points stay
  multi-second because the cost is dominated by the winning city's own
  distance. Concurrent scans are gated at `min(GOMAXPROCS, 8)`, and a
  saturated gate returns `503` + `Retry-After: 1` rather than queueing. A scan
  can transiently allocate several hundred MB.
- **Name index** (v1.2) is flattened: per-country sorted names with int32 CSR
  postings over one distinct-city table, mirroring the on-disk v2 layout.
  Measured trade-off at full scale: heap −1.2 GB (5.7 → 4.49 GB), ~38 s extra
  on cold build, ~3 s on warm start, and exact lookups move from a 0.33 µs
  hash-map hit to a ~3 µs binary search. Zero allocations either way.
- **Fuzzy matching** (edit distance ≤ 2) uses a q-gram inverted index with a
  length filter and banded Levenshtein verification. The server builds it in
  the background right after boot (`WarmFuzzy`). Until it lands (~30–90 s at
  this scale, state exported as `fuzzy_build_state` on `/metrics`), typo
  lookups return exact-only results. No request ever pays the build.
  - `name.FuzzyMaxNames` (default 25M keys) bounds it against unmeasured
    scales.
  - Per-query work is capped by `name.FuzzyMaxCandidates` (default 4M posting
    entries). A capped query returns the matches verified so far, is never
    cached as complete, and increments `fuzzy_budget_trips_total`.
  - Results are cached (10k entries, 1 h TTL). Eviction is O(1) FIFO since the
    2026-10 review; the former full-map scan per insert at capacity cost
    ~54 % of a cache-churning typo lookup (interleaved A/B, n=8, 100k-name
    fixture).
  - Completeness boundary: very short names (≤ 1 rune at distance 1, ≤ 4 runes
    at distance 2) can be missed. Exact matches are always found first.
  - Measured budget trade-off: [design/index-format-v2.md](design/index-format-v2.md).
- **Warm start** decodes the three indexes concurrently. Name decode dominates
  (~14 s), and S2 and postal decode overlap inside its window.

## Memory sizing

- A cold start on an empty volume peaks around 9–9.3 GB RSS while building
  indexes. A warm boot alone also peaks ≈ 9 GB (concurrent decode).
- The steady-state live heap is ~4.5 GB after GC. The fuzzy index adds
  ~1.2 GB resident once built.
- RSS depends on workload and `GOGC`/`GOMEMLIMIT`. Population-rank scans add
  transient allocation, bounded by the gate.
- The Helm defaults (10 Gi request / 14 Gi limit) cover this with headroom.
  Re-check memory and the startup-probe budget whenever the index format
  changes.

## History

- [sprint/baseline-v1.md](sprint/baseline-v1.md): the pre-v1.0 anchor pass
  and the per-sprint re-measurements.
- [benchmarking.md §9](benchmarking.md#9-cross-engine-comparison-2026-10-02-v120):
  the cross-engine comparison (S2 vs cKDTree, Redis GEO, R-tree, brute force).
