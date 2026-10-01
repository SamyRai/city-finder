# Nearest City Finder for Go

A high-performance Go library to find the nearest city based on geographical coordinates using the S2 Geometry Library.

## Features

- **Efficient Nearest City Search**: Uses the S2 Geometry Library to provide fast and accurate nearest city searches.
- **Low Memory Consumption**: Optimized for low memory usage.
- **Easy Integration**: Simple API for integrating into your Go projects.
- **Support for Postal Codes**: Find cities based on postal codes.

## Why S2?

The S2 Geometry Library is chosen for its superior performance and efficiency in handling geographical data. This library uses `s2.ShapeIndex` to store geographical points as a `s2.PointVector`, which allows for highly efficient spatial indexing.

Nearest neighbor searches are performed using `s2.NewClosestEdgeQuery`, which leverages the spatial index to find the closest points with remarkable speed. This approach provides:
- **Hierarchical Spatial Indexing**: Efficiently manages and queries large sets of geographical points.
- **High Precision**: Ensures accurate results by calculating geodesic distances on the sphere.
- **Low Memory Footprint**: Uses memory efficiently, making it suitable for applications with limited resources.

## Performance

Nearest-neighbor queries are bounded by `s2.ClosestEdgeQuery` with `MaxResults(1)`, which prunes the search instead of collecting a result for every indexed point. Results are validated against a brute-force great-circle oracle in `s2_oracle_test.go`, and the ShapeIndex is built eagerly at startup so the first query after boot pays no construction cost.

**Production scale** — v1.1, measured on the full GeoNames dump (13.47M cities, 17.7M unique names; Apple Silicon):

| Metric | v1.1 | v1.0 | pre-v1.0 (v1 indexes) |
|---|---|---|---|
| Warm start (indexes on disk, datasets not re-parsed) | **19.4–20.9 s (two runs)** | 20.5 s | 43–61 s |
| Heap after warm start (post-GC) | **5.7 GB** | 5.5 GB | 6.3–7.5 GB |
| `FindNearestCity` (rank=distance) | p50 10 µs, p99 ~76–95 µs (10k queries) | p50 10 µs, p99 95 µs | p50 20 µs, p99 177–619 µs |
| `FindNearestCity` rank=population (land queries) | 0.3–40 ms, exact gravity winner; `include=admin` adds ~0 latency | 0.3–40 ms | n/a |
| `CityByName` exact, real keys | p50 0.33 µs, p99 1.8 µs (1k queries) | p50 0.33 µs, p99 1.8 µs | p50 0.67–9 µs |
| `CityByName` fuzzy (1–2-edit typos) | p50 7–21 ms, 1000/1000 typos resolved | p50 7–21 ms | disabled at this scale |
| `CityByPostalCode` | p50 0.4 µs, p99 1.8 µs (1k queries) | p50 0.4 µs | p50 0.5 µs |
| Index files | **559 MB (name v2)** / **279–293 MB (S2 v3)** / **26–28 MB (postal v3)** | 559 / 521 / 98 MB | 1.6 GB (name) / 519 MB / 98 MB |

Cold build (parse + all three indexes, no download) takes ~3 min on the same machine (zstd encoding adds ~1 min over v1.0); a first boot also downloads ~420 MB of GeoNames archives plus the optional ~120 KB admin1-names file. Upgrading from v1.0's on-disk indexes rebuilds them once automatically on first boot (v2 s2/postal files are rejected and regenerated as v3). Peak RSS is workload-dependent: a warm start alone peaks ≈9 GB; the fuzzy index adds ~1.2 GB resident once built (the server starts the build in the background right after initialization — no request ever pays it in-flight; typo lookups issued while it builds, roughly the first ~30–90 s after boot at this scale, get exact-only results; the build state is exported as the `fuzzy_build_state` gauge on `/metrics`); population-ranked queries are bounded by a CPU-sized concurrency gate (saturation returns 503) and can transiently allocate several hundred MB per scan. The Helm chart's defaults (10 Gi request / 14 Gi limit) cover this with headroom.

Since v1.1 the three index deserializations run concurrently on warm starts (name decode dominates at ~14 s; S2 and postal overlap inside its window) and the population-rank anytime bound is tightened by a top-4096 population table, which keeps mid-ocean queries off the full-sphere scan. Both changes alter the measured profile — re-measure the warm-start and population-tail rows above at the next baseline pass before citing them as current.

