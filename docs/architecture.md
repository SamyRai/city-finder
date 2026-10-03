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
cmd/memreport     end-to-end footprint measurement + answer transcript (before/after proofs)
internal/loadgen  constant-arrival scheduler, outcome classification, sweep summaries
lib/initializer   download → extract → load → build/deserialize the three indexes
lib/config        config file loading and validation
lib/dataLoader    GeoNames dump and postal parsers
lib/finder        facade over the three finders
  coordinates     S2 nearest-city index (distance + population rank, admin attribution)
  name            exact / fuzzy / prefix name index
  postalCode      postal code index
lib/city          shared city types
lib/indexfile     index file framing: atomic + fsynced writes, streaming verified reads
benchmarks        bench.sh (measurement protocol) + committed baseline outputs
helm, deploy      Helm chart, image config
```

## Data ownership

Every city record exists once per process. The S2 index owns the city table
(`S2Finder.Cities`, one contiguous `[]city.City` in loader row order). The
name index stores **row numbers**, and the initializer attaches it to that
same table with `ShareCities`. The attach is only accepted after proving the
table identical: element-wise, by `city.Fingerprint`, or (for a legacy file
numbered in another order) value-for-value with an id remap. Sharing can
therefore never change an answer. Primary-name keys share their bytes with
`City.Name`. The postal index keeps only the fields a lookup returns.

| Index file | Format | Content |
|---|---|---|
| S2 | v3 | city table, admin ids/codes |
| name | v3 | per-country name → row ids; references the S2 table by row count + fingerprint (a standalone index embeds its own table instead) |
| postal | v4 | per country: sorted codes + latitude / longitude / place-name columns |

All three files share one framing (`lib/indexfile`): a gob header (magic,
version, count) that is checked before any decompression, then one zstd
frame (CRC on) holding the gob payload. Writes go to `<file>.part`, are
fsynced, and are renamed into place. Reads stream from the file through zstd
into gob, so no whole-file buffer sits next to the decoded index, and drain
the frame to its end, which verifies the CRC and rejects trailing bytes.

Reads are bounded against hostile or corrupt files, with no format change.
The zstd window is capped at 64 MiB (`indexfile.MaxDecoderWindow`; the
writer's is 4 MiB, a test reads it back from the frame header), because the
decoder allocates the declared window up front: a 9-byte frame declaring the
library default of 512 MB used to cost 512 MB per attempt. The decompressed
payload is also capped by a byte budget, `indexfile.DefaultMaxPayloadBytes`
(1 GiB, gob's own per-message ceiling; a 13M-city S2 index is about 0.7 GB),
which a caller can tighten with `Reader.LimitPayload`. A ratio cap was
measured and rejected: real indexes compress 1.6x (S2), 1.9x (name) and 2.3x
(postal), but 200k legitimate cities with identical name and coordinates
reach 3900x (postal codes 100x, name 11x), the same range as a bomb of
one-byte entries, so any ratio that stops the bomb would also force a
rebuild loop for repetitive data. Going over the budget or the window is
`ErrFormat`, which each package reports as its `ErrCorruptIndex`, so the
initializer rebuilds the file as for any other corruption.

The previous formats (name v2, postal v3) still load. The initializer
rewrites them in the current format on the first boot, with no rebuild and
no dataset download. An index that does not match the S2 index is rebuilt.

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
