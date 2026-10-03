# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **A cached fuzzy result hid cities added later.** After `AddCity`, a
  typo whose result was already cached kept returning the old candidates
  for the full one-hour TTL. Cache entries now carry a generation that
  `AddCity` bumps. This also makes empty results safe to cache.
- **Restart loop after an OOM kill.** The init lock was a pid file, and the
  server runs as PID 1 in its container, so a restarted container found its
  own pid in the leftover file and refused to start. The lock is now a
  `flock`, which the kernel releases when the holder dies.
- **Metrics and access log recorded errors as 200.** Router 404/405s and
  handler errors were logged with the default status, because fiber writes
  the error response after the middleware chain unwinds. They are now
  logged with their real status, and unrouted requests are labelled
  `(unrouted)` instead of `/`. Handler panics are now counted as 500s.
- **A population batch could 503 itself.** On hosts with more than 8 cores
  a single batch fanned out wider than the population gate. Batches with
  population points now cap their fan-out at the gate size.
- Dataset zips that ship a `readme.txt` next to the data file (GeoNames
  postal archives) were rejected. Extraction now skips readme entries and
  directories; downloaded and extracted files are fsynced before they are
  renamed into place.
- A single network blip during the cold-boot dataset download failed
  startup. Connection errors, dropped transfers, 5xx and 429 are now
  retried up to 3 attempts with backoff (2 s, then 4 s). Other 4xx
  responses and client timeouts still fail at once.
- Index files were renamed into place without an fsync, so a power loss
  could leave an empty or partial index under the final name. Writes are
  now fsynced, and the directory is synced after the rename.
- `NearestPlace` with a NaN, infinite or past-the-pole point now returns
  `coordinates.ErrInvalidCoordinate`. Before, a population query escalated
  to the full-sphere scan (seconds at production scale). The HTTP layer
  already validated input.
- Config files: content after the JSON object is now a load error (it was
  silently ignored). Unknown keys are logged at startup. Startup fails with
  a clear message when a dataset or index file name is missing, or when two
  keys name the same file.
- `PrefixNames` on a name index loaded detached from its city table could
  return a match with a nil city.
- Bad `lat`/`lon` parameters are no longer logged per request (a log
  amplification path).

- **Homonym resolution was nondeterministic.** The concurrent name-index
  build merged worker results in completion order, so for a name shared by
  several cities `FindCityByName` could return a different city after each
  rebuild (100 winner changes across 6 identical builds of a 200k fixture).
  It now always resolves in load order, as documented.
- **Ambiguous typos resolved randomly.** When a typo matched several names,
  the winner depended on map-iteration order in the fuzzy index (different
  per process), and a distance-2 match could beat a distance-1 match.
  Candidates are now ordered by edit distance, then name.
- A cold-built server kept every source line of the GeoNames dump in
  memory: the loader stored `City.Name`/`Country` as substrings of the line.
  Name is now cloned and Country interned (test: 24 MB → < 4 MB retained
  for 20k rows with 1 KiB of alternate names).

- Fuzzy result cache eviction is O(1) FIFO instead of two full-map scans
  per insert at capacity (10k entries). Under a churning distinct-typo
  workload, the scan cost more than the search it cached:
  `BenchmarkCityByNameFuzzy/cache-churn` 641 µs → 296 µs (−54 %, p=0.000,
  n=8 interleaved, 4-vCPU cloud host); cache hits unchanged; +2 allocs/op.
  Same eviction order (oldest first, expired first).
- `name.Finder.AddCity` no longer writes into the caller's `AltNames`
  backing array (`append(AltNames, Name)` aliased spare capacity). Pinned
  by a regression test.
- Helm chart `appVersion` was still `1.0.0`. Because the image tag defaults
  to it, a default install deployed the v1.0 server. Now `1.3.1` (chart
  `1.0.1`). CI fails when it drifts from the latest CHANGELOG release, and
  the release job fails on a tag that does not match.
