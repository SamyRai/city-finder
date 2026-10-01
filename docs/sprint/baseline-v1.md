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
