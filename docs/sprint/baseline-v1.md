# Production baseline — v1 formats (Day 1 anchor)

Measured 2026-10-01, 02:56–02:57 local, this machine (Apple Silicon, darwin
arm64), Go 1.27.1, warm start = all three v1 indexes present, datasets not
re-parsed. Method: one-off harness (`initializer.Initialize` timed in-process;
latencies measured in-process over the initialized finder; nearest = 10k
random global coordinates, name/postal = 1k real keys sampled from the
loaded indexes; peak RSS via /usr/bin/time -l). Same harness reruns on Day 9
for the v2 "after" numbers.

| Metric | v1 baseline (2026-10-01) |
|---|---|
| Warm start, total | 60.65 s |
| — S2 index deserialize | ~6 s |
| — name index deserialize | 54 s (89% of total) |
| — postal deserialize + wiring | ~0.5 s |
| Peak RSS (warm start) | 9.32 GB |
| Heap after GC, post-init | 7.53 GB alloc / 13.0 GB sys |
| Index files (v1) | 1.6 GB name / 519 MB s2 / 98 MB postal |
| FindNearestCity (10k) | p50 21 µs, p90 89 µs, p99 619 µs, max 4.6 ms |
| CityByName exact, real keys (1k) | p50 0.67 µs, p90 2.2 µs, p99 9.1 µs |
| CityByPostalCode, real keys (1k) | p50 1.3 µs, p90 2.0 µs, p99 5.0 µs |
| Fuzzy matching | disabled at 17.7M names (FuzzyMaxNames gate 2M) |

Notes:
- README's table (65 s warm, 9.1 GB, exact p50 9 µs / p99 124 µs) measured a
  December-2025 dump with a mixed hit/miss name sample; today's fresh dump
  and real-key-only sample give the numbers above. Both are valid anchors;
  the Day-9 comparison uses THIS method on both sides.
- The 54 s name decode is the sprint's primary lever (NAME-V2): gob flattens
  35.07M pointer refs into full struct copies versus 13.47M distinct cities.
- Targets for v2: name index ≤ 800 MB, warm start ≤ 25 s, fuzzy p50 < 50 ms
  at full scale.

## Day-9 final validation (integrated sprint/v1.0, 2026-10-01, same method)

| Metric | v1.0.0 | v1 baseline | Target |
|---|---|---|---|
| Warm start total | 20.49 s | 60.65 s (cold-cache) / 38.6–43.1 s (settled) | ≤25 s ✓ |
| Name decode | 14 s | 36.4–54 s | — |
| Heap after GC | 5,548 MB | 7,531 MB | — |
| name_index.gob | 559 MB (zstd v2) | 1,726 MB | ≤800 MB ✓ |
| s2index.gob / postal | 521 MB / 98 MB | 519 MB / 98 MB | — |
| nearest (distance) | p50 10.5 µs / p99 94.5 µs | p50 21 µs / p99 619 µs | — |
| name exact (real keys) | p50 334 ns / p99 1.8 µs | p50 667 ns / p99 9.1 µs | — |
| fuzzy d2 (mixed typo workload) | p50 7.4 ms, 1000/1000 resolved; first-query lazy build ~30 s | disabled | p50 <50 ms ✓ |
| postal | p50 375 ns / p99 1.8 µs | p50 1.3 µs / p99 5.0 µs | — |
| population rank (land classes, lane-measured) | 0.3–40 ms; ocean ~10 s documented worst case | n/a | — |

Cold build on the integrated tree (sources present, no download): 112 s
total; heap_sys transient 23.7 GB during build. Peak process RSS in the
measurement run (12.6 GB) includes the lazily built fuzzy index (+1.18 GB)
and transient allocations from ocean population scans; warm start alone
peaks ≈9 GB.

## v1.1 validation (2026-10-01, integrated tree, same harness, quiet machine)

| Metric | v1.1 | v1.0 (Day-9) |
|---|---|---|
| Warm start (two runs) | 19.41 / 20.94 s | 20.49 s |
| Heap after settle | 5,702 MB | 5,548 MB (+154 MB admin arrays, as designed) |
| Index files | 559 MB name (v2) / 279 MB s2 (v3) / 26 MB postal (v3) | 559 / 521 / 98 MB |
| nearest distance p50/p99 | 10.3–10.5 µs / 66–112 µs | 10.5 / 94.5 µs |
| name exact p50 | 333 ns | 334 ns |
| fuzzy mixed-typo p50/p99 | 3.0–4.7 ms / 278–342 ms, 1000/1000 | 7.4 ms / 365 ms |
| postal p50 | 375–458 ns | 375 ns |
| include=admin overhead | ~0 (lane-measured 10.0 µs p50 vs 10.5 base) | n/a |
| Peak RSS (whole run incl. fuzzy build + ocean scans) | 12.3–13.0 GB | 12.6 GB |

