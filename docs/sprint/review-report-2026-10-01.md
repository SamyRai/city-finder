# city-finder — Deep Review, Evaluation & Estimate

| | |
|---|---|
| **Repository** | [mukimovd/city-finder](https://gitea.bk.glpx.pro/mukimovd/city-finder) (local clone: `~/projects/city-finder`) |
| **Reviewed at** | `main` @ `fb7e44c` ("ci: add concurrency cancel-in-progress"), 2026-09-30 |
| **Reviewer method** | 3 parallel review lanes (correctness review, spatial-core inspection, security scan) + first-hand verification of every load-bearing claim: build, vet, full test suite, race detector, benchmarks, govulncheck, static analysis, direct source reads. All P0–P2 findings below were **verified first-hand by the lead**, not just reported by a lane. |
| **Overall verdict** | **Not production-ready as shipped.** One critical performance defect makes the flagship endpoint unusable at full dataset scale; fuzzy search is silently dead; several operational hazards. The core spatial design is sound and the highest-impact fixes are small — a focused 1.5–2 week effort takes this to genuinely production-grade. |

---

## 1. Executive summary

city-finder is a Go library + Fiber HTTP service that resolves a lat/lon to the nearest city (Google S2 geometry), plus name and postal-code lookups, over GeoNames data (~12.76M cities in production mode). The codebase is a substantially refactored fork of `SamyRai/cityFinder` (2024) with serious 2026 investment: a rewritten S2 core, a 2.3k-LOC benchmark framework, an extensive test suite (71 test functions), Renovate/Dependabot, and Gitea CI.

The paradox of this repo: **the parts that look most impressive are not the parts that work.** The test suite is large but the endpoint tests are vacuous (they skip on any non-200, and one escapes its own query string into gibberish), which is exactly why the three biggest defects shipped unnoticed:

1. **The nearest-city query returns ALL edges, not the nearest one's limit** — every `/nearest` request scans, collects, and sorts every point in the index. Measured first-hand: **49.8 ms/query on just 100K points** (17.6 MB, 350,926 allocations per query). At the production 12.7M points this extrapolates to **tens of seconds per request** and ~gigabytes of garbage per request. The fix is one line: `.MaxResults(1)`.
2. **Fuzzy name matching is dead in every production path** — the BK-tree is deliberately not populated during index build (`name.go:444`), the lazy builder returns instantly on the empty name list yet marks itself built (`name.go:576-577,571`), and the empty tree is persisted. `/coordinates?name=Pars` returns 404. This was a designed, advertised feature.
3. **A failed dataset download permanently bricks startup** — `downloadFile` never checks the HTTP status (`initializer.go:87`), writes straight to the final zip path, and any pre-existing zip is trusted as valid. One transient network failure leaves a corrupt zip that fails every subsequent boot until manually deleted.

Also verified: an actual vulnerability in the pinned Fiber version (govulncheck: `GO-2026-4543`, fixed in 2.52.12, called code), an unbounded cache keyed by raw user input (remote memory exhaustion), NaN coordinate bypass of validation, a latent self-deadlock and a latent map-write race that both activate the moment fuzzy search is revived, and a warm-start path that needlessly re-parses the entire 12.76M-row dataset (with a 1.2 GB slice preallocation) even when all indexes are on disk.

What is genuinely good: the S2 usage itself is conceptually correct (degenerate point-edges make `ClosestEdgeQuery` a true point nearest-neighbor; the distance math `ChordAngle→Angle→Radians×6371 km` is right; concurrent post-build queries on a shared `ShapeIndex` are safe); the lib-level test culture is real (property/fuzz tests, concurrency stress tests, cross-finder consistency tests); no secrets anywhere (gitleaks clean across all 33 commits); the zip extractor has a correct path-traversal guard; complexity metrics are healthy (194 functions, avg cyclomatic 3.4).

**Scorecard**

| Dimension | Grade | One-liner |
|---|---|---|
| Architecture & design | **B−** | Clean layering (loader → index builders → finder → server); sound S2 choice; persistence layer is the weak seam |
| Correctness | **C** | Distance math and query mapping correct; two advertised behaviors silently dead/broken; latent deadlock + race |
| Performance (as shipped) | **F** | Scan-all query at 12.7M scale; multi-GB wasted memory; minutes-long warm start |
| Performance (after 1-line fix) | **A−** | ~30 µs/query measured at 2M points with `MaxResults(1)` |
| Security | **C+** | 1 called CVE, memory-DoS cache, NaN bypass, no timeouts/recover; but no secrets, correct zip-slip guard, HTTPS downloads |
| Testing | **B−** | Deep and creative at lib level; vacuous at the HTTP level — the tests that mattered most couldn't fail |
| Docs | **C** | README endpoints/params don't match routes; perf claims contradicted by behavior |
| Operations | **C−** | No timeouts/rate limits/recover; download bricks startup; no index versioning; go.sum not committed |

---

## 2. Project overview

**What it is:** "A high-performance Go library to find the nearest city based on geographical coordinates using the S2 Geometry Library" (README), with name and postal-code lookups, GeoNames dataset auto-download, prebuilt gob indexes, and a Fiber HTTP server on :3000.

**Stack:** Go 1.25 (`go.mod`), `golang/geo` (S2), `gofiber/fiber/v2` v2.52.10, `agnivade/levenshtein`, `cheggaaa/pb` (progress bars), `fatih/color`, `testify`. Makefile has PGO and Green Tea GC experiment targets.

**Layout & size** (9,662 LOC Go total):

| Area | LOC | Contents |
|---|---|---|
| Core library (`lib/`) | ~3,000 | loaders, S2/name/postal finders, initializer, config, city model |
| Server + build CLI (`cmd/`) | ~700 | Fiber server, routes, index builder |
| Benchmark framework (`benchmarks/`) | 2,308 | standalone runner, suites, profilers, HTML reporters |
| Tests | 4,347 | 71 test functions, 18 benchmark functions |

**History & people:** 34 commits, 2024-06 → 2026-08. One human contributor (Damir Mukimov / GitHub `SamyRai`), plus dependabot and google-labs-jules bots. Major 2026 rework: S2 rewrite, Go 1.25 upgrade, CI migration to shared reusable workflows, benchmark infrastructure.

**Actual API surface** (verified in `cmd/server/routes/routes.go` — note it differs from the README, see finding D-4):

| Endpoint | Parameters | Returns |
|---|---|---|
| `GET /nearest` | `lat`, `lon` | nearest city JSON + distance not included in response (city only; distance computed but discarded at `routes.go:34`) |
| `GET /coordinates` | `name`, `country-code` (**required**, upper-cased) | exact-match city JSON |
| `GET /postalCode` | `code`, `country-code` (**required**, upper-cased) | city JSON for postal code |

---

## 3. Architecture (verified)

**Build path.** `initializer.Initialize` → ensure datasets (download+unzip if missing) → `ensureFinders` → always `loadData` (full TSV parse of ~12.76M rows + full postal file) → three index builders, each "deserialize from gob if file exists, else build from the parsed data and serialize" (`initializer.go:150-176`).

**Query path.** `/nearest` → `Finder.FindNearestCity` → `S2Finder.NearestPlace` (`s2.go:62-85`): one `PointVector` (all cities as degenerate point-edges) added as the single shape of one `ShapeIndex`; query = `s2.NewClosestEdgeQuery(index, defaultOptions)` + `MinDistanceToPointTarget`; takes `results[0]`, maps `EdgeID() == point index == city index`, converts `ChordAngle` to km. The edge→city mapping is provably correct (`PointVector.Edge(i)` is `{p[i], p[i]}`), and results are sorted ascending, so `results[0]` is the nearest. This part of the design is right — it's the *options* that are wrong (finding C-1).

**Persistence.** gob files only: `s2index.gob` stores just `[]city.City` and rebuilds the ShapeIndex on every load (`s2.go:109-140`); `name_index.gob` stores inverted index + (empty) BK-tree + flags; `postal_code_index.gob` stores the postal map. No versioning, no checksums, in-place `os.Create` writes; existence of the file is the only validity test (`initializer.go:198`).

**Concurrency model.** Single-threaded startup completes before `Listen` (no race there); after that the S2 index is read-only and `NearestPlace` allocates per-call query objects — concurrent reads are safe (verified against pinned `golang/geo` source: lazy index build is mutex/atomic guarded, and it happens on first query since nothing calls `index.Build()`). Name finder uses an RWMutex with a lazy BK-tree build and a separate cache mutex — this is where the deadlock/race findings live.

---

## 4. Findings

Severity: **C** = critical, **P1** = major, **P2** = moderate, **P3** = minor. All file:line references verified first-hand at `fb7e44c`.

### Critical

**C-1. Nearest-neighbor query has no `MaxResults` — every query processes the entire index.**
`s2.go:67`: `query := s2.NewClosestEdgeQuery(f.Index, s2.NewClosestEdgeQueryOptions())`. The geo library defaults `maxResults` to `math.MaxInt32` (`query_options.go:189`) and its own docs say "you should always specify either MaxResults or DistanceLimit". With defaults, `FindEdges` collects **every** edge in range, then sorts them all; the `maxResults == 1` fast paths (`edge_query.go:493,601`) never fire.
*Evidence (first-hand):* repo's own `BenchmarkNearestPlace` on **100K** points → **49.8 ms/op, 17.6 MB/op, 350,926 allocs/op**. Inspector lane measured at 2M points: default = 4.16 s/query vs `MaxResults(1)` = **30.5 µs/query** with identical results. At 12.76M production points, each `/nearest` request ≈ tens of seconds and hundreds of MB of garbage. The repo's `article.md` claims this query is "optimized for speed" — the intent was there; the option was dropped (likely during the "Replace third-party spatial libraries" → S2 refactor).
*Fix:* `s2.NewClosestEdgeQueryOptions().MaxResults(1)` — one line, ~5 orders of magnitude, verified same answer.

### P1 — Major

**P1-1. Fuzzy name matching is dead in all production paths.**
`name.BuildIndex` explicitly skips `collectAllNames()` ("Skip expensive collectAllNames() during bulk loading", `name.go:444-445`); nothing else ever adds names to the BK-tree; the lazy `buildBKTreeParallel` returns immediately on empty `allNames` (`name.go:576-578`) yet `ensureBKTreeBuilt` still sets `isBKTreeBuilt = true` (`name.go:571`), and the empty tree is persisted to `name_index.gob`. Net: `/coordinates` is exact-match-only; `"Pars"` → 404. The only code path that populates the tree (`BuildIndexStreaming` → `collectAllNames`) has zero callers (dead code).
*Fix:* populate `allNames` at the end of `BuildIndex` (or derive from `InvertedIndex` when empty) — **and fix P2-1/P2-2 first**, because both activate when the tree becomes non-empty.

**P1-2. A failed dataset download permanently breaks startup.**
`downloadFile` (`initializer.go:81-104`): `http.Get` with no client timeout, **no `resp.StatusCode` check** (a 404 HTML page is happily saved as `allCountries.zip`), writes directly to the final path (no temp+rename). `downloadAndExtractDataset` (`initializer.go:59-67`) treats any existing zip as valid. One transient failure → corrupt zip → every boot fails in `unzipAndRename` until someone deletes the file by hand. Also no integrity verification of any kind (no checksums) and unbounded `io.Copy`.

**P1-3. Warm start re-parses the entire raw dataset even when every index exists.**
`ensureFinders` unconditionally calls `loadData` (full 12.76M-row TSV + postal file) before the three `ensure*Index` functions, which then deserialize from disk and ignore the parsed data (`initializer.go:150-176`). Amplified by a hardcoded `capacity := 15000000` preallocation — a ~1.2 GB zeroed `[]SpatialCity` slice per load (`cityCoordinate.go:61-65`), regardless of actual file size. Committed benchmark log shows the CSV load alone takes ~8s; total warm-start waste is minutes of CPU plus multi-GB of transient memory for nothing.
*Fix:* stat the three index files first; parse raw data only if a build is actually needed; size the prealloc from the file.

**P1-4. Known vulnerability in pinned Fiber version, on a called path.**
govulncheck (run first-hand): `GO-2026-4543` (Fiber DoS via route-parameter overflow), Found in `gofiber/fiber/v2@v2.52.10`, **fixed in v2.52.12**, "Your code is affected". All registered routes are static (no `:param`), so exploitability as configured is limited — but the module graph also carries 3 more import-path vulns (incl. `GO-2026-5585`, fixed 2.52.13). *Fix:* bump to ≥ v2.52.13. (Note: a Renovate fiber-v3 bump was previously reverted in a conflict resolution — the v2 line is what actually ships.)

### P2 — Moderate

**P2-1. Latent self-deadlock in `CityByName` (reproduced by inspector lane with the real package).**
Phase 2 takes `nf.mutex.RLock()` with `defer RUnlock()` (`name.go:735-736`) that stays held into Phase 3 (`name.go:746`), and on a cache miss Phase 3's `getCachedFuzzySearch` → `ensureBKTreeBuilt` → `nf.mutex.Lock()` (`name.go:562`) — a writer lock while our own read lock is held → Go RWMutex deadlock that then wedges **every** `/coordinates` request (blocked writer poisons subsequent readers). Currently unreachable only because the tree is empty (P1-1); triggers as soon as fuzzy works and a phase-2 candidate fails to resolve in the requested country with a cold (name, 2) cache. *Fix:* scope the RLock to the map lookups (no defer across phases).

**P2-2. Data race in `buildBKTreeParallel` — 8 goroutines insert into the shared BK-tree concurrently.**
`name.go:612-627`: each worker builds a local tree, then "adds sequentially" — but that loop (`nf.BKTree.Add(name)`, line 624) is **inside the per-worker goroutine**, so up to 8 goroutines mutate the shared tree; `BKTree.Add` does unsynchronized map writes (`util/util.go:73-88`) → concurrent-map-write panic or silent tree corruption. The comment at line 621 admits the intent and the code contradicts it. Latent today (P1-1); live the moment fuzzy is fixed. *Fix:* merge local trees after `wg.Wait()` under one lock, or insert sequentially.

**P2-3. Unbounded `fuzzyCache` growth — remote memory exhaustion.**
Every distinct `/coordinates?name=<anything>` miss inserts into `fuzzyCache` (`name.go:709-714`) keyed by raw user input; nothing is ever evicted (the 1h TTL only skips on read of the same key). Cheap unauthenticated remote memory leak; each unique name creates two entries (`_1`, `_2` thresholds). *Fix:* size-capped LRU or cap name length at the route.

**P2-4. NaN coordinates bypass validation.**
`routes.go:26-32`: `ParseFloat("NaN")` succeeds and NaN fails every `<`/`>` comparison, so `lat=NaN` passes range checks → invalid `s2.Point` → verified empirically to return HTTP 200 with an arbitrary city (no crash). Inf forms are correctly rejected. *Fix:* `math.IsNaN || math.IsInf` guard.

**P2-5. Server hardening gaps: no timeouts, no rate limit, no panic recovery, wildcard bind.**
`main.go:31-38`: Fiber config sets only `ETag` + `EnablePrintRoutes` (fiber defaults: ReadTimeout/WriteTimeout/IdleTimeout = 0 = unlimited; its own source recommends non-zero), `Listen(":3000")` binds all interfaces, no limiter middleware, no `recover` middleware — fasthttp does **no** panic recovery, so any future panic is a whole-process remote crash (verified in fasthttp v1.66.0 source).

**P2-6. Request logger is an attack surface of its own.**
`main.go:58` calls `c.MultipartForm()` on **every** request (including GETs/404s — forced multipart parse), `main.go:67` reads the full body (up to fiber's 4 MB default), and everything is `log.Printf`-interpolated raw (`main.go:76-87`) with ANSI color escapes: log flooding (4 MB/request), log injection via percent-encoded newlines, broken log ingestion, and query strings (location data = PII) logged wholesale. *Fix:* drop body/form capture or gate behind debug; sanitize.

**P2-7. First query after startup pays the entire ShapeIndex build.**
Nothing calls `index.Build()` after `index.Add(&points)` (`s2.go:55-56`, `s2.go:133-134`); the s2 library builds lazily on first query and warns the build "can use up to 20x as much memory per edge as the final index" (`shapeindex.go:837-839`). First `/nearest` after boot stalls for the full 12.76M-edge build. *Fix:* `index.Build()` at init (or persist real index structure).

**P2-8. `s2index.gob` is not an index — persistence saves nothing.**
`DeserializeIndex` stores only `[]city.City` and rebuilds PointVector + ShapeIndex from all points on every load (`s2.go:109-140`). Combined with P1-3, "pre-built indexes" save no load time versus the CSV, contradicting the README. Also: in-place `os.Create` serialization (crash mid-write = truncated file forever treated as valid, `initializer.go:198`), no version headers (gob silently zero-fills changed struct fields — old files poison cities with 0,0 coordinates rather than erroring), nondeterministic map encoding order.

**P2-9. Memory dead weight in the cold-build path: ~2–2.5 GB avoidable.**
(a) A heap `Rect` with two `[]float64` is allocated per city (`cityCoordinate.go:113-116`) ≈ 1.4 GB at 12.76M — and **no `Rect` method has any caller anywhere** (verified by grep; write-only memory). (b) The name index stores pointers into the loader-owned `cities` slice (`name.go:169,228`), pinning the ~1 GB backing array for process lifetime. (c) gob drops pointer sharing: a city with N alternate names is serialized (and re-materialized in RAM) N+1 times in `name_index.gob` (verified empirically by the inspector). The warm/deserialized path doesn't have (a)/(b) — so cold and warm deployments have wildly different memory profiles.

**P2-10. HTTP endpoint tests are vacuous — the safety net that would have caught C-1/P1-1 has holes.**
`main_test.go:215`: `url.QueryEscape("code=" + code + "&country-code=" + countryCode)` escapes the **entire query string** into one blob → server sees no `code` param → 400 → and the guard at `main_test.go:219-222` treats any non-200 as "skipping...". Same skip-on-anything pattern at `main_test.go:191-194`. These tests structurally cannot fail. Also: the `cmd/server` suite rewrites the committed `testdata/*.gob` files as a side effect (verified: my run dirtied two files).

**P2-11. `unzipAndRename` clobbers multi-entry archives and leaks handles on error.**
`initializer.go:128` writes **every** archive entry to the same `newFileName` (last one silently wins) — safe only while GeoNames zips contain exactly one file; error paths at `:132-138` leak `outFile`/`rc`, and `r.Close()` (`:146`) is skipped on any error return. (The zip-slip guard at `:115` is present and correct — genuinely good.)

**P2-12. Internal error details leak to clients.**
`routes.go:36` returns `fmt.Sprintf("Error finding city: %v", err)` → e.g. internal index sizes from `s2.go:78` are disclosed in 500 responses.

**P2-13. Input normalization is inconsistent.**
Country code is upper-cased (`routes.go:49,64`) but name and postal code are raw: `/coordinates?name=paris` 404s while `name=Paris` works; UK postal codes with the standard internal space (`SW1A 1AA`) must match byte-for-byte. Postal-code parse errors are silently zeroed (`zipCodes.go:55-57` — bad rows become (0,0) coordinates served to users).

**P2-14. README documents endpoints that don't exist.**
README says `/postalcode?postalcode=…&country=…`; actual is `/postalCode?code=…&country-code=…` (`routes.go:62-71`); README also omits that `/coordinates` requires `country-code` (`routes.go:50-52` returns 400 without it) and claims prebuilt indexes deliver performance (P2-8) and that the query is optimized (C-1).

**P2-15. The concurrency stress test is flaky by design — and its flakiness is evidence for C-1.**
`TestConcurrentStress_CoordinateFinder` (`concurrency_stress_test.go:110-176`) gives each query a fixed **100 ms wall-clock budget** and counts a timeout as an error (`:159-161`) — while the spawned goroutine's eventual completion *also* increments the same counters (`:147-152`). Timed-out-but-completed ops are double-counted, and the pass criterion is `InDelta(total, success+errors, 10)` (`:173`). Even the *passing* idle run counts 16000 successful + 4 errors = 16004 of 16000. Under coverage instrumentation or CPU contention the C-1 scan-all query blows the 100 ms budget often enough to exceed the ±10 tolerance: verified failing 4/4 under load (3 plain reruns + coverage run) while passing on an idle machine. The test's own comment `"Reduce concurrency to avoid S2 library deadlocks"` (`:122`) shows the author previously hit apparent hangs that were almost certainly C-1 slowness, not library deadlocks. *Fix:* drop the wall-clock criterion (assert errors==0 and totals exactly), or count completions only.

### P3 — Minor

- **P3-1. Dead code inventory** (verified zero callers incl. tests): entire `streaming_shape.go`; entire `serializable.go` (`CityReader`); `finder.NewFinder` (initializer builds the struct literally) and transitively `name.AddCity`, `postalCode.AddPostalCode`; `LoadGeoNamesCSVConcurrent`, `StreamGeoNamesCSV`; `BuildIndexStreaming` + `collectAllNames` chain; pool helpers `getCityFromPool`/`putCityToPool`, `optimizeMemoryLayout`, `addCityUnsafe`, `addCityOptimized`, `fastApproximateDistance`, `buildBKTree`; `util.min`, `LevenshteinDistanceWithThreshold`, `HaversineDistance`, `EuclideanDistance`; all `Rect` methods + `SpatialCity.Bounds`. `streaming_shape.go`'s `Edge()` also swallows errors and would gob-decode the wrong type if ever used. Roughly 700–900 LOC of dead weight.
- **P3-2.** `err.Error() == "EOF"` instead of `errors.Is(err, io.EOF)` (`zipCodes.go:44`).
- **P3-3.** `EdgeID() < 0` unchecked (`s2.go:77` checks only the upper bound).
- **P3-4.** `config.S2` min/max level and max cells are parsed but never read anywhere — S2 tuning is configured-but-inert.
- **P3-5.** Build/CI drift: `make test` runs only `./cmd/server` (README points at `./lib/finder/coordinates/`); `make build-prod` lists `testdata/*.gob` but prod writes `datasets/`; CI pins Go 1.24 while `go.mod` says 1.25 (works only via GOTOOLCHAIN auto-download); **`go.sum` is gitignored** — dependency checksums are not pinned in VCS (reproducibility/supply-chain hygiene; Renovate bumps land unverified).
- **P3-6.** World-writable dataset dirs (`os.ModePerm`, `initializer.go:33,119,125`) and `O_TRUNC` open in them (local-only symlink-truncate risk); HTTP→HTTP redirect downgrades allowed in the default client policy.
- **P3-7.** Path contract divergence: `config.LoadConfig` joins root-relative (`config.go:45,60`), `NewFinder` reads `IndexFile` relative to CWD — same config field, two contracts.

---

## 5. Performance evaluation (measured)

**Query latency** (this machine, repo's own benchmark, `-benchtime 3s`):

| Scenario | Per-query | Allocs |
|---|---|---|
| `NearestPlace`, 100K points, **as shipped** | **49.8 ms** | 17.6 MB / 350,926 allocs |
| `NearestPlace`, 2M points, as shipped (inspector, same geo version) | 4.16 s | results slice = all 2M edges |
| `NearestPlace`, 2M points, with `MaxResults(1)` (inspector) | **30.5 µs** | minimal |
| Extrapolation, 12.76M prod points, as shipped | **tens of seconds** | ~GB garbage/request |

Scaling is superlinear in the shipped configuration because the full result set is sorted (100K→50 ms, 2M→4.16 s: 83× time for 20× points). With `MaxResults(1)` the s2 library's spatial pruning + `LocatePoint` fast path make it effectively O(1)-ish; sub-100 µs at 12.76M is a reasonable expectation, i.e. **>100,000× improvement from one line**.

**Startup** (from the committed `benchmark_output_after_optimization.log`, this machine, 2025-12-09): dataset TSV load 12,760,237 cities ≈ 8 s; S2 index ensure ≈ 9 s; name index build ≈ 9 s+. That log also confirms this exact clone path was used for the project's own benchmarking.

**Memory model (estimated, 12.76M cities):**
- Warm start, today: `[]City` ≈ 0.6 GB + strings + PointVector ≈ 0.3 GB + ShapeIndex (built lazily on first query, with the library-warned transient spike) + name index with gob-duplicated city pointers — realistically **4–8 GB RSS**.
- Cold build, today: add the 1.2 GB prealloc + ~1.4 GB dead `Rect`s + loader-slice retention ≈ **6–10 GB peak**.
- After the P1-3/P2-9 fixes (skip CSV when indexes exist, drop Rects, intern pointers): warm start ≈ **3–5 GB**, and cold build no longer needs multi-GB headroom.

---

## 6. Security evaluation

**Verified first-hand:** govulncheck (1 called vuln: `GO-2026-4543` fiber v2.52.10 → fixed 2.52.12; plus 3 import-level + 2 module-level not called); gitleaks across working tree and all commits: **clean**; NaN bypass demonstrated; download path read.

**Ranked residual risk** (details in findings): remote memory exhaustion via `fuzzyCache` (P2-3) and per-request result garbage at prod scale (C-1 — itself a trivial remote DoS until fixed); unbounded download `io.Copy` + no integrity check (P1-2/P2-14); no timeouts/rate-limit/recover (P2-5); log flooding/injection + PII logging (P2-6); internal error disclosure (P2-12); NaN bypass (P2-4); local-only symlink/mode issues (P3-6).

**Genuine positives:** no secrets anywhere; zip-slip guard correct; downloads over HTTPS to the official GeoNames host; read-only unauthenticated data API is a defensible design for a public service (should be stated explicitly in the README if so).

---

## 7. Code quality metrics

- **194 functions**, avg cyclomatic complexity **3.4** (healthy); max 21 (`cmd/build-index/main.go:main`). Hotspots: `SetupRoutes` (15), `LoadGeoNamesCSVWithLimit` (17), name-index batch/concurrency functions (11–15). Static analysis: 9 warning-level issues, all complexity, **zero** security-class findings in shipped code (gosec hits are in test/bench tooling only).
- **Test suite:** 71 test functions / 4,347 LOC. Genuinely strong at lib level: property/fuzz tests (`fuzz_property_test.go`), concurrency stress (`concurrency_stress_test.go`), cross-finder consistency, real-data integration, per-package benches (18). Weak at HTTP level (P2-10), and the headline stress test is flaky by design (P2-15). All packages **pass** on an idle machine (`go test ./...` ≈ 96 s; `lib/finder` alone ≈ 92 s).
- **Per-package statement coverage** (measured in this review; understates true exercise of `lib` code because cross-package calls from `lib/finder`'s and `cmd/server`'s suites aren't credited without `-coverpkg`): `postalCode` **95.9%**, `coordinates` **64.1%**, `dataLoader` **19.4%**, `name` **6.0%**, `lib/finder` 8.8%, `cmd/server`/`routes`/`city`/`config`/`initializer`/`build-index` 0%. The `name` package's 6% is the stand-out gap — precisely the package where P1-1/P2-1/P2-2 hid.
- **`go vet` clean**, **build clean** (after `go mod tidy` — go.sum is not committed, P3-5).
- **Race detector:** `lib/finder/coordinates`, `name`, `postalCode`, `dataLoader` pass with `-race`. The full `lib/finder` suite under `-race` fails — but from the flaky P2-15 timing assertion, not a reported data race (verified with `-v` output capture; no `DATA RACE` warning). The post-init hot path is race-clean; the one real race (P2-2) sits in the dead path.

---

## 8. Estimates

**Defect-fix effort** (single developer familiar with the code):

| Batch | Items | Effort | Risk |
|---|---|---|---|
| Quick wins | C-1 (`MaxResults(1)`), P1-4 (fiber bump), P2-4 (NaN/Inf), P2-12, P3-5 (commit go.sum, CI pin) | **half a day** | Trivial; each independently verifiable |
| Revive fuzzy correctly | P1-1 + P2-1 + P2-2 + P2-3 **together** (fixing P1-1 alone activates the deadlock and the race), plus regression tests that fail without the fix | **1–2 days** | Medium — concurrency-sensitive; needs the stress tests extended to the name finder |
| Download & startup robustness | P1-2 (status/timeout/temp+rename/checksum), P1-3 (skip CSV when indexes exist; prealloc sizing), P2-11 | **1–2 days** | Low |
| Server hardening | P2-5, P2-6, P2-13 (normalization policy), P2-10 (real endpoint tests — highest leverage per hour in the whole list) | **1–2 days** | Low |
| Persistence & memory | P2-7 (`Build()` at init), P2-8 (temp+rename, version header, real index persistence or honest removal), P2-9 (drop Rects, intern pointers) | **2–4 days** | Medium |
| Hygiene | P3-1 dead-code deletion (~800 LOC), README/P3-4/P3-7, Makefile/CI sync | **1 day** | Trivial |

**Total to production-ready: ≈ 7–11 focused days** (1.5–2 weeks calendar time part-time). The quick-wins batch alone transforms the service from "unusable at scale" to "functional" — that is the striking asymmetry of this codebase.

**Sizing expectations after fixes** (12.76M cities, this class of machine): `/nearest` p99 well under 1 ms (measured 30 µs at 2M with the fix); warm restart ≈ index-deserialize time (~10–30 s once P1-3 lands) instead of minutes; RSS ≈ 3–5 GB.

---

## 9. Recommendations (priority order)

1. **Today:** `.MaxResults(1)` in `s2.go:67`; bump `fiber/v2` ≥ 2.52.13; add `math.IsNaN/IsInf` guards; commit `go.sum`; add `recover` middleware. Then re-run the repo's own benchmark and put real numbers in the README.
2. **This week:** revive fuzzy search as one combined change (populate names → scoped RLock → single-writer tree build → bounded cache), with new tests that demonstrably fail on the old code. Fix the download path (status check, timeout, temp+rename, SHA-256 pin). Skip dataset parsing when all three indexes exist.
3. **Next:** real HTTP-level tests (replace the vacuous suite; stop mutating committed testdata), server timeouts + rate limiting + sane logging, persistence versioning, drop the `Rect` dead weight, call `index.Build()` at startup.
4. **Housekeeping:** delete the dead code (entire `streaming_shape.go`/`serializable.go`, the unused loader variants and helper chain), sync README↔routes↔Makefile, decide and document the public-unauthenticated-API intent, pin CI Go to 1.25.

**Bottom line:** the S2 foundation, the test culture, and the benchmark infrastructure are worth keeping — this is a fixable project, not a rewrite. But until the C-1 one-liner lands, the service's headline feature degrades linearly-superlinearly with dataset size, and two advertised behaviors (fuzzy matching, prebuilt-index performance) do not actually exist.

---

## 10. Remediation log (2026-09-30, branch `fix/review-p0-p1`, 7 commits `23bc09c..f2cb941`)

All P0/P1 and most P2 findings from this report were fixed the same day, via four parallel go-engineer lanes on disjoint worktrees, each verified first-hand by the lead before integration, with full canonical gates rerun on the integrated tree.

**Fixed and verified:**

| Finding | Fix (commit) | Evidence |
|---|---|---|
| C-1 scan-all query | `MaxResults(1)` + eager `index.Build()` (`23bc09c`) | 349K→60 allocs/query; ~16–50 ms → **2.8–5 µs** on 100K distinct points (integrated-tree rerun: 2.8 µs); brute-force oracle green before and after; first-query stall 118.6 ms → 111 µs |
| P1-1 dead fuzzy | tree derived from inverted index on first miss; legacy gobs revive (`8009417`) | `Pars`→Paris red/green; round-trip test |
| P1-2 download bricks boot | status check, 15-min timeout, `.part`+rename, corrupt-archive self-heal (`3607734`) | httptest suite red/green (404 leaves no file, timeout errors) |
| P1-3 warm-start waste | skip dataset parse when all 3 indexes exist; file-size prealloc (`3607734`) | ~1.2 GB → ~70 KB allocations on test data |
| P1-4 Fiber CVE | v2.52.15 + fasthttp v1.70.0/compress v1.18.7/x/sys bumps (`75d6a37`) | govulncheck: **"No vulnerabilities found"** (whole module) |
| P2-1 deadlock | read locks scoped to map lookups (`8009417`) | reproduced red (5.00 s hang), green in 0.00 s |
| P2-2 tree race | sequential build under write lock (`8009417`) | red: `fatal error: concurrent map writes`; `-race` clean |
| P2-3 unbounded cache | 10k cap, expiry-then-oldest eviction (`8009417`) | red/green bound test |
| P2-4 NaN bypass | `parseCoordinate` rejects non-finite (`971e2a4`) | red (200 + garbage city) → green (400) |
| P2-5/P2-6/P2-12 server hardening | timeouts 15s/15s/60s, 1 MB body, recover middleware, PORT env, single-line logger, generic 500s (`971e2a4`) | rebuilt endpoint suite, 12 subtests red→green |
| P2-8/P2-11 unzip/persistence | multi-entry rejected, handles closed (`3607734`) | red/green |
| P2-10 vacuous tests | full rebuild: per-param escaping, known-good fixtures, exact assertions, temp dataset dir (`971e2a4`) | suite now fails on regressions; `testdata/` stays clean |
| P2-13 normalization | TrimSpace (inner postal spaces preserved) (`971e2a4`) | `"AD 100"` 404 test proves no over-normalization |
| P2-14 README | endpoints/perf/testing/datasets synced (`f2cb941`) | — |
| P2-15 flaky stress test | double-count scaffolding removed, exact totals (`476a749`) | passes in 0.5 s (was 21–90 s+); full `lib/finder` suite 92 s → 31.6 s |
| P3-5 hygiene | go.sum committed & un-ignored, CI Go 1.25, Makefile targets (`75d6a37`) | — |

**Bonus fixes found during remediation:** a second live race the review missed (tree search vs `AddCity`, closed in `8009417`); the benchmark fixture's degeneracy documented via a new distinct-points benchmark.

**Final gates on the integrated tree (all green):** `go build ./...`; `go vet ./...`; `go test -count=1 ./...` (all 7 packages); `go test -race` on coordinates/name/postalCode/initializer/dataLoader; govulncheck clean; testdata untouched.

**Not yet done (deferred, from §8 batches):** persistence versioning/temp-rename beyond download (P2-8 partial), Rect dead weight and pointer-retention memory reduction (P2-9), dead-code deletion (P3-1), name-package direct coverage (still low). Branch is local only — not pushed.

---

## 11. Remediation wave 2 (2026-09-30, same branch, 5 commits `8111811..cb16c1c`)

The deferred batch was completed via four more parallel lanes (MEM / NAME / PERSIST / ENRICH), each verified first-hand before integration; all gates green on the integrated tree (full suite, `-race` on five packages, govulncheck clean, query latency unchanged at 2.85 µs).

| Area | Change | Evidence |
|---|---|---|
| Rect dead weight (P2-9a, P3-1 part) | `Rect` type + `SpatialCity.Rect` + dead loaders deleted | exact −80 B / −3 allocs per city (−300K allocs per 100K); ~1.02 GB at production scale |
| Loader-slice pinning (P2-9b) | name index heap-copies City values instead of pointing into `cities[i]` | 0 B freed before vs full 8 MB array freed after at 100K cities (`runtime.KeepAlive` harness — the lane caught and fixed its own vacuous first test) |
| Decoded-string duplication (P2-9c) | City string fields interned after gob decode | 16% of decoded heap saved at 200K entries; the map-key pass measured as a net loss and was dropped |
| Persistence (P2-8) | all three indexes: magic/version/count header, `.part`+rename atomic writes, count validation; initializer rebuilds once over corrupt/incompatible files instead of dying | truncation / wrong-magic / legacy-file / corrupt-index-rebuild all covered by new red/green tests |
| Server enrichments | `/nearest` returns `distance_km`; `/healthz`; graceful SIGINT/SIGTERM shutdown (armed post-init, Listen-return retry) | process-level signal test, stable ×5 |
| Dead code (P3-1) | `streaming_shape.go`, `serializable.go`, `finder.NewFinder` deleted (zero-reference-verified) | — |
| Name coverage | 12-case CityByName phase table + cache hit/miss/expiry/eviction + serialization tests added | package no longer relies on indirect coverage |

**Remaining known items:** `name.ErrCorruptIndex` sentinel tightening (initializer currently rebuilds on any name-index decode error — correct, slightly broad); BK-tree terms not interned (`bkNode` unexported); production-scale memory run of the interning pass; `article.md` still describes the pre-fix behavior. Branch remains local only — not pushed.

---

## 12. Wave 3: final cleanup + production-scale validation (commits `ba8665c..d8474d1` + fuzzy gate)

**Cleanup (committed):** 207 LOC of goast-verified dead code removed (streaming build path, pool helpers, `NewS2Finder`, `EuclideanDistance`, …); `name.ErrCorruptIndex` sentinel with `errors.Is`-gated rebuild (fs errors stay fatal, tested both ways); `lib/config` 0% → 9 tests incl. a fix for the P3-7 path bug (absolute `CONFIG_PATH` was mangled by `filepath.Join` — red/green proven); `gofmt -l .` now empty repo-wide; `article.md` marked historical.

**Production-scale validation** (real GeoNames dump 2026-09-30, Apple M2 / 8 cores / 24 GB; drivers in `/tmp/prodscale`, repo untouched):

| Claim from earlier waves | Verdict at 13.47M cities / 17.7M names |
|---|---|
| Warm-start skip | **Validated**: 348s → 65s (5.3×); dataset parse fully skipped |
| `/nearest` ~3–5 µs (synthetic) | **Refuted at scale**: p50 20.4 µs, p99 177 µs, max 2 ms — still fast, but the synthetic number doesn't extrapolate; README corrected |
| Exact name / postal | p50 9.1 µs / 0.54 µs, p99 124 µs / 1.1 µs — no concerns |
| Interning ~16% of decoded heap | **Corrected to 10.3%** (−439 MB settled) with a transient +1 GB overshoot and ~21 s pass cost |
| Lazy fuzzy (BK-tree) | **NO-GO as designed**: first miss = 3m18s build **under the write lock** (stalls all name lookups incl. exact hits); steady-state typo lookup 1.3–2.2 s; +1.65 GB retained; candidates/query ~1,700 vs ~72 at 1M names |
| Cold start | 5m48s incl. 480 MB download; peak RSS 8.6 GB; name build 2m35s is the dominant phase |
| Index sizes | 519 MB / 1.6 GB / 98 MB (s2 / name / postal) |

**Follow-up fix from these findings (committed, `2515b67`):** fuzzy gate — the tree now builds via snapshot→build-locally→swap with no write lock held (red/green: worst exact lookup during a 270 ms build went from blocked-for-the-whole-build to 23.7 µs), a `FuzzyMaxNames` threshold (default 2M keys) disables fuzzy and falls back to exact-only on huge indexes with a single log line, not-ready results are never cached, and a key-count commit guard prevents losing `AddCity` additions that land mid-build. The 4.4 GB production datasets and indexes from the validation run are preserved locally (gitignored `datasets/`) for future warm-start work.

**Remaining known items after wave 3:** gob flattens pointer sharing (35.07M City refs decoded as 35M structs vs 13.47M distinct cities) — a columnar/dedup index format would cut the 1.6 GB name index, the 54 s deserialize, and warm RSS substantially (the biggest remaining lever); exact-name p99 tail (124 µs on pure map hits) worth profiling only if a name SLO exists; warm deserialize is slightly more memory-hungry than the build path (9.1 vs 8.6 GB peak).

---

## 13. Ship record (2026-10-01)

**PR #32** — https://gitea.bk.glpx.pro/mukimovd/city-finder/pulls/32 — **merged fast-forward-only into `main`**. Merged/`main` SHA: **`92b38c5`** (22 commits over `fb7e44c`, 57 files, +4317/−1390). Feature branch auto-deleted per repo policy; local `main` synced.

**CI evidence:** the Gitea runner fleet has been dead since 2026-08-26 (PR events create no run records at all; last 10 runs all failed at "Download dependencies" from the CI Go pin lagging go.mod + go.sum being gitignored). Per the owner's direction, CI ran on **GitHub Actions** (the repo's public upstream, `SamyRai/cityFinder`) via a new `.github/workflows/ci.yml` porting the same gate. Three iterations to green: (1) a real test-side data race in the shutdown process test, caught by Linux `-race` timing that macOS never triggered — fixed with a mutex-guarded log buffer; (2) six pre-existing staticcheck U1000 unused symbols — deleted; (3) **full green** on `92b38c5`: deps verify, gofmt, build, race tests, vet, staticcheck, govulncheck, tidy check (run 36796675579, 2m26s). The green verdict was posted to Gitea as a commit status (`ci/github-actions-mirror`, target_url = the GH run) to satisfy branch protection — the check genuinely ran and passed on that exact SHA; nothing was overridden.

**Infra findings for the owner:** (a) Gitea Actions runners/queue need attention — no run creation for a month while Renovate PRs piled up; (b) the "GitHub native push mirror" fleet convention does not hold for this repo (GH `main` diverges; our branch never synced), so the manual `github` remote push was necessary; (c) repo merge policy is fast-forward-only with empty status-check contexts — until runners are fixed, merges need an externally-posted status.
