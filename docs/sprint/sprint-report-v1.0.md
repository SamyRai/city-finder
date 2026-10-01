# Sprint report — city-finder v1.0 (2026-10-01)

Lead + parallel go-engineer lanes on disjoint worktrees; every lane verified
first-hand before integration onto `sprint/v1.0` (linear history, one commit
series). Full canonical gates green on the integrated tree: build, vet,
gofmt, `go test -race ./...` (all packages), staticcheck, govulncheck,
`go mod tidy -diff`; helm lint + kubeconform (both value permutations).

## Metrics vs targets

| Target | Result | Status |
|---|---|---|
| Warm start ≤25 s (from 43–61 s) | **20.49 s** (name decode 14 s, was 36–54 s) | met |
| Name index ≤800 MB (from 1.6 GB) | **559 MB** (v2 payload, zstd-framed) | met |
| Fuzzy p50 <50 ms at 17.7M names (was disabled) | **7.4 ms** mixed typo workload / **20.6 ms** pure distance-2 (lane-measured), 1000/1000 typos resolved | met |
| Population-weighted nearest | exact gravity winner (`population/(d²+1)`), escalating-radius under an anytime bound; land 0.3–40 ms, ocean ~10 s documented worst case | met |
| Container + Helm + release pipeline | distroless static image (verified), chart through the helm-validator gate, tag→GHCR multi-arch + GitHub release workflow | met |
| Tag v1.0.0 | ship sequence follows this report | pending |

## Workstream outcomes

- **NAME-V2 (format v2 + fuzzy)**: distinct-city payload with int32 refs and
  pointer-table rehydration; coordinated header bump 1→2 across name/s2/postal
  (v1 files auto-rebuild via the existing initializer fallback — proven at
  prod scale); zstd framing after the first cut measured 972 MiB (over gate)
  and the byte decomposition identified the omitted name-key component;
  final 559 MB. Decoder lifecycle fixed (per-call zstd decoder; a shared one
  retained ~900 MB of buffers). Fuzzy phase: q-gram inverted index (q=3,
  CSR storage, rarest-lists walk) + banded allocation-free Levenshtein
  verifier (cross-validated against the reference implementation and brute
  force; the validation caught two real band bugs). FuzzyMaxNames 2M→25M
  with measured citations. Documented completeness boundary for very short
  names and a d2 tail (~0.75 s p99 on weak-filter short queries) with a
  per-query candidate budget named as the v1.1 lever.
- **QUALITY-TAIL**: postal silent-zero fix (skip-and-log; current dump is
  clean — fix is protective); MIT LICENSE; CHANGELOG; OpenAPI 3.0.3 (rank
  parameter specced at integration); exact-tail pprof analysis — no cheap
  win, correctly left unfixed (tail is memory-hierarchy + GC).
- **WEIGHTED**: rank API with byte-compatible default; two design iterations
  forced by measurement — MaxResults(16) regressed distance queries to ~10 s
  (this golang/geo version only prunes at maxResults==1), and k=16 truncated
  (7/30 random points divergent vs k=64); the shipped escalating-radius
  search is exact, verified against brute force at prod scale including a
  crowd-out fixture the pool design failed. Data quirk surfaced: GeoNames
  ADM0/ADM1 rows are indexed with huge populations and win ocean queries
  under the gravity model (pre-existing distance behavior too) — noted as a
  v1.1 data-scope question.
- **DEPLOY**: image/chart/workflow as above; the helm-validator gate caught
  a blocking default (startup probe 180 s < guaranteed 6–8 min cold start →
  crash-loop) plus sync-wave placement, label set, PDB guard, CI chart gate —
  all fixed before integration.

## Handoff items (not sprint work)

- **Gitea runner-fleet outage** — public repo CI stays on GitHub Actions per
  decision; fleet repair tracked as an infra handoff (GLPX GP backlog).
- **Baikonur adoption** — cluster-side: add to `platform/apps.yaml`, Harbor
  image onboarding (or keep GHCR), and the IngressRoute-vs-Ingress decision
  flagged in the chart's values comment.
- **v1.1 candidates**: admin-region reverse geocoding; s2/postal zstd
  compression (name-only today); fuzzy per-query candidate budget (d2 tail);
  ADM0/ADM1 feature-class filtering for "real place" semantics;
  config-loading decoupled from project-root discovery (container ships a
  go.mod marker today — documented in the Dockerfile); CORS/rate-limiting if
  fronted publicly.
- The plan's "review report §14" is an external artifact from the review
  session; the in-repo numbers surface (README Performance) is updated
  instead.

## Deviations from plan

- DEPLOY started early (Day ~4) — zero file overlap with in-flight lanes.
- QUALITY-TAIL edited three test fixtures in `data_loader_test.go` against
  its "do not edit" instruction: the fixtures passed only through the
  silent-zero bug being fixed; corrections were minimal and verified.
- NAME-V2's size gate initially missed (972 MiB); resolved by a bounded zstd
  extension rather than accepting the miss or redesigning the payload.
- Sample sizes on the slowest measurements (population ocean classes, fuzzy
  prod scales) were reduced vs the 10k protocol with rationale documented in
  lane reports.