- Benchmarks (full review against the measurement protocol now in
  `docs/benchmarking.md`): `b.Fatalf` called from `RunParallel` workers;
  workers sweeping keys in lockstep; `AddCity`/`AddPostalCode` measuring
  growth or overwrite of one key instead of inserts; a `b.TempDir()` per
  iteration in every serialize benchmark; `Sprintf` inside timed loops;
  `MemoryUsage` ignoring b.N and timing fixture generation; ignored errors
  and results; legacy `b.N` loops migrated to `b.Loop`; nearest queries
  cycling three hot points (now 4096 seeded global points on a
  land-clustered world); a fuzzy benchmark mixing cache misses, evictions
  and hits (now `cache-churn` / `cache-hit`); duplicated-point S2 fixtures.
  Redundant benchmarks removed (`BuildIndexConcurrent`,
  `ConcurrentOperations`, `MemLiveLoad_TestData`, legacy `NearestPlace`).
- `make profile`, `make build-greentea` and `make bench*` no longer exist
  in broken form (`go run -cpuprofile` is not a flag; Green Tea is the
  default GC since Go 1.26).

### Added

- `cmd/memreport`: a synthetic GeoNames generator plus an end-to-end
  measurement of heap, peak RSS, index file sizes and init time, with an
  answer transcript for before/after comparison.
- Fuzz targets for the three index deserializers.
- `cmd/server/app`: the single owner of the production HTTP stack (fiber
  config, recover, access log, metrics, routes), used by `main` and the
  HTTP benchmarks. HTTP benchmarks now run `core` and `production`
  variants: the old ones measured a bare `fiber.New()` without ETag,
  recovery, access log or metrics.
- `PPROF_ADDR`: an opt-in `net/http/pprof` listener on its own address and
  mux, never the API port. Used for profiling a live workload and for
  representative PGO profiles (`make build-pgo`).
- `benchmarks/bench.sh` (`env` / `run` / `ab` / `smoke`) with Makefile
  wrappers. It compiles once, pins one toolchain for both A/B sides,
  interleaves rounds, records the environment, and uses a pinned benchstat.
  CI runs `bench.sh smoke` (every benchmark once, correctness only).
- New benchmarks: `NearestDistance` (land-clustered world, random global
  queries), `NearestDistanceScaling/N=1K…1M`, `NearestDistanceParallel`.
- `cmd/loadgen` (`make loadtest`): an open-model load generator. Requests
  arrive at a constant rate independent of responses, latency is measured
  from the intended send time (coordinated-omission correct), and a rate
  sweep reports offered vs achieved throughput, errors, drops, 404s and
  p50–p99.9 per step, stopping past the knee. Seeded workloads: `nearest`,
  `nearest-admin`, `nearest-population`, `coordinates`, `postal`, `mixed`.

### Removed

- The `benchmarks/` Go harness (`run_benchmarks.go`, `cmd`, `suite`,
  `reporters`, `profilers`, `types`). It ran a 10-row fixture under
  "1K–250K scaling" labels and its `greentea` mode set `GOEXPERIMENT` at run
  time, which has no effect on a compiled binary.

### Changed

- **Index footprint.** Measured with `cmd/memreport` on a 4M-city synthetic
  GeoNames dataset (answer transcripts byte-identical for nearest, prefix
  and postal lookups):

  | | before | after |
  |---|---|---|
  | heap after warm boot | 1234 MB | 724 MB (−41%) |
  | heap with fuzzy index | 1497 MB | 856 MB (−43%) |
  | fuzzy index | 263 MB | 132 MB (−50%) |
  | heap after cold build | 1510 MB | 734 MB (−51%) |
  | index files | 286 MB | 183 MB (−36%) |
  | cold init | 58.8 s | 46.3 s |

  The changes behind these numbers:
  - The name index shares the S2 index's city table instead of holding a
    second copy (name format v3).
  - The postal index keeps only the fields a lookup returns (postal format
    v4).
  - Fuzzy posting lists are delta-varint encoded.
  - Cities no longer pin their source lines.
  - Country codes are interned.

  Old index files still load and are rewritten in the new format on first
  boot, with no rebuild. See `docs/performance.md`.
