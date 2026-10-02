# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
