# Sprint v1.0 lane briefs

Integration branch: `sprint/v1.0` (forked from `main` @ 92b38c5). Each lane
works in its own worktree on a lane branch forked from `sprint/v1.0`; the
lead verifies every lane first-hand before fast-forwarding it onto
`sprint/v1.0`. `datasets/` is gitignored — worktrees symlink it:
`ln -s ../../datasets <worktree>/datasets`.

Canonical gates (every lane, before handoff):
`go build ./... && go vet ./... && gofmt -l .` (empty) `&& go test -race ./...`
plus `staticcheck ./...`, `govulncheck ./...`, `go mod tidy -diff` (empty).
Prod-scale gates additionally run against the symlinked `datasets/`.

## Lane NAME-V2 — format v2, then fuzzy at scale

Owner: go-engineer. Branch: `lane/name-v2`.
Files owned (phase 1 — format hop, Days 2–5):
`lib/city/city.go`, `lib/dataLoader/cityCoordinate.go`, `lib/finder/name/**`,
`lib/finder/coordinates/s2.go`, `lib/finder/postalCode/postalCode.go`,
`docs/design/index-format-v2.md` (already written; amend if implementation
diverges). Shared read-only context: `lib/initializer/**` (do not edit —
its rebuild fallback must keep working unchanged).

Phase 1 deliverables per `docs/design/index-format-v2.md`: `City.Population
int32` (loader field 14), name payload v2 (distinct `[]city.City` +
`map[country]map[name][]int32` refs, pointer-table rehydration, no BK-tree
serialization), version bump 1→2 on all three headers, gates 1–4 from the
design note (round-trip incl. pointer-sharing semantics, v1-reject →
auto-rebuild, count validation, prod before/after).

Phase 2 (Days 6–8, same owner, same files): q-gram (n-gram) inverted fuzzy
index + length filter + Levenshtein verify replacing BK-tree search; retire or
massively raise `FuzzyMaxNames`. **Measured go/no-go at 1M names** (memory
footprint + p50) before committing the design; fallback if memory explodes:
keep the threshold, raise the default, document honestly. Target: fuzzy p50
< 50 ms at 17.7M names. May slip to v1.1 without blocking the release.

Out of scope: initializer rewrite, benchmarks framework, API changes.

## Lane QUALITY-TAIL — last open P2 + ship hygiene

Owner: go-engineer. Branch: `lane/quality-tail`. Parallel with NAME-V2;
disjoint files.
Files owned: `lib/dataLoader/zipCodes.go` + `lib/dataLoader/*_test.go` (new
tests file only — do not touch `cityCoordinate.go` or its tests),
`LICENSE` (MIT, copyright SamyRai/cityFinder contributors — match README's
claim), `CHANGELOG.md` (Keep a Changelog format, v1.0.0 entry),
`docs/openapi.yaml` (OpenAPI 3, four endpoints: /healthz, /nearest,
/coordinates, /postalCode — mirror `cmd/server/routes/routes.go` semantics
exactly, including 400/404 shapes).

Postal silent-zero fix: `LoadPostalCodes` ignores lat/lon parse errors
(zipCodes.go:55-56), indexing entries at (0,0). Policy: skip-and-log — a row
whose record[9]/record[10] fails ParseFloat (empty or non-numeric) is not
indexed; log a count summary at end (not per-row) at prod scale. Red test
first (malformed rows currently produce 0,0 entries), then green. Do not
change PostalCodeEntry layout (NAME-V2 owns the postal version bump).

Optional if time remains: one pprof session on the exact-name p99 tail
(124 µs) — read-only analysis, fix only if a cheap win shows; no fix without
a before/after measurement.

Out of scope: postal index format, admin fields enrichment, rate limiting.

## Lane WEIGHTED — population-weighted nearest

Owner: go-engineer. Branch: `lane/weighted`. Starts **after** NAME-V2 phase 1
lands on `sprint/v1.0` (needs `City.Population` + owns overlapping files).
Files owned: `lib/finder/coordinates/s2.go` (NearestPlace),
`lib/finder/finder.go`, `cmd/server/routes/routes.go`,
`lib/finder/coordinates/s2_oracle_test.go` + related tests.

`NearestPlace(lat, lon, rank)`: fetch k=16 nearest candidates
(`MaxResults(16)`), then either return nearest (rank=distance, default,
byte-for-byte backward compatible) or rank by distance-decay × population
(rank=population). API: `/nearest?lat&lon&rank=distance|population`, default
`distance`; invalid rank → 400. Distance-decay function: score =
population / (d² + 1) style gravity model — pick a concrete formula, document
it in the PR, keep it monotone (closer still wins ties at equal population).
Oracle test extended to model weighted semantics over small fixtures;
measured before/after on the village-next-to-a-city case at prod scale.

Out of scope: postal/name ranking, config knobs beyond the query param.

## Lane DEPLOY — container + chart + release pipeline

Owner: go-engineer. Branch: `lane/deploy`. New files only — no collisions.
Files owned: `Dockerfile`, `.dockerignore`, `helm/**`,
`.github/workflows/release.yml`, README deploy section.

Multi-stage Dockerfile → distroless static image; `/healthz` healthcheck;
config via env (CONFIG_PATH/PORT already supported; document the env story);
prod dataset volume story documented (image ships empty; operator mounts
datasets/ or lets the initializer download on first boot). Helm chart per
Baikonur cluster conventions (validate with the helm-validator agent gate
before any apply; no secrets — public data API; resources sized for the
measured RSS ~< 9.1 GB → request generously, e.g. 12Gi memory). GitHub
Actions release workflow: on tag `v*` → build/push image (GHCR) → create
GitHub release with CHANGELOG notes. No goreleaser.

Out of scope: actually applying to the cluster, Gitea fleet repair, CORS.

## Integration (Day 9) and ship (Day 10)

Day 9: all lanes on `sprint/v1.0`; full canonical gates; prod-scale
validation rerun (warm start, RSS, index sizes, query latencies, fuzzy p50);
README Performance table + review report updated. Day 10: ship-ready
sequence — PR to main, GitHub CI green on the exact head, fast-forward-only
merge, tag `v1.0.0`, GitHub release from CHANGELOG, sprint report with
metrics vs targets and handoff items (Gitea runner fleet → infra handoff
doc; admin-region reverse geocoding → v1.1 candidate).
