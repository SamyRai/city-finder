# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

- Nearest-city queries bounded with `MaxResults(1)` and the S2 index built
  eagerly at startup, so the first query after boot pays no construction
  cost.
- Name index: stopped pinning the loader slice and interned decoded
  strings; reduced logging/progress overhead during index builds.

### Added

- CI runs the Go gate (build, vet, gofmt, race tests, staticcheck,
  govulncheck, `go mod tidy -diff`) on both Gitea and GitHub Actions.
- `/healthz` endpoint, `distance_km` in the `/nearest` response, and
  graceful shutdown (10s drain) on SIGINT/SIGTERM.

> Note: index format v2 and population-weighted nearest are landing via
> sibling lanes — final entries are assembled at release.

## [1.0.0] - TBD

- TBD at tag time.
