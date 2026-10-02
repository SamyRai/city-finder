# Architecture

## Components

```
cmd/server        HTTP server: main (process lifecycle, signals, PPROF_ADDR)
  app             production fiber stack: config, recover, access log, metrics, routes
  routes          handlers, parameter validation, population gate, batch fan-out
  metrics         Prometheus text registry
  diag            opt-in net/http/pprof listener
cmd/build-index   offline index builder (test / prod datasets)
cmd/loadgen       open-model load generator (thin CLI over internal/loadgen)
internal/loadgen  constant-arrival scheduler, outcome classification, sweep summaries
lib/initializer   download → extract → load → build/deserialize the three indexes
lib/config        config file loading and validation
lib/dataLoader    GeoNames dump and postal parsers
lib/finder        facade over the three finders
  coordinates     S2 nearest-city index (distance + population rank, admin attribution)
  name            exact / fuzzy / prefix name index
  postalCode      postal code index
lib/city          shared city types
benchmarks        bench.sh (measurement protocol) + committed baseline outputs
helm, deploy      Helm chart, image config
```

## Why S2

Points are stored as an `s2.PointVector` in an `s2.ShapeIndex`, a hierarchical
spatial index on the sphere. Nearest-neighbour queries use
`s2.ClosestEdgeQuery` with `MaxResults(1)`. Distances are geodesic, so there
is no projection distortion near the poles or the antimeridian. The index is
built eagerly at startup (the s2 library warns that construction can
transiently use up to ~20× the index memory), so no query pays for it.

The [cross-engine comparison](benchmarking.md#9-cross-engine-comparison-2026-10-02-v120)
measured this design against scipy cKDTree, Redis GEO, an R-tree and brute
force on the full dataset, with exact winner agreement against cKDTree.

## Design notes

- [design/index-format-v2.md](design/index-format-v2.md): on-disk index
  formats, zstd framing, fuzzy budget sweep.
- [design/admin-attribution-v1.1.md](design/admin-attribution-v1.1.md): admin
  attribution and S2 format v3.
- [design/narrative.md](design/narrative.md): the original design narrative
  (linear search → R-tree / k-d tree → S2); historical, pre-benchmark claims.
- [sprint/](sprint/): sprint reports, lane briefs, the 2026-10-01 deep review
  and the production baseline log.
