# Index format v2 — design note

Status: accepted for sprint v1.0 (Day 1). Owner: NAME-V2 lane.
Scope: `lib/finder/name`, `lib/finder/coordinates`, `lib/finder/postalCode`,
`lib/city`, `lib/dataLoader/cityCoordinate.go`.

## Problem (measured)

The v1 name index serializes `map[string]map[string][]*city.City` with gob.
gob has no pointer sharing: every reference is encoded as a full struct copy.
At production scale (Oct 2026 GeoNames dump: 13.47M distinct cities,
35.07M name references) this means:

- 1.6 GB on disk (vs. ~13.47M actual structs + ~140 MB of int32 ids needed),
- ~54 s of the 65 s warm start decoding 35M City structs only to intern most
  of them away afterwards,
- a transient ~4.3 GB decode heap before the interning pass collapses it.

The interning pass (`internDecodedStrings`) fixes the *strings* but cannot fix
the struct duplication: 35.07M `city.City` values stay alive even though only
13.47M are distinct.

Second, `city.City` lacks population — GeoNames field 14 (0-indexed) of the
allCountries dump, parsed by nobody today. Population-weighted nearest
(WEIGHTED lane) needs it.

## Design

### One coordinated version hop v1 → v2 (all three payloads)

`city.City` gains `Population int32`:

```go
type City struct {
    Latitude   float64
    Longitude  float64
    Population int32   // GeoNames allCountries field 14; 0 when absent
    Name       string
    Country    string
}
```

Because gob zero-fills fields it does not find, a v1 file decoded into the new
struct would load every `Population` as 0 — silently wrong data. Therefore all
three index headers (`CFNAMEIDX`, `CFS2IDX`, `CFPOSTIDX`) bump
`indexVersion` 1 → 2 in one change:

- **name**: new payload layout (below) — the actual size/startup lever.
- **s2**: payload struct unchanged (`[]city.City`), but the embedded City
  meaning changes; the bump forces a regenerate so no v1 S2 file can be
  interpreted with zero-filled populations.
- **postal**: payload struct (`PostalCodeEntry`) is unchanged and embeds no
  City. The bump is for lockstep consistency only: all three indexes
  regenerate together from the same source on first v2 boot, and there is a
  single version story in logs and docs. (Decision from the sprint plan; the
  cost is a one-time auto-rebuild of a 98 MB file.)

**v1 file handling:** keep the v1 *rejection* path, not a v1 reader. A v1 file
fails the header version check and returns `ErrCorruptIndex` exactly as
truncated files do today; the initializer's existing `ensure*Index` fallback
then rebuilds from the source datasets and rewrites the file as v2. Deployed
indexes self-heal on first boot. No v1 decode code is retained — decoding a
pre-Population payload has no correct answer.

### Name payload v2

```
gob(indexHeader{Magic: "CFNAMEIDX", Version: 2, Count: len(Countries)})
gob(nameIndexPayloadV2)
```

```go
type nameIndexPayloadV2 struct {
    Cities []city.City                         // each distinct city exactly once; index i == city id
    Refs   map[string]map[string][]int32       // country -> name -> city ids
}
```

- Serialize: walk `InvertedIndex`, assign ids by pointer identity
  (`map[*city.City]int32`), emit distinct `Cities` in first-encounter order
  plus `Refs`. Map iteration order makes file bytes non-deterministic —
  acceptable: indexes are regenerable artifacts gated by count validation and
  round-trip tests, not byte comparison.
- The BK-tree, `isBKTreeBuilt`, and `allNames` are **dropped from the file**.
  They are lazy-build runtime state (the bulk build path never populates
  them today); v2 always rebuilds the fuzzy structure lazily on first lookup.
- Decode: gob-decode `Cities`, build the pointer table `ptrs[i] = &cities[i]`
  in one pass, then rehydrate `map[string]map[string][]*city.City` from
  `Refs` — one pointer store per reference, no per-ref struct allocation.
- Interning: no longer needed for correctness (each City decodes once).
  Country strings still decode as one backing per city (~13.47M copies of
  ~250 values): run a cheap Country-only intern pass over `Cities`
  (13.47M calls vs. v1's 70M). Interning `Name` is optional — measure at prod
  scale and keep only if the heap delta is real.

### S2 payload v2

`SerializableS2Finder` stays `[]city.City`; only `indexVersion` changes. The
file grows by 13.47M × 4 B ≈ 54 MB (Population field), heap by +323 MB
(struct 48 → 72 B) — the cost Population buys.

### Loader

`LoadGeoNamesCSVWithLimit` parses field 14 as `int32` (values fit: largest
GeoNames feature populations are < 40M). Empty or unparsable field → 0 +
continue (population is enhancement data; rows are never dropped for it).

## Gates

1. Round-trip: serialize → deserialize → deep-equal index (incl. pointer
   sharing: mutating one city must be observable from every name that
   references it), existing test-suite green.
2. v1 auto-rebuild: a v1-headed file (synthesized) is rejected with
   `ErrCorruptIndex` and the initializer rebuilds + rewrites v2.
3. Count validation: decoded distinct-city count == built count; total refs ==
   total refs built; per-country key counts preserved.
4. Prod-scale before/after: name index size, warm-start time, warm heap —
   targets ≤ 800 MB and (with S2+postal unchanged in spirit) total warm start
   ≤ 25 s.

## Fuzzy (forward pointer, Days 6–8)

With v2 landed, the n-gram fuzzy index replaces the BK-tree for search
(q-gram inverted index + length filter + Levenshtein verify), gated by a
measured go/no-go at 1M names before the design commits (memory risk). The
BK-tree serialization path it depends on is already gone in v2. See the lane
brief in `docs/sprint/lane-briefs.md`.