- Index files are decoded by streaming (file → zstd → gob) instead of
  decompressing whole-file buffers. The framing now lives in one package,
  `lib/indexfile`, instead of three copies. S2 decode is 15% faster with
  16% less allocation. At 4M cities, warm-boot peak RSS dropped by 130–200 MB
  and warm init by ~1 s (two interleaved pairs).
- Population-ranked queries skip the exact distance for top-K cities that
  cannot change the bound (−70% on land queries), and reuse a pooled
  query. The top-K table is selected with a bounded heap instead of
  sorting every populated city (1M cities: 314 ms → 7 ms per boot).
- Fuzzy search dedups with a pooled bitset and runs the length filter
  first (typo lookups −41%, worst-case walk −31%, 2.3 MB → 6 KB allocated).
  Cache hits no longer format a key (−19%, zero allocations).
- ETag is computed by the server's own middleware. It produces the same
  tags as fiber's, without rebuilding a CRC table per response
  (`/autocomplete` and `/postalCode` −11–12%).
- Batch requests stop executing points after the first failing one; the
  reported failure is unchanged. `/healthz` serves fixed bytes.
- The cold-build loader no longer pins each row's source line through
  alternate names and admin codes. The row slice is sized from the file's
  line count instead of a bytes-per-line guess that overshot by ~8%.
- The server returns boot garbage to the OS once init finishes. On the 4M
  dataset, RSS right after init goes from 2.4 GB to 0.87 GB (warm) and from
  3.5 GB to 1.3 GB (cold), instead of sitting near the boot peak until the
  scavenger catches up. `cmd/memreport` reports both values.
- **Library API:** the exported `postalCode.Finder.PostalCode` map is gone
  (use `Len()`).

- Documentation consolidated under `docs/` (`api`, `configuration`,
  `deployment`, `architecture`, `performance`, `benchmarking`). The README
  is rewritten as an entry point. `article.md` moved to
  `docs/design/narrative.md`. Corrected readings of the 2026-10-02 M2
  baseline are listed in `docs/benchmarking.md` §8. That baseline was
  captured with Go 1.27.1 while the module builds with 1.26, and is marked
  historical.

- `/metrics` scrapes no longer race on a shared `runtime/metrics` sample
  slice (the heap-gauge read is per-scrape now); pinned by a concurrent-
  scrape test under `-race`. Found by the 2026-10-02 correctness review;
  introduced with the gauges in 1.3.1. Gauge values unchanged.

## [1.3.1] - 2026-10-02

### Fixed

- `TestFuzzyDisabledOverThreshold` no longer fails on shared CI runners
  whose scheduler deschedules the timing goroutine (one observed 59.6 ms
  outlier against the fixed 50 ms bound; the disabled typo-miss path takes
  no lock at all, so the outlier was starvation, not blocking). The
  no-stall gate now measures a control run under identical conditions and
  asserts worst-alongside-misses ≤ max(50 ms, 5× control) — quiet machines
  keep the same sensitivity, and a real regression (the miss path grabbing
  the write lock or doing per-miss work) still fails by an order of
  magnitude. The one-shot "fast nil" bound widens 100 ms → 1 s for the
  same reason.

### Added

- Missing benchmarks, one per public query path: `PrefixNames`
  (sparse/dense/miss on a 1M-name table), `NearestPlaceWithAdmin`
  (distance + land-population with admin attribution), metrics-registry
  `Render`/`ObserveRequest`, and HTTP round-trips for every route
  (`/nearest` ×3, `/nearest/batch`, `/coordinates`, `/autocomplete`,
  `/postalCode`, `/metrics`) via fiber `app.Test`.
- `/metrics` runtime gauges `go_goroutines` and `go_heap_alloc_bytes`,
  computed per scrape; the heap figure reads
  `/memory/classes/heap/objects:bytes` via `runtime/metrics`, so a scrape
  pays no stop-the-world.

### Changed