Cold build with zstd encode: ~3 min quiet (the 371 s figure in the first
attempt was measured under heavy concurrent load and is not representative).
Population-mode latencies are ocean-mixed uniform-band samples; per-class
land numbers (urban 1.9 ms / suburban 40 ms / rural 302 µs; ocean ~10 s)
remain those measured by the v1.0 WEIGHTED lane and re-confirmed by ADMIN-V3.

## v1.2 validation (2026-10-01, rebuilt v3/v2 indexes, same method, quiet machine)

| Metric | v1.2 | v1.1 (reference) |
|---|---|---|
| Cold rebuild (datasets present, no download) | 2 m 26 s; peak RSS 9.25 GB | ~3 min; ≈12.6 GB peak |
| Index files | 531 MB name (v2) / 279 MB s2 (v3) / 26 MB postal (v3) | 559 / 280–293 / 26–28 MB |
| Warm start (decode-only boot) | 23.3 s | 19.4–20.9 s |
| Heap after warm start (post-GC) | 4.49 GB | 5.7 GB |
| Fuzzy n-gram build | 29.3 s, background at boot (`WarmFuzzy`) | ~30–90 s, in-request on first typo query |
| Nearest rank=distance (10k random) | p50 11.9 µs / p99 103 µs / max 7.9 ms | p50 10 µs / p99 ~76–95 µs |
| rank=population, random global (n=120, ocean-heavy, noisy run) | p50 4.0 s / p90 11.4 s / max 16.6 s | ocean worst case ~10 s (documented) |
| rank=population, ocean points (n=5) | 8.5–14.3 s | ~10 s |
| Name exact, real keys (1k, warm passes) | p50 ~3 µs / max ~13 µs / 1000 hits | p50 0.33 µs / p99 1.8 µs |
| Name fuzzy, real-name typos (1k) | p50 5.1 ms / p99 101 ms / max 614 ms / 1000 resolved | p50 7–21 ms, 1000/1000 |
| Autocomplete PrefixNames (1k 3-char prefixes) | p50 10.5 µs / p99 94 µs | n/a |
| Postal, real keys (n=141) | p50 0.79 µs / p99 9.3 ns-class tail | p50 0.4 µs |

Notes from this pass: the warm start pays ~3 s over v1.1 for the flatten
sort at deserialize (each country's names sorted once while decoding) and
the heap drops 5.7 → 4.49 GB; the cold rebuild peak drops ~3.4 GB. The
boot-time fuzzy build means no user request ever pays the in-request
build. The population tail is now bounded in MEMORY and CONCURRENCY (top-K
bound removes the terminal full-sphere scan and its ~300 MB slice; the
HTTP gate caps simultaneous scans) but its LATENCY at prod remains
multi-second for far-from-land points — dominated by the wide escalation
discs themselves; a second, noisier run measured p50 4 s / max 16.6 s
over 120 random global points. Exact-name GC settling inflated first-pass
timings ~10–40×; the numbers above are from converged warm passes (the
one-off harness and probe live outside the repo). Boot on the noisy second
run was 45.9 s vs 23.3 s quiet — cite the quiet figure with variance in
mind.

## v1.3 validation (2026-10-02, v3/v2 index files unchanged from v1.2, same method, quiet machine)

v1.3 changed no index format, so warm boot and heap carry over from the
v1.2 validation (23.3 s decode-only boot; 4.49 GB post-GC heap). The
changes below are query-path and were measured at prod scale on the
13.47M-row dataset:

| Metric | v1.3 | v1.2 (reference) |
|---|---|---|
| Nearest rank=distance (10k random) | p50 9.4 µs (pooled query objects, −20%); allocs −40% | p50 11.9 µs / p99 103 µs |
| rank=population, ocean-class points | 1.0–9.5 s (top-K-anchored disc) | 8.5–14.3 s |
| rank=population exactness | all brute-force oracles (ties, crowd-out, all-zero populations) pass unedited | same |
| POST /nearest/batch | points execute concurrently (GOMAXPROCS-bounded; population gate still caps the expensive class) — a far-from-land batch now costs ≈ its slowest single point, not the sum | sequential (sum of points) |
| include=admin / autocomplete / postal | unchanged paths (re-measured in-repo: see benchmarks/README.md baseline) | — |

Repeatable in-repo benchmarks now cover every public query path (the
missing ones — autocomplete PrefixNames, admin attribution, the metrics
registry, and HTTP round-trips — were added 2026-10-02, with baseline
numbers, fixture caveats, and a trust assessment of the prod-scale
comparison in benchmarks/README.md). /metrics additionally exposes
go_goroutines and go_heap_alloc_bytes per scrape.
