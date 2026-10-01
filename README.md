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

**Production scale** — measured on the full GeoNames dump (13.47M cities, 17.7M unique names; Apple M2, 8 cores, 24 GB RAM):

| Metric | Result |
|---|---|
| Cold start (download → indexes built) | 5m48s, peak RSS 8.6 GB |
| Warm start (indexes on disk, datasets not re-parsed) | 65s, peak RSS 9.1 GB |
| `FindNearestCity` | p50 20 µs, p99 177 µs (10k queries) |
| `CityByName` exact | p50 9 µs, p99 124 µs (1k queries) |
| `CityByPostalCode` | p50 0.5 µs, p99 1.1 µs (1k queries) |
| Index files | 519 MB (S2) / 1.6 GB (name) / 98 MB (postal) |

Micro-benchmark context (100k distinct synthetic points): ~3–5 µs per nearest query, ~1.6 KB / 60 allocs — query cost grows with index size, so the production numbers above are the authoritative ones.

Fuzzy name matching (edit distance ≤ 2) is practical on small and medium indexes but its cost grows near-linearly with the number of distinct names, so on very large indexes it is disabled by a threshold (see `name.FuzzyMaxNames`) and lookups fall back to exact matching.

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

**Note**: Production mode processes ~12.7 million cities and may take several minutes to complete.

## Usage

### Finding the Nearest City

To find the nearest city based on latitude and longitude:

```go
package main

import (
    "fmt"
    "log"

    "github.com/SamyRai/cityFinder/lib/finder"
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
    nearestCity, distance, err := cityFinder.FindNearestCity(40.7128, -74.0060) // New York coordinates
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

By default, the server will listen on port 3000; set the `PORT` environment variable to override. The server enforces read/write/idle timeouts (15s/15s/60s), a 1 MB body limit, and recovers from handler panics.

### API Endpoints

- **Health Check**: `/healthz` — returns `{"status":"ok"}`; cheap, never touches the indexes.
- **Find Nearest City**: `/nearest?lat=<latitude>&lon=<longitude>&rank=distance|population>` — lat/lon must be finite and in range (`[-90, 90]` / `[-180, 180]`); anything else returns 400. `rank` is optional (default `distance`; any other value returns 400). `rank=population` applies weighted semantics: the 16 nearest candidates are scored by the gravity model `population / (d² + 1)` (d = great-circle km) and the highest score wins, so a nearby big city can beat the closest village; such responses also carry the winner's `Population`. Returns the city plus `distance_km` (great-circle, 2 decimals).
- **Find City by Name**: `/coordinates?name=<city_name>&country-code=<country_code>` — both parameters required. The name is matched exactly first, then fuzzily (edit distance ≤ 2), so typos like `Pars` still resolve. Surrounding whitespace is trimmed.
- **Find City by Postal Code**: `/postalCode?code=<postal_code>&country-code=<country_code>` — both parameters required. Inner spaces in postal codes are significant (GeoNames stores GB codes as `SW1A 1AA`); only surrounding whitespace is trimmed.

The server shuts down gracefully on SIGINT/SIGTERM (10s drain).

## Testing

```bash
go test ./...
```

The S2 finder includes a brute-force oracle test, the initializer/download layer is covered by `httptest`-based tests, and the concurrency behavior is exercised by stress suites (run with `-race` for the race detector).

## Initialization and Datasets

This project requires datasets from the [GeoNames](http://www.geonames.org/) database. Specifically, you need the `allCountries.txt` for city data and `allCountries.zip` for postal code data. These files should be placed in the `datasets` folder.

During initialization, the application checks if these datasets and the S2 index are available. If they are not, it downloads and extracts the required datasets and builds the S2 index. Downloads are verified (HTTP status checked, streamed to a temporary file and renamed atomically), and a pre-existing archive that fails to extract is re-downloaded once instead of blocking every later startup. When all three pre-built indexes are present, the raw datasets are not parsed at all, which makes warm starts fast.

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
| `CONFIG_PATH` | `/etc/cityfinder/config.json` | Config file to load. Use an absolute path: the loader resolves relative paths against a detected project root (a `go.mod` walk-up), which containers do not have. |

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

Defaults are sized for the v1.0 memory profile (memory request `10Gi`, limit `14Gi`, startup-probe budget 180 s). A single replica rolls without an outage window (`maxUnavailable: 0`, `maxSurge: 1`); scale to two or more replicas before enabling the (default-off) PodDisruptionBudget. Memory and startup-probe values are the two knobs to re-check whenever the index format changes. Validate any local customization with `helm lint` and `helm template ... | kubeconform -strict -summary`.

### Cluster adoption (Harbor / ArgoCD)

Registering the image in Harbor and creating the ArgoCD `Application` (auto-sync policy; the chart's manifests already carry `argocd.argoproj.io/sync-wave` annotations) is an operations handoff documented outside this repo. This repository ships the image and the chart, and intentionally contains nothing that talks to a cluster.