- Name-index benchmark fixtures use 100k–1M distinct synthetic names
  instead of copies of one name, which collapsed the name tables and the
  n-gram index to a handful of keys; exact/fuzzy/autocomplete lookups now
  measure real table spread. `BenchmarkMemoryUsage` keeps the index alive
  with `runtime.KeepAlive` (a blank assignment let the second GC reclaim
  it and report a near-zero retained heap).
- benchmarks/README.md gained the full benchmark inventory, a repeatable
  baseline (2026-10-02, Apple M2, Go 1.27.1, benchstat), fixture-vs-
  production caveats, and a trust assessment of the cross-engine
  comparison and of the in-process numbers. docs/sprint/baseline-v1.md
  gained the v1.3 validation section.
- README synced with the shipped surface: the `/metrics` bullet lists
  `fuzzy_build_state` and the runtime gauges, the v1.3 performance column
  is marked as the current re-measured baseline, and the memory-floor
  paragraph quotes the flattened 4.49 GB heap instead of the pre-v1.2
  figure.

## [1.3.0] - 2026-10-02

### Added

- `include_feature_classes` config key (default `""` = no filter): a
  comma-separated GeoNames feature-class allowlist (`"P"` = populated
  places only, `"P,A"` = places + admin divisions; valid letters
  A P H L R S T U V, invalid entries fail config load). Complements
  `exclude_admin_divisions`; applies at dataset load/index rebuild and is
  honored by both the initializer and `cmd/build-index`.
