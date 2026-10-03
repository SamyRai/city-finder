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
- They are single-client numbers, **not latency under load**. Nothing here
  covers p99 at a given request rate. That takes a `cmd/loadgen` sweep against
  a deployed server (see [benchmarking.md](benchmarking.md#load-testing)), and
  no such sweep is recorded on this page yet.
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
  - `name.Options.FuzzyMaxNames` (default 25M keys) bounds it against unmeasured
    scales.
  - Per-query work is capped by `name.Options.FuzzyMaxCandidates` (default 4M posting
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

## Index footprint reduction (2026-10, after v1.3.1)

Five changes target memory and index files, and none changes an exact
answer:
- one shared city table instead of a second copy in the name index;
- postal entries reduced to the served fields, in sorted columns;
- the GeoNames loader no longer pins source lines;
- interned country codes, and primary-name bytes shared with `City.Name`;
- delta-varint fuzzy posting lists.

**Measured** with `cmd/memreport` on a 4M-city synthetic, production-shaped
dataset (~30 % of production rows), on one machine (Linux, 4 vCPU, Go
1.26.0). It is the same dataset for both sides, and each `measure` ran in a
fresh process:

| Metric (4M cities) | Before (`4b71da4`) | After | Change |
|---|---|---|---|
| Live heap after warm start | 1,233.8 MB | 724.1 MB | **−41 %** |
| … with the fuzzy index built | 1,496.5 MB | 856.0 MB | **−43 %** |
| Fuzzy index alone | 262.6 MB | 131.9 MB | **−50 %** |
| Live heap after a cold build | 1,510.0 MB | 733.7 MB | **−51 %** |
| Index files: S2 / name / postal | 118.3 / 142.8 / 24.6 MB | 118.3 / 55.7 / 8.6 MB | **−36 %** total |
| Cold build: init time / peak RSS | 58.8 s / 5,421 MB | 46.3 s / 4,580 MB | −21 % / −16 % |
| Warm start: init time | 10.4 s | 9.9 s | −5 % |
| Warm start + query transcript: peak RSS | 4,334 MB | 3,134 MB | −28 % |

**Answers.** Seeded transcripts of 42,469 queries were compared line by line:
nearest (both ranks, with admin attribution), exact and alternate-name
lookups, typos, prefixes and postal codes.
- Nearest, prefix and postal answers are byte-identical.
- Name answers differ only for ambiguous typos. 380 queries, none of them an
  indexed key, and none flipping between hit and miss. There the old code
  picked a random winner per process. The new code picks the closest match,
  then the alphabetical first. Two new-code processes produce byte-identical
  transcripts (all 42,469 lines).

**At production scale** these are estimates, to be confirmed with a heap
profile (`PPROF_ADDR`) on the real dump.
- The removed duplicates scale with rows (~72 B per city for the second city
  copy; ~250–300 B per postal entry).
- The ~4.5 GB production heap should land around 2.6–2.9 GB.
- The ~1.2 GB fuzzy index should land around 0.6 GB.
- The name file (531 MB) should lose most of its 546 MB-raw city table.

### Boot path follow-ups (same dataset and machine)

Later changes target the boot path:
- streaming index decode;
- the loader no longer pinning lines through alternate names and admin
  codes, and its row slice sized exactly;
- top-K selection with a heap;
- the server returning boot garbage to the OS (`debug.FreeOSMemory`) once
  init finishes.

| Metric (4M cities) | Before | After |
|---|---|---|
| Warm start: init time / peak RSS | 11.1–11.3 s / 2,915–3,084 MB | 10.0–10.3 s / 2,781–2,882 MB |
| Warm start: RSS right after init → after the release | — | 2,415 → **870 MB** |
| Cold build: init time / peak RSS | 46.3 s / 4,580 MB | 36.4 s / 4,453 MB |
| Cold build: RSS right after init → after the release | — | 3,491 → **1,341 MB** |
| Fuzzy index (exactly sized posting buffer) | 131.9 MB | 111.9 MB |

The warm rows are two interleaved before/after pairs, with transcripts
byte-identical across all four runs. The cold row and the release rows are
single runs. Without the release, the resident set stays near the boot peak
until the background scavenger returns the pages, which takes minutes. A
container's memory metric then reports the boot peak, not the working set.

### Index compression level (decision record)

The index files use zstd `SpeedFastest` (`lib/indexfile`). Measured on the
same 4M-city files (synthetic data; real GeoNames text may compress
differently):

| Level | S2 + name + postal | Encode (cold build only) | Decode |
|---|---|---|---|
| fastest (used) | 182.6 MB | 1.4 s | 0.85 / 0.40 / 0.07 s |
| default | 168.7 MB | 2.7 s | about the same |
| better | 157.0 MB (−14 %) | 5.2 s | about the same |
| best | 141.0 MB (−23 %) | 20.2 s | up to 2× slower |

Decode speed barely depends on the level, so a warm boot gains nothing from
a higher one. A cold build pays the encode on the path where a fresh pod
waits to become ready: "better" would add ~10 % to the 4M cold boot
(~12 s at production scale) to save 14 % disk. Disk is the cheaper resource,
so the level stays `fastest`. Re-evaluate if index files ever ship over a
network.

## Memory sizing

Figures below are the v1.3 production measurements. See the reduction above
for the post-v1.3.1 changes, which lower all of them.

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
