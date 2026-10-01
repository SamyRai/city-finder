# Admin-region attribution + format v3 — v1.1 design note

Status: accepted for v1.1. Owner: ADMIN-V3 lane. Read after
`docs/design/index-format-v2.md` (this note builds on v2's framing and
version-reject mechanisms).

## Goal

`/nearest` can report the administrative region (admin1, e.g. state/
province; admin2 where present) of the winning city — the pragmatic
"reverse geocoding" attribution: the admin of the nearest (or
population-ranked) city. NOT polygon containment: near boundaries the
attribution follows the nearest city, which may sit across the line. This
accuracy boundary is documented in the API and README.

## Data (verified on the Oct-2026 dump)

- allCountries field 10 (0-indexed) = admin1 code, present on 97.9% of rows;
  field 11 = admin2 code, present on 57.7%. Codes are short per-country
  strings ("AD/02", "US/CA"); ~4K distinct admin1 pairs worldwide.
- Names for admin1 codes: GeoNames `admin1CodesASCII.txt` (~120 KB,
  `code\tname\tname-ascii\tgeonameId`). Added as an OPTIONAL fourth dataset:
  when present, responses include admin1 NAME; when absent (offline /
  minimal deployments), responses include admin1 CODE only + one log line.
  admin2 NAMES are deferred (admin2Codes.txt is ~4 MB for 57% coverage —
  poor value density; revisit if users ask).

## Storage — no City change, no name-index change

Attribution lives only where it is served: the S2 index.

```
S2Finder gains (all lazily consumable by NearestPlace):
  Admin1IDs  []int32   // per-city id into Admin1Codes; -1 = absent
  Admin2IDs  []int32   // per-city id into Admin2Codes; -1 = absent
  Admin1Codes []string // "US.CA" style composite keys (country.code)
  Admin2Codes []string
  Admin1Names map[string]string // composite key -> name (nil when dataset absent)
```

Heap cost at prod: 2 × 13.47M × 4 B ≈ 108 MB + tiny tables. `city.City` is
untouched, so the name index and postal payload structs are untouched.

## Format v3 — s2 and postal only; name stays v2

- **s2 v3**: zstd-framed (same framing as name v2: whole-stream zstd, CRC,
  per-call encoder/decoder) payload `{Cities, Admin1IDs, Admin2IDs,
  Admin1Codes, Admin2Codes}`. Version bump 2→3; a v2 file fails the header
  check → `ErrCorruptIndex` → initializer auto-rebuild (the proven v1→v2
  path). File size: 521 MB → expect ~380–450 MB (zstd eats the id arrays'
  locality; measure).
- **postal v3**: zstd framing only (struct unchanged), 2→3. 98 MB → expect
  ~40 MB.
- **name**: stays v2. The v1.0 lockstep principle was "bump together when
  `city.City` changes" — City does not change here. Mixed versions across
  the three files are fine: each header is validated independently and each
  auto-rebuilds independently.

## Loader

`LoadGeoNamesCSVWithLimit` parses fields 10/11 into the SpatialCity build
path — carried on `SpatialCity` (build-only, like AltNames) so `City` stays
clean. Empty fields → -1 id at index build. The admin1-names file loads in
the initializer when present (config key `admin1_codes_file`; URL for cold
download `admin1_codes_url`), absent file → names map nil, codes-only mode.

## API

`GET /nearest?...&include=admin` (optional; absent/empty → response
byte-identical to v1.0; any other value → 400 `Invalid include`). With it,
the response gains, after `distance_km`:

```json
"admin1_code": "CA", "admin1_name": "California", "admin2_code": "073"
```

(`admin1_name` omitted in codes-only mode; `admin2_code` null/omitted when
the city has none.) OpenAPI + README bullet updated by this lane.

## Gates

1. Oracle: known fixtures (US city → CA/California; AD mountain → 02/...;
   boundary case documented in test comments).
2. Round-trip v3 (pointer/count validation incl. admin arrays); v2 s2 and
   postal files rejected → initializer auto-rebuild → rewritten as v3.
3. Codes-only degradation: no admin1Codes file → API returns codes, no
   names, one log line; gates still pass.
4. Prod before/after: s2index/postal file sizes, warm start total + s2
   decode split, heap; fuzzy/exact/nearest latencies unchanged (no
   regression: attribution adds one slice read per query).