- Top-K-anchored disc for population-rank escalation: when the 250 km disc
  fails to certify, the best top-4096 candidate anchors a single
  challenger-radius disc instead of the 1250/6250 km tiers. Measured at
  full scale: ocean-class queries 8–14 s → 1–9.5 s (~2–3×; a 200k-city
  fixture shows ~12×, but at 13.47M cities the disc is dominated by the
  winner's own distance — the next lever is a merged comparator letting
  table candidates win without entering the disc, currently blocked by
  golang/geo's unexported EdgeQueryResult fields). Exactness unchanged:
  all brute-force oracles (ties, crowd-out, all-zero populations) pass
  unedited; the escalation ladder and terminal fallback remain.
- RankDistance query objects are pooled per finder (sync.Pool over the
  ClosestEdgeQuery + options): measured at prod scale, nearest p50
  11.8 → 9.4 µs (−20%), allocations −40% on the realistic benchmark.
- `POST /nearest/batch` now executes points concurrently (bounded by
  GOMAXPROCS; the population gate still caps the expensive class per
  point). A batch of far-from-land `rank=population` points that ran for
  minutes sequentially now completes in roughly its slowest single point.

## [1.2.0] - 2026-10-01

### Added

- `GET /autocomplete`: prefix search over the flattened name index —
  sorted matches, each name paired with its first city, limit 1-50
  (default 10), empty array on no match; no fuzzy-index dependency.
- `POST /nearest/batch`: one round trip for 1–100 lookups. Per-point
  `rank`/`include` with the same validation, error texts, and population
  concurrency gate as GET `/nearest` (both handlers now share one query
  core; GET behavior is byte-identical, existing tests unchanged).
  Response is a parallel `results` array of the GET response objects with
  `null` for not-found points.
- Optional `exclude_admin_divisions` config key (default `false`,
  byte-identical behavior when off): drops GeoNames feature-class-A rows
  (countries/states/provinces and their huge synthetic populations) at
  dataset load — they otherwise win mid-ocean `rank=population` queries.
  Applies when an index is (re)built; honored by both the initializer and
  `cmd/build-index` when a config is loaded. When enabled, admin-division
  names no longer resolve through `/coordinates`.
- `GET /metrics` (Prometheus text format, no new dependencies): request
  counters and latency histograms keyed by route pattern and status code,
  and a `fuzzy_budget_trips_total` counter scraped from the library's
  atomic trip counter. `routes.SetupRoutes` keeps its signature (no metrics
  surface — tests/embedded use); the server wires it via
  `SetupRoutesWithMetrics`.
- Boot lock (`.cityfinder-init.lock`) per datasets folder: two cold-booting
  processes previously wrote the same `<index>.gob.part` paths and truncated
  each other's in-flight writes; a living lock owner now fails the second
  boot fast with a clear message, and a lock from a provably dead process is
  stolen.
- Admission control on the HTTP surface: concurrent `rank=population`
  queries pass a CPU-sized semaphore (GOMAXPROCS, capped at 8) — saturation
  sheds with `503` + `Retry-After: 1` instead of queueing unbounded
  full-index scans. The `/coordinates` name parameter is capped at 200
  runes (bounds the quadratic query-gram dedup). fasthttp `Concurrency` is
  capped at 1024 (default 256k).

### Changed

- The in-memory exact name index is flattened: per-country sorted name
  slices + int32 CSR postings over one distinct-city pointer table,
  replacing the nested `map[country]map[name][]*city.City`. Measured at
  full scale (13.47M cities / 18.7M names): name-finder heap −1.43 GiB
  (−29%) — the entire avoidable nested-map overhead; the flat structure
  itself is ~0.48 GiB at payload scale. On-disk v2 format unchanged
  (compat proven against an index serialized pre-refactor); pointer-sharing
  semantics preserved; cold build pays ~38 s for the flatten sort and the
  warm start ~3 s (deserialize-time sort); end-to-end exact lookups move
  from a 0.33 µs hash-map hit to ~3 µs binary search at prod (the measured
  latency tradeoff; zero allocations, sub-µs at 1M-key microbench scale).
- The fuzzy (n-gram) index builds in a background goroutine: the first
  fuzzy query no longer runs the ~30–90 s build synchronously in-request —
  it returns the same fast exact-only degradation concurrent queries always
  had, and the server triggers the build at startup via `WarmFuzzy()`
  (idempotent, non-blocking, also exposed for library users).
  `CityByName` short-circuits before any fuzzy work when the country has
  no indexed names, and empty non-truncated fuzzy results are no longer
  cached (a name added after a miss is visible to the next identical
  query, honoring the documented overflow guarantee).
- Dataset extraction is crash-atomic: the zip entry streams through
  `<file>.part` and is renamed into place only after a complete copy,
  mirroring the download path.
- Warm boots (all three indexes present) skip the dataset ensure entirely;
  a rebuild that needs the raw files re-ensures them on demand. Archives
  are deleted after successful extraction (re-downloadable cache pinning
  ~442 MB per volume; best-effort, never fails the boot).
- The three index deserializations run concurrently on warm starts — name
  decode (~14 s at prod) bounds the path instead of summing with S2 (~4 s)
  and postal (~1 s). Peak RSS rises by the smaller indexes' transient
  decode buffers (~0.5–1 GB), within the chart's request/limit headroom.
- `rank=population` escalation uses a top-4096 population table to tighten
  the anytime bound: cities outside the table are bounded by the 4096th
  largest population instead of the single global max, and the table's own
  cities are scored exactly. This removes the terminal full-sphere scan
  and its ~300 MB transient slice per query; measured at prod, ocean-class
  queries remain multi-second (8–14 s in the v1.2 baseline — dominated by
  the wide escalation discs, not the terminal scan), so the operative
  mitigations are the HTTP concurrency gate above and the
  `exclude_admin_divisions` data knob. Exactness is unchanged — the
  brute-force oracle tests still pin the winner, and the terminal unbounded
  iteration remains the fallback. The table is derived in memory (no
  on-disk format change).
- `cmd/build-index` honors `CONFIG_PATH` (same resolution as the server via
  the new `config.LoadFromEnv()`) and writes its prod outputs to the
  config's index-file keys through the new `(*Config).IndexFilePaths()` —
  the same resolution the initializer reads, so pre-built indexes can no
  longer be silently ignored under a non-default config.
- The postal CSV loader tolerates rows with a wrong field count
  (`FieldsPerRecord = -1` + the existing `< 12` skip) and counts them in
  their own summary line; one malformed row in the ~14M-row file no longer
  aborts the whole load (and with it server startup). `LazyQuotes` is
  deliberately not set: it would silently swallow the file tail after an
  unterminated leading quote, so quote corruption still fails loudly.

### Removed

- Dead `s2` config knobs `min_level`/`max_level`/`max_cells` and the
  never-read `*config.S2` parameter of `coordinates.BuildIndex` (breaking
  for library callers; the index was always built with ShapeIndex defaults
  — golang/geo exposes no such tuning). Configs containing the old keys
  still load (unknown JSON keys are ignored).
- `util.FindProjectRoot` — dead since config loading became CWD-relative;
  the one test consumer moved to an in-tree helper.

### Fixed

- A crash or full disk mid-extraction could leave a truncated dataset at
  its final path; the next boot accepted it as complete and baked the
  partial data into all three serialized indexes.
- The postal loader compared EOF by error string and locked the CSV field
  count to the first row's 12 fields.
- `nearest` bounds-checked the winning edge id only against the upper bound
  (negative ids unreachable today, now guarded like the sibling paths).

## [1.1.0] - 2026-10-01

### Added

- Administrative-region attribution: `/nearest?...&include=admin` adds
  `admin1_code`, `admin1_name` (when the optional `admin1CodesASCII.txt`
  dataset is loaded — auto-downloaded on cold start; codes-only otherwise),
  and `admin2_code` (when present) to the response. Attribution follows the
  winning city (nearest or population-ranked); near boundaries it is the
  city's region, not polygon containment. Default responses are unchanged.
- `name.FuzzyMaxCandidates` (default 4,000,000): caps per-query n-gram
  posting-list work so degenerate short queries cannot walk unbounded lists;
  capped queries return verified-so-far matches (best-effort, never cached as
  complete) and `name.FuzzyBudgetTrips()` counts trips. The default keeps
  completeness on real typo queries (0/1000 truncated at prod scale in two
  samples); see `docs/design/index-format-v2.md` for the measured budget
  frontier.

### Changed

- **Index format v3 for the S2 and postal indexes** (name stays v2): both
  payloads are now zstd-framed and the S2 index carries per-city admin1/admin2
  id arrays plus code tables. On-disk: S2 521 MB → ~280–293 MB (−46%), postal
  98 MB → ~26–28 MB (−73%); total index footprint 1.18 GB → ~0.87 GB. v1.0's
  v2-format files are rejected on first boot and rebuilt automatically.
- Config loading is project-root-independent: an absolute `CONFIG_PATH` no
  longer requires a `go.mod` walk-up (the container image drops its marker
  file); relative config paths resolve against the CWD and relative
  `datasets_folder` against the config file's directory (default repo layout
  unchanged).
