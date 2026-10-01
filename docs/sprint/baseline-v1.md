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