Micro-benchmark context (100k distinct synthetic points): ~3–5 µs per nearest query — query cost grows with index size, so the production numbers above are the authoritative ones.

Fuzzy name matching (edit distance ≤ 2) works at the full 17.7M-name scale via a q-gram inverted index with length filter and banded Levenshtein verification. It is built lazily on the first fuzzy lookup; `name.FuzzyMaxNames` (default 25M keys) bounds it against unmeasured scales. A documented completeness boundary applies to very short names (≤1 rune at distance 1, ≤4 runes at distance 2); exact matches are always found first regardless. Per-query work is capped by `name.FuzzyMaxCandidates` (default 4,000,000) so degenerate short queries cannot walk unbounded posting lists; a capped query returns the matches verified so far (best-effort, never cached as complete), and `name.FuzzyBudgetTrips()` counts trips. Lower it to ~500k for harder latency clamping at the cost of partial results on a few percent of typo queries — see `docs/design/index-format-v2.md` for the measured tradeoff.

Data scope — administrative divisions: GeoNames rows of feature class A (countries, states, provinces, districts) carry huge synthetic populations and, when indexed, win mid-ocean `rank=population` queries under the gravity model (a whole country can be returned as the nearest "city"). The optional config key `exclude_admin_divisions` (default `false`) drops those rows at dataset load for "real place" semantics. The filter applies when an index is (re)built — warm boots with existing serialized indexes are unaffected until you delete an index file to force a rebuild, and when enabled, admin-division names ("California" as an ADM1 row) no longer resolve through `/coordinates`.

Administrative-region attribution: `/nearest?include=admin` adds `admin1_code`, `admin1_name` (when the optional admin1-names dataset is loaded; codes-only otherwise), and `admin2_code` (when present) to the response. Attribution follows the winning city — near administrative boundaries the reported region is that of the nearest (or population-ranked) city, not polygon containment.

## Installation

To install the library, use `go get`:

```bash
go get github.com/SamyRai/cityFinder
```

## Building Indexes

The library requires pre-built indexes for optimal performance. Two modes are available:

### Test Mode (Small Dataset)
For development and testing with a small dataset:
```bash
make build-test
# or
go run cmd/build-index/main.go test
```

### Production Mode (Full Dataset)
For production use with the complete GeoNames dataset:
```bash
make build-prod
# or
go run cmd/build-index/main.go prod
```

**Note**: Production mode processes ~13.5 million cities and may take several minutes to complete. Prod mode resolves the dataset filenames from the same config the initializer uses — `CONFIG_PATH` is honored exactly as the server honors it, defaulting to `config.json` — and writes its three outputs to the config's index-file keys (the same paths the initializer reads). If no config is found it falls back to the legacy literals with a warning.

## Usage

### Finding the Nearest City

To find the nearest city based on latitude and longitude:

```go
package main

import (
    "fmt"
    "log"

    "github.com/SamyRai/cityFinder/lib/finder"
    "github.com/SamyRai/cityFinder/lib/finder/coordinates"
    "github.com/SamyRai/cityFinder/lib/config"
    "github.com/SamyRai/cityFinder/lib/initializer"
)

func main() {
    cfg, err := config.LoadConfig("config.json")
    if err != nil {
        log.Fatalf("Failed to load config: %v", err)
    }

    cityFinder, err := initializer.Initialize(cfg)
    if err != nil {
        log.Fatalf("Initialization failed: %v", err)
    }
	
    // Find the nearest city
    nearestCity, distance, err := cityFinder.FindNearestCity(40.7128, -74.0060, coordinates.RankDistance) // New York coordinates
    if err != nil {
        log.Fatalf("Failed to find nearest city: %v", err)
    }

    fmt.Printf("Nearest city: %s, %s\n", nearestCity.Name, nearestCity.Country)
    fmt.Printf("Distance: %.2f km\n", distance)
}
```

### Finding a City by Postal Code

To find a city based on postal code:

```go
// Assuming cityFinder is initialized as shown above
nearestCity := cityFinder.FindCityByPostalCode("10001", "US")

if nearestCity != nil {
    fmt.Printf("City for postal code: %s, %s\n", nearestCity.Name, nearestCity.Country)
} else {
    log.Println("No city found for the given postal code")
}
```

## Running the Server