- `cmd/build-index prod` resolves dataset filenames and S2 settings from
  `config.json` (same source as the initializer) instead of hardcoded names
  that did not match the initializer's output; logged fallback to the legacy
  literals when no config is found.
- Warm start: 19.4–20.9 s total (v1.0: 20.5 s) — s2 v3 decode is faster
  (read+decompress+gob ≈ 3.9 s vs ~6 s) while postal pays decompression
  (~0.5 s → ~1.2 s) and the admin arrays add ~108 MB decoded; heap after
  warm start 5.7 GB (v1.0: 5.5 GB).

### Fixed

- N-gram index integer bounds are now guarded: names ≥ 65,536 runes are
  length-capped (unfindable-by-fuzzy either way; exact lookups unaffected)
  and CSR posting totals beyond int32 fail the build with a descriptive
  error instead of silently corrupting the index.
- Documentation actualization pass: 16 drift findings corrected across
  README (compiling usage example, dataset filenames, 600 s startup-probe
  budget), benchmarks/README (fixture-bound scope, non-existent env vars and
  config-file loader removed), Helm values comments, article.md, and
  `.tool-versions`.

### Removed

- Dependencies: `cheggaaa/pb/v3` (progress bar → stdlib milestone logging in
  the S2 build) and `agnivade/levenshtein` (overflow scan and test reference
  now use the in-house banded checker and a brute-force DP test reference).
  Direct dependencies 6 → 4; go.sum 51 → 39 lines. Stale v1-era tracked
  artifacts (18 MB benchmark log, empty pprof) untracked and ignored.

