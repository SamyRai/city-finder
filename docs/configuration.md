# Configuration

## Locating the config file

| Variable | Default | Meaning |
|---|---|---|
| `CONFIG_PATH` | `config.json` (image: `/etc/cityfinder/config.json`) | Config file for the server and `cmd/build-index prod`. Relative paths resolve against the process working directory. |
| `CONFIG_FILE` | — | Fallback read by `config.LoadConfig("")` for library users who pass no path. |

A relative `datasets_folder` inside the file resolves against the **config
file's directory**, so the repo-root `config.json` (`"datasets"`) points at
`<repo>/datasets`.

## Config keys

| Key | Default (repo `config.json`) | Purpose |
|---|---|---|
| `datasets_folder` | `datasets` | Where datasets, archives and the three index files live |
| `all_cities_url` / `all_cities_zip` / `all_cities_file` | GeoNames `dump/allCountries.zip` → `allCountries_dump.txt` | City dataset source, archive name, extracted file |
| `postal_codes_url` / `postal_codes_zip` / `postal_codes_file` | GeoNames `zip/allCountries.zip` → `allCountries_zip.txt` | Postal dataset |
| `admin1_codes_url` / `admin1_codes_file` | GeoNames `admin1CodesASCII.txt` | Optional admin1 display names for `include=admin` (codes-only without it) |
| `s2.index_file` | `s2index.gob` | Serialized S2 index |
| `name_index_file` | `name_index.gob` | Serialized name index |
| `postal_code_index_file` | `postal_code_index.gob` | Serialized postal index |
| `include_feature_classes` | `""` (everything) | Comma-separated GeoNames feature-class allowlist, e.g. `"P"` (populated places only). Invalid letters fail config load. |
| `exclude_admin_divisions` | `false` | Drop feature-class `A` rows (countries, states, districts) |

Unknown keys still load, but the server logs them once at startup
(`ignoring unknown keys ...`), so a misspelled key does not silently fall
back to its zero value. The v1.1 `s2.min_level`/`max_level`/`max_cells` keys
were removed in v1.2 because nothing consumed them. A file holding anything
after the JSON object fails to load.

At startup the server also checks that the dataset and index file names
(`all_cities_file`, `postal_codes_file`, `s2.index_file`, `name_index_file`,
`postal_code_index_file`) are set. It also checks that no two keys,
including the zips and the admin1 file, name the same file, since one
download or index would overwrite the other. The zips and the admin1 file
may be left empty: the zips are only used when a dataset must be
downloaded.

### Data scope knobs

Both knobs apply when an index is **(re)built** from the raw dump. A warm boot
that deserializes existing index files is unaffected until you delete an index
file to force a rebuild.

- `exclude_admin_divisions: true` gives "real place" semantics. Class-A rows
  carry huge synthetic populations and otherwise win mid-ocean
  `rank=population` queries: a whole country is returned as the "nearest
  city". Admin-division names ("California" as an ADM1 row) then no longer
  resolve through `/coordinates`.
- `include_feature_classes` is the stricter allowlist and is applied first.
  `"P"` also removes class-L "regions" such as the 49M-population "HHS
  Region 9", which dominates population ranking across the western US, and
  country-less undersea features. With `"P"`, `exclude_admin_divisions` is a
  no-op.
- A filter (or a truncated or wrong dataset file) that leaves zero cities
  fails startup with an error naming the file and the filters. Nothing is
  serialized, so the next start is not mistaken for a warm start that serves
  empty indexes. An empty postal file is allowed.

## Runtime environment

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `3000` | HTTP listen port: a decimal number from 1 to 65535. Anything else (`abc`, `0`, `99999`) makes the server exit with status 1 immediately, before the index load. |
| `PPROF_ADDR` | unset (off) | Opt-in `net/http/pprof` listener on its own address and mux, never on the API port. Bind it to loopback (`127.0.0.1:6060`) and reach it via a port-forward: it exposes goroutine stacks and heap contents. Used for profiling and to collect PGO profiles from a real workload. |
| `CONFIG_FILE` | unset | Deprecated alias of `CONFIG_PATH`, honored only when `CONFIG_PATH` is set to the empty string (and by `config.LoadConfig("")` in the Go API). Using it logs a deprecation notice; set `CONFIG_PATH` instead. |
| `GOMAXPROCS` | runtime default | With no container CPU limit the runtime uses every host core. Pin it for reproducible throughput. The population gate is `min(GOMAXPROCS, 8)`, batch fan-out is `GOMAXPROCS`. |
| `GOMEMLIMIT`, `GOGC` | unset / 100 | GC soft limit and pacing. Set `GOMEMLIMIT` below the container memory limit. |

## Library tunables (Go API)

Package variables in `lib/finder/name`. Set them **before** the first fuzzy
lookup.

| Variable | Default | Meaning |
|---|---|---|
| `name.FuzzyMaxNames` | 25,000,000 keys | Above this the fuzzy index is never built (exact-only) |
| `name.FuzzyMaxCandidates` | 4,000,000 posting entries | Per-query cap on the fuzzy walk; a capped query returns partial results (`name.FuzzyBudgetTrips()`). ~500k clamps latency harder at the cost of truncating a few percent of typo queries. |
