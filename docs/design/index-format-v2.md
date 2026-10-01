# Index format v2 — design note

Status: accepted for sprint v1.0 (Day 1); amended in-lane after phase-1
measurement (zstd framing, corrected arithmetic — see "Amendments" at the
end). Owner: NAME-V2 lane.
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
gob(indexHeader{Magic: "CFNAMEIDX", Version: 2, Count: len(Countries)})   // raw
zstd-frame(gob(nameIndexPayloadV2))                                      // compressed
```

The header stays uncompressed so version checks — including v1 rejection —
run before any decompression. The payload is one zstd frame at
`SpeedFastest` with the frame CRC enabled (corruption inside a structurally
valid frame is then detected deterministically by the decoder). A truncated
or undecodable frame, or a non-zstd payload behind a v2 header, is
`ErrCorruptIndex` exactly like a malformed raw stream was; the initializer
rebuilds. (In-lane amendment: the original sketch was a raw gob payload; the
measured 971.9 MiB missed the 800 MB gate, and zstd framing closes it at
558.8 MiB measured in the shipped path. SpeedDefault would save ~55 MB more
but taxes every warm start; rejected.)

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
  (13.47M calls vs. v1's 70M). Interning `Name` was evaluated and SKIPPED:
  primary names are mostly distinct values, and the v1 key-interning
  experiment already measured that interning near-unique strings grows the
  unique-package table more than it frees duplicates.

### S2 payload v2

`SerializableS2Finder` stays `[]city.City`; only `indexVersion` changes.
Corrected arithmetic (measured): the struct grows 48 → 56 B (one int32 plus
4 B padding, not 48 → 72 B), and the FILE grows only ~2.3 MB — gob omits
zero-valued fields, and only 655,559 of 13.47M GeoNames features carry a
non-zero population. The bump still forces a coordinated regenerate so no
v1 S2 file can be interpreted with zero-filled populations. S2 and postal
payloads stay UNCOMPRESSED: their gates are met, and fleet-wide
compression is v1.1 material, not this format hop.

### Loader

`LoadGeoNamesCSVWithLimit` parses field 14 as `int32` (values fit: largest
GeoNames feature populations are < 40M). Empty or unparsable field → 0 +
continue (population is enhancement data; rows are never dropped for it).
`City.Population` carries `json:"-"`: routes embed `city.City` directly and
its field set IS the wire contract, so exposing population in HTTP responses
is a routing/API decision (WEIGHTED lane / integration), not this format hop.

## Amendments after phase-1 measurement

- **zstd framing** (above): raw payload measured 1,019,093,855 B = 546 MB
  city table (53.6%, 40.6 B/city) + 318 MB reference keys (31.2%, 17.0 B/key
  over 18.70M (country,name) keys — the component the original estimate
  omitted) + 155 MB ids (15.2%, 4.41 B/ref). zstd SpeedFastest + CRC at prod
  scale: 586,411,260 B (558.8 MiB, 69.9% of the 800 MB gate).
- **Decode split** (v2 + zstd, prod, measured in-init): frame read 470 ms,
  zstd decompress 586 MB → 1,019 MB in 2.85 s, gob decode 6.61 s; the split
  is logged by `DeserializeIndex` on every warm start.
- **Decoder lifecycle**: the zstd decoder is created per call and closed
  immediately after `DecodeAll`. A shared package-level decoder was measured
  to retain ~900 MB of internal window/worker buffers after the prod-scale
  frame (warm heap 7.2 GB vs 5.5 GB); warm start is once-per-boot, so setup
  cost is irrelevant and the retention is pure waste.
- Corrected S2 growth, Name-interning skip, and the `json:"-"` note are
  folded into the sections above.

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
