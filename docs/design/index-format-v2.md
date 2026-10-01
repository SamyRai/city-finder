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

## Fuzzy (as built, phase 2)

The BK-tree is retired as the fuzzy search structure and removed from the
Finder entirely (v2 had already stopped serializing it). Its replacement is
an immutable q-gram inverted index over the distinct names:

- **Grams**: rune-level trigrams over each name padded with two `\x00`
  sentinels per side. Rune-level grams keep the filtering math consistent
  with the rune-based Levenshtein verifier.
- **Layout**: CSR — one flattened `[]int32` of name ids grouped by gram plus
  per-gram offsets and a `map[string]int32` gram dictionary. Immutable after
  the lock-free build, so concurrent searches take no locks at all.
- **Search** (`maxDistance d`): the q-gram lemma gives `minCommon =
  Gd(query) − 3d` shared distinct grams; walking all but the `minCommon − 1`
  LARGEST posting lists (the rarest-lists trick) enumerates every true match
  while skipping the millions-long lists of ubiquitous grams. Survivors pass
  a length filter (`|runes(name) − runes(query)| ≤ d`, which subsumes the
  name-side gram-count filter) and Levenshtein verification.
- **Completeness boundary**: names of ≤ 1 rune may be missed at d=1 and ≤ 4
  runes at d=2 (every one of their grams can be destroyed by the edits).
  Fuzzy is a best-effort typo-rescue layer; exact phase-1 lookups are
  unaffected, and the previous prod state (BK-tree over threshold) had NO
  fuzzy at all.
- **AddCity after a build**: the CSR cannot take incremental inserts, so
  post-build names land in a small overflow list that searches scan
  linearly; the next rebuild (which snapshots the whole index) folds them
  in. The lock-free build/commit state machine is unchanged from the BK-tree
  era: snapshot under RLock, build lock-free into a local structure, commit
  under Lock gated on an unchanged key total (an AddCity that lands mid-build
  discards the build for a retry).
- **Measured** (Apple silicon, Oct 2026 GeoNames: 18,698,093 keys,
  17,727,652 distinct names; typo workload = 1k real names with 1–2 edits,
  272.4M postings over 2.18M distinct grams at prod):

  | scale | build | structure resident | d1 p50/p99/p99.9 | d2 p50/p99/p99.9 |
  |---|---|---|---|---|
  | 100K | 0.32 s | 11.0 MB | 25 µs / 0.38 ms / 0.93 ms | 122 µs / 3.9 ms / 4.4 ms |
  | 1M | 3.3 s | 82.2 MB | 113 µs / 2.0 ms / 2.5 ms | 0.69 ms / 21.5 ms / 23.9 ms |
  | 17.73M (prod) | 94 s | 1.18 GiB | 2.6 ms / 42.5 ms / 209 ms | 20.6 ms / 749 ms / 1.07 s |

- **Verification**: Levenshtein is a hand-rolled banded, early-exit,
  allocation-free checker (query runes decoded once per search, candidate
  streamed rune-by-rune, DP confined to the ±d diagonal). A prod-scale CPU
  profile showed the agnivade library's two `[]rune` conversions per
  candidate cost ~half of all d2 search time; swapping it cut d2 p50 from
  55.8 ms to 20.6 ms. The checker is cross-validated against the reference
  implementation over a fixed + pseudo-random pair grid
  (TestLevenshteinCheckerMatchesReference), and search is verified against
  brute-force Levenshtein over safe name lengths
  (TestNGramSearchMatchesBruteForce).
- **Known tail**: d2 p99/p99.9 (~0.75–1.1 s at prod) comes from short or
  common-gram queries whose filters degenerate and walk multi-million-entry
  posting lists. CityByName always tries distance 1 first (p50 2.6 ms), so
  only two-edit typos that miss at distance 1 pay the tail. A per-query
  candidate budget would clamp it at the cost of another completeness
  boundary — deliberately left as a v1.1 lever, not shipped silently.
- **Go/no-go gate (1M, run before committing)**: structure 82 MB and d2 p50
  1.05 ms measured (pre-verifier-fix numbers); linear extrapolation to
  17.73M projected ~1.5 GB (< 3 GB budget) and d2 p50 well under the 100 ms
  stop line — GO. As-built prod: 1.18 GiB, d2 p50 20.6 ms < 50 ms target.
- **FuzzyMaxNames**: kept as a bound, raised from 2,000,000 to 25,000,000
  with the prod numbers above cited in the code — prod (18.70M keys) builds
  and searches inside target, and the disable path stays for absurd scales.
  It still counts (country,name) keys, not distinct names, so the
  over-approximation comment in the code still applies.
