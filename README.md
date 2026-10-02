# cityFinder

Nearest-city lookup over the full [GeoNames](https://www.geonames.org/) dataset
(13.47M places), as a Go library and a small HTTP service. Geometry is
[S2](https://github.com/golang/geo): geodesic distances on the sphere, with
nearest-neighbour queries checked against a brute-force oracle.

- **Nearest city** to a coordinate, ranked by distance or by a
  population-weighted gravity model (exact over the whole dataset).
- **Administrative region** of the winning city (admin1/admin2 codes and
  names).
- **City by name**: exact, then fuzzy (≤ 2 edits), plus prefix autocomplete.
- **City by postal code.**
- Batch queries, Prometheus metrics, graceful shutdown, container image and
  Helm chart.

Measured on the full dataset (Apple Silicon, in-process, v1.3): nearest by
distance p50 9.4 µs / p99 78 µs; exact name ~3 µs; postal < 1 µs; ~4.5 GB
heap. The method, per-version history and limits are in
[docs/performance.md](docs/performance.md). These are single-client library
latencies, not latency under load (measure that with `make loadtest`).

## Quick start

### HTTP service (Docker)

```bash
docker build -t city-finder .
docker run -d -p 3000:3000 -v city-finder-data:/data --memory 14g city-finder
# first boot downloads ~420 MB and builds indexes (~6–8 min); later boots ~20–25 s
curl 'http://localhost:3000/nearest?lat=48.8566&lon=2.3522'
curl 'http://localhost:3000/coordinates?name=Pars&country-code=FR'   # typo → Paris
```

### From source

```bash
go build -o nearestcityserver ./cmd/server
./nearestcityserver            # reads ./config.json (or $CONFIG_PATH), listens on $PORT (3000)
```

### As a library

```bash
go get github.com/SamyRai/cityFinder
```

```go
cfg, err := config.LoadConfig("config.json")
if err != nil {
    log.Fatal(err)
}
f, err := initializer.Initialize(cfg) // downloads datasets / builds indexes on first use
if err != nil {
    log.Fatal(err)
}
f.WarmFuzzy() // optional: build the typo index in the background now

c, km, err := f.FindNearestCity(40.7128, -74.0060, coordinates.RankDistance)
if err != nil {
    log.Fatal(err)
}
fmt.Printf("%s, %s (%.1f km)\n", c.Name, c.Country, km)

if c := f.FindCityByName("Pars", "FR"); c != nil { /* Paris */ }
if c := f.FindCityByPostalCode("10001", "US"); c != nil { /* New York */ }
```

Packages: `lib/config`, `lib/initializer`, `lib/finder`,
`lib/finder/coordinates`.

## API

| Endpoint | |
|---|---|
| `GET /nearest?lat&lon[&rank=distance\|population][&include=admin]` | nearest city |
| `POST /nearest/batch` | 1–100 points |
| `GET /coordinates?name&country-code` | city by name (exact, then fuzzy) |
| `GET /autocomplete?name&country-code[&limit]` | name prefix search |
| `GET /postalCode?code&country-code` | city by postal code |
| `GET /healthz`, `GET /metrics` | liveness, Prometheus metrics |

Full reference: [docs/api.md](docs/api.md) and
[docs/openapi.yaml](docs/openapi.yaml).

## Development

```bash
make test          # go test ./...
make test-race     # with the race detector (what CI runs)
make bench-smoke   # every benchmark once — correctness, not numbers
make bench-ab BASE=origin/main PKG=./lib/finder/name BENCH=CityByName
make loadtest URL=http://127.0.0.1:3000 RATES=500,1000,2000   # open-model load sweep
make build-test    # build indexes from the small test fixture
```

CI also runs gofmt, `go vet`, staticcheck, govulncheck, `go mod tidy -diff`,
and helm lint + kubeconform. Any performance claim in a PR follows the
protocol in [docs/benchmarking.md](docs/benchmarking.md): interleaved A/B on
one machine, benchstat, a pinned toolchain, and validation at the layer the
claim is about.

## Documentation

[docs/](docs/README.md): [configuration](docs/configuration.md) ·
[deployment](docs/deployment.md) · [architecture](docs/architecture.md) ·
[performance](docs/performance.md) · [benchmarking](docs/benchmarking.md) ·
[changelog](CHANGELOG.md)

## License

MIT, see [LICENSE](LICENSE). Data: [GeoNames](https://www.geonames.org/)
(CC BY 4.0). Geometry: [golang/geo](https://github.com/golang/geo).