## [1.0.0] - 2026-10-01

First tagged release. Performance numbers below are measured on the full
GeoNames dump (13.47M cities, 17.7M distinct names) on Apple Silicon; see the
README Performance section for methodology.

### Added

- Population data: `city.City` carries `Population` (GeoNames field 14).
- Population-weighted nearest search: `/nearest?rank=distance|population`
  (default `distance`, byte-compatible with previous responses). The
  `population` mode returns the exact gravity-score winner
  (population / (distance_km² + 1)) over all indexed cities via an
  escalating-radius search under an anytime bound, and includes `Population`
  in the response. Land queries resolve in microseconds-to-milliseconds;
  sparse ocean points can take seconds.
- Fuzzy name matching at full scale: the BK-tree is replaced by a q-gram
  inverted index with length filter and a banded allocation-free Levenshtein
  verifier. Fuzzy search now works on the full 17.7M-name production index
  (distance-2 typo queries: p50 ~21 ms; previously disabled above 2M names,
  where the BK-tree cost 650 ms per query). Completeness boundary for very
  short names (≤1 rune at distance 1, ≤4 runes at distance 2) is documented
  in `docs/design/index-format-v2.md`.
- OpenAPI 3.0.3 specification for all four endpoints (`docs/openapi.yaml`).
- Deployment artifacts: distroless multi-arch container image
  (`Dockerfile`), Helm chart (`helm/city-finder`), and a tag-driven GitHub
  Actions release pipeline publishing to GHCR with a GitHub release built
  from this changelog. Container and Helm workflows documented in the README
  Deployment section.
- `/healthz` endpoint, `distance_km` in the `/nearest` response, and
  graceful shutdown (10s drain) on SIGINT/SIGTERM.
- CI runs the Go gate (build, vet, gofmt, race tests, staticcheck,
  govulncheck, `go mod tidy -diff`) plus a Helm lint/kubeconform chart gate.

### Changed

- **Index format v2** (breaking for on-disk indexes; old files rebuild
  automatically): the name index serializes each distinct city once plus
  int32 reference ids, zstd-framed, instead of one struct copy per name
  reference. At production scale the name index shrank 1.6 GB → 559 MB and
  warm start 43–61 s → 20.5 s; decoded heap dropped ~2 GB. `city.City`
  gained `Population`, so all three index headers bumped v1 → v2 together;
  v1 files are rejected and rebuilt from source data on first boot.
- Warm-start memory: interned country strings collapse decoded duplication;
  per-reference struct copies are gone entirely (pointer sharing survives
  serialization round trips).

### Fixed

- Postal-code loader no longer silently indexes rows whose latitude or
  longitude cannot be parsed: such rows used to land at (0,0) and surface
  Null-Island coordinates from `/postalCode` lookups. They are now skipped,
  with one summary log line at end of load.
- Server rejects non-finite (`NaN`/`±Inf`) and out-of-range `lat`/`lon`
  query parameters with 400 instead of leaking them into the spatial index.
- Initializer downloads are verified and hardened (HTTP status checked,
  atomic rename, one re-download when a pre-existing archive fails to
  extract); warm starts skip dataset parsing entirely when pre-built
  indexes are present.
- Fuzzy name search made safe and bounded at production scale (deadlock,
  tree-build race and unbounded cache growth fixed).
- Versioned, atomically written index files; the initializer rebuilds
  indexes over corruption instead of failing startup.

### Performance

- Warm start (all indexes on disk): **20.5 s** (was 43–61 s); name index
  decode alone 36–54 s → 14 s.
- Name index file: **559 MB** zstd-framed v2 (was 1.6 GB).
- Decoded heap after warm start: **5.5 GB** (was 6.3–7.5 GB).
- Nearest queries (rank=distance): p50 ~10 µs at prod scale.
- Fuzzy typo queries at 17.7M names: p50 7–21 ms (previously disabled at
  this scale).