The package also includes a server that provides an API for finding the nearest city and querying cities by name or postal code.

### Building the Server

To build the server, use the following command:

```bash
go build -o nearestcityserver cmd/server/main.go
```

### Running the Server

To run the server, execute the built binary:

```bash
./nearestcityserver
```

By default, the server will listen on port 3000; set the `PORT` environment variable to override. The server enforces read/write/idle timeouts (15s/15s/60s), a 1 MB body limit, a 1024-connection concurrency cap, and recovers from handler panics.

### API Endpoints

- **Health Check**: `/healthz` — returns `{"status":"ok"}`; cheap, never touches the indexes.
- **Metrics**: `/metrics` — Prometheus text format: request counters and latency histograms per route and status code, plus `fuzzy_budget_trips_total` (how many fuzzy searches returned budget-truncated partial results).
- **Find Nearest City**: `/nearest?lat=<latitude>&lon=<longitude>&rank=distance|population>` — lat/lon must be finite and in range (`[-90, 90]` / `[-180, 180]`); anything else returns 400. `rank` is optional (default `distance`; any other value returns 400). `rank=population` applies weighted semantics: cities are scored by the gravity model `population / (d² + 1)` (d = great-circle km) and the highest score wins — exactly, over the whole dataset — via a radius that escalates from 10 km until no city outside it can mathematically beat the in-radius best (a top-4096 population table tightens that bound, so even mid-ocean queries usually certify without the full-sphere scan). A nearby big city beats the closest village. Such responses also carry the winner's `Population`. Concurrent population-ranked queries are gated by a CPU-sized semaphore; saturation returns `503` with `Retry-After: 1` rather than queueing unbounded scans. Returns the city plus `distance_km` (great-circle, 2 decimals). An optional `include=admin` (any other value returns 400) appends the winning city's administrative region — `admin1_code`, `admin1_name` (when the optional admin1-names dataset is loaded; codes-only deployments omit it), and `admin2_code` (when the city has one); attribution follows the nearest city, not polygon containment, so near a boundary it may report the region across the line.
- **Batch Nearest**: `POST /nearest/batch` — JSON body `{"points":[{"lat":..,"lon":..,"rank":"distance|population","include":"admin"}, ...]}` (1–100 points; per-point optional fields with the same semantics as GET). Returns `{"results":[...]}` — a parallel array of the same objects GET `/nearest` returns, `null` for a point with no indexed city. `Content-Type: application/json` required; unknown JSON fields and invalid points are rejected with the offending index; the population-rank concurrency gate applies per point (a saturated gate mid-batch fails the whole request with 503 + `Retry-After`).
- **Find City by Name**: `/coordinates?name=<city_name>&country-code=<country_code>` — both parameters required. The name is matched exactly first, then fuzzily (edit distance ≤ 2), so typos like `Pars` still resolve. Surrounding whitespace is trimmed; names longer than 200 runes return 400 (bounds the fuzzy path's query cost).
- **Find City by Postal Code**: `/postalCode?code=<postal_code>&country-code=<country_code>` — both parameters required. Inner spaces in postal codes are significant (GeoNames stores GB codes as `SW1A 1AA`); only surrounding whitespace is trimmed.

The server shuts down gracefully on SIGINT/SIGTERM (10s drain).

## Testing

```bash
go test ./...
```

The S2 finder includes a brute-force oracle test, the initializer/download layer is covered by `httptest`-based tests, and the concurrency behavior is exercised by stress suites (run with `-race` for the race detector).

## Initialization and Datasets

This project requires datasets from the [GeoNames](http://www.geonames.org/) database. Specifically, you need `allCountries_dump.txt` for city data (extracted from the downloaded `allCountries.zip` archive) and `allCountries_zip.txt` for postal code data (extracted from `zipCodes.zip`). These files should be placed in the `datasets` folder.

During initialization, the application checks if these datasets and the three pre-built indexes are available. If they are not, it downloads and extracts the required datasets and builds whichever of the three indexes is missing. Downloads are verified (HTTP status checked, streamed to a temporary file and renamed atomically), extraction is equally atomic (a crash mid-extract cannot leave a truncated dataset that a later boot would bake into the indexes), a pre-existing archive that fails to extract is re-downloaded once instead of blocking every later startup, and archives are deleted after a successful extraction. When all three pre-built indexes are present, the raw datasets are not even downloaded (a rebuild that needs them fetches them on demand) and the three indexes decode concurrently, which makes warm starts fast. One initializer per datasets folder is enforced with a lock file, so two cold-booting processes (e.g. a rolling update sharing a volume) cannot truncate each other's index writes.

### Using the Server

The server can be started using the following command:

```bash
go run cmd/server/main.go
```

## Contributing

Contributions are welcome! Please fork the repository and submit pull requests for any improvements or bug fixes.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.

## Acknowledgements

- The [S2 Geometry Library](https://github.com/golang/geo) for providing the efficient spatial indexing and search capabilities.
- [GeoNames](http://www.geonames.org/) for providing the geographical data used in this project.

## Deployment

The service ships as a container image built from the repo-root `Dockerfile` (multi-stage: `golang:1.26` builder → `gcr.io/distroless/static-debian12:nonroot` runtime; static binary, non-root user, no shell, no package manager). Pushed `v*` tags publish it to `ghcr.io/<owner>/city-finder` (lowercase owner) for `linux/amd64` and `linux/arm64` — see `.github/workflows/release.yml`.

### Running the container

```bash
docker build -t city-finder .
docker volume create city-finder-data

docker run -d --name city-finder \
  -p 3000:3000 \
  -v city-finder-data:/data \
  --memory 14g \
  city-finder

curl http://localhost:3000/healthz
```

The memory floor matters: a cold start on an empty volume peaks around 9 GB RSS while building indexes, and the server runs at ~5.5-9 GB when warm.

### Image environment variables

| Variable | Default in image | Purpose |
|---|---|---|
| `PORT` | `3000` | HTTP listen port. |
| `CONFIG_PATH` | `/etc/cityfinder/config.json` | Config file to load. Absolute paths are opened as-is; a relative path resolves against the container's working directory (`/app`), and a relative `datasets_folder` inside the config resolves against the config file's directory. |

To run with your own config, mount it and point `CONFIG_PATH` at it (`docker run -v "$PWD/my-config.json:/etc/cityfinder/config.json:ro" ...`). The shipped default config keeps all GeoNames URLs and file names from the repo-root `config.json` but pins `datasets_folder` to `/data/datasets`.

### Datasets storage (`/data`)

Everything the server downloads and builds lives under `/data` — mount a volume there. Three options, in increasing order of boot speed:

1. **Empty volume, download on boot.** First start downloads ~420 MB of GeoNames archives and builds all indexes: ~6 minutes and a multi-GB RAM spike. Fine for a trial, slow for every restart.
2. **Raw datasets mounted.** Pre-place the extracted GeoNames dump files in the volume's `datasets/` folder; boot skips the download but still builds the indexes.
3. **Pre-built indexes mounted (recommended).** Place `s2index.gob`, `name_index.gob` and `postal_code_index.gob` in the volume's `datasets/` folder; boot warm-starts in well under a minute.

Seed a named volume with pre-built indexes from a local `datasets/` directory:

```bash
docker run --rm \
  -v city-finder-data:/data \
  -v "$PWD/datasets:/seed:ro" \
  alpine sh -c 'mkdir -p /data/datasets && cp /seed/*.gob /data/datasets/'
```

### Helm chart

`helm/city-finder` deploys the service (Deployment with rolling updates, Service, optional PVC and Ingress, probes on `GET /healthz`):

```bash
helm install city-finder ./helm/city-finder \
  --namespace city-finder --create-namespace \
  --set image.repository=ghcr.io/<owner>/city-finder \
  --set image.tag=1.0.0 \
  --set persistence.enabled=true
```

Defaults are sized for the v1.0 memory profile (memory request `10Gi`, limit `14Gi`, startup-probe budget 600 s). A single replica rolls without an outage window (`maxUnavailable: 0`, `maxSurge: 1`); scale to two or more replicas before enabling the (default-off) PodDisruptionBudget. Memory and startup-probe values are the two knobs to re-check whenever the index format changes. Validate any local customization with `helm lint` and `helm template ... | kubeconform -strict -summary`.

### Cluster adoption (Harbor / ArgoCD)

Registering the image in Harbor and creating the ArgoCD `Application` (auto-sync policy; the chart's manifests already carry `argocd.argoproj.io/sync-wave` annotations) is an operations handoff documented outside this repo. This repository ships the image and the chart, and intentionally contains nothing that talks to a cluster.