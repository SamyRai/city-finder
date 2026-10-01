# Sprint report — city-finder v1.2 (2026-10-01)

Lead + parallel go-engineer/iterate lanes on disjoint worktrees; every lane
verified first-hand before cherry-pick onto `sprint/v1.2` (linear history).
Full canonical gates green on the integrated head: build, vet, gofmt,
`go test -race ./...`, staticcheck, govulncheck, `go mod tidy -diff`, helm
lint + kubeconform (both value permutations).

The sprint ran in two waves: robustness/ops (review-driven, 10 objectives
from the v1.1 post-ship review) and product scope (name-index flattening,
ADM division filter, batch + autocomplete endpoints).

## Metrics vs targets

| Target | Result | Status |
|---|---|---|
| Fix the H1 extraction defect (truncated dataset baked into indexes) | `.part`+rename extraction, atomic like downloads; regression tests | met |
| Bound rank=population worst case (OOM/latency vector) | top-4096 anytime bound kills the terminal full-sphere scan + ~300 MB slice (exactness unchanged, oracles green); CPU-sized HTTP gate (503 + Retry-After) caps concurrency. Measured: memory/concurrency bounds met; prod ocean LATENCY remains 8–14 s (wide discs, not the terminal scan) — further data-scope follow-up noted | partially met (as designed: bounded blast radius, not bounded latency) |
| No in-request fuzzy build (~30–90 s hang) | background build + server `WarmFuzzy()` at boot; `fuzzy_build_state` gauge | met |
| Name-finder memory (flatten nested map) | heap after warm start 5.7→4.49 GB end-to-end (−1.2 GiB); lane-isolated name-build 5.27→3.73 GB (−29%) | met |
| Config contract honest | dead s2 knobs + never-read BuildIndex param removed; build-index honors CONFIG_PATH + IndexFilePaths parity test | met |
| Observability | `/metrics` (Prometheus text, no new deps): requests, latencies, budget trips, build state | met |
| Warm-start decode parallelized | three decodes concurrent; name decode bounds the path | met |
| Dataset retention | warm boots skip dataset ensure; on-demand re-fetch for rebuilds; archives deleted after extract | met |
| API growth | `POST /nearest/batch` (1–100 points), `GET /autocomplete` (prefix search, no fuzzy dependency) | met |
| Real-place data scope | `exclude_admin_divisions` knob (default off), loader + initializer + build-index wired | met |

## v1.2 validation (same methodology as baseline-v1.md; numbers below)

Measured on the rebuilt local indexes (13,465,092 cities / 18.70M name
keys), Apple Silicon, Go 1.27.1, quiet machine.

| Metric | v1.2 | v1.1 (reference) |
|---|---|---|
| Cold rebuild (parse + all three indexes, datasets present) | 2 m 26 s, peak RSS 9.25 GB | ~3 min, ≈12.6 GB peak |
| Index files | 531 MB name (v2) / 279 MB s2 (v3) / 26 MB postal (v3) | 559 / 280–293 / 26–28 MB |
| Warm start (decode-only boot) | 23.3 s quiet (45.9 s on a noisy rerun) | 19.4–20.9 s |
| Heap after warm start | 4.49 GB | 5.7 GB |
| Fuzzy build (background at boot) | 29.3–33.4 s | ~30–90 s lazy, in-request |
| Nearest rank=distance (10k random) | p50 11.9 µs / p99 103 µs | p50 10 µs, p99 ~76–95 µs |
| rank=population | random global n=120: p50 4.0 s / max 16.6 s; ocean n=5: 8.5–14.3 s | land 0.3–40 ms; ocean ~10 s |
| Name exact / fuzzy / autocomplete | ~3 µs p50 (warm) / 5.1 ms p50, 1000/1000 / 10.5 µs p50 | 0.33 µs p50 / 7–21 ms p50 / n/a |

## Workstream outcomes

- **Robustness wave** (initializer/loader/server): crash-atomic extraction;
  per-datasets-folder boot lock (stale-lock stealing, pid-liveness via
  Signal 0); postal loader tolerates field-count damage (quote corruption
  deliberately still fails loudly — LazyQuotes would swallow file tails);
  warm-boot dataset skip with on-demand re-fetch; archive deletion.
- **Population bounds lane**: in-memory top-4096 population table
  (population > 0, deterministic ties) tightening the outside-radius bound
  to `max(best exact top-K score outside R, popK/(R²+1))`; never larger
  than the retired single-max bound; terminal full-sphere iteration kept
  as the exactness fallback; all-zero-population oracle added (was
  missing); mutation-checked early-termination test (reverting the bound
  fails it).
- **Fuzzy warmup lane**: CAS winner spawns the n-gram build in a background
  goroutine (first typo query degrades to exact-only instead of hanging);
  `WarmFuzzy()`; unknown-country early exit; empty fuzzy results no longer
  cached (L1 stale-miss fix); every changed test listed in the lane report.
- **Name flattening lane**: per-country `nameTable{names sorted; starts;
  ids}` CSR postings over `cities []*city.City`; AddCity via overflow map;
  on-disk v2 unchanged (compat proven against a pre-refactor fixture);
  build stages through the nested shape then flattens (build 59→97 s,
  accepted); exact-tail 292→585 ns p50 (accepted); measured −1.43 GiB.
- **API wave**: batch endpoint shares one `executeNearest` core with GET
  (GET byte-identical, tests unmodified); autocomplete rides the sorted
  tables (`PrefixNames`, binary search + walk, overflow merged).
- **Ship hygiene**: `/metrics` hand-rolled (counters/histograms/gauges,
  bounded label cardinality); dead `util` package removed; stale
  dependabot PRs on GitHub (#16–18) identified as closable (user-owned).
- **Docs actualization**: README/CHANGELOG/OpenAPI/article/design-note/helm
  comments updated; deploy/config.json gained the missing admin1 keys
  (containers had been serving codes-only since v1.1 — drift found by this
  pass); dead s2 keys removed from shipped configs.

## Handoff items (not sprint work)

- Tag `v1.2.0` + GitHub release (release workflow publishes the multi-arch
  image from the tag) — owner: user.
- Gitea `origin/main` is stale at pre-v1.0 `92b38c5`; fast-forwarded as
  part of ship, but the Gitea runner fleet remains an infra backlog item.
- Re-baseline caveat: measured on a dev machine; the second run was
  visibly noisy (boot 45.9 vs 23.3 s) and the repo's ±2–3× p99 run-to-run
  noise note for fuzzy tails still applies. Population ocean latency
  (8–14 s) is the open latency item: the bound removed the terminal scan,
  the wide escalation discs remain — candidate next levers are
  MaxResults-bounded per-disc scoring or the class-P data scope.
- Deferred with rationale: per-index streaming zstd decode; opt-in dataset
  freshness HEAD-check; contiguous `[]city.City` backing + string
  interning (lane-identified ~0.1 GiB each); class-P (populated-places-only)
  filter variant.
