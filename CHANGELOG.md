# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
