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

Nearest-neighbor queries are bounded by `s2.ClosestEdgeQuery` with `MaxResults(1)`, which prunes the search instead of collecting a result for every indexed point. Measured on an Apple-silicon laptop (8 cores), 100,000 distinct points:

| Metric | Per query |
|---|---|
| Latency | ~5 µs |
| Allocations | ~1.6 KB / 60 allocs |

The ShapeIndex is built eagerly at startup, so the first query after boot does not pay the construction cost. Results are validated against a brute-force great-circle oracle in `s2_oracle_test.go`.

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
- **Find Nearest City**: `/nearest?lat=<latitude>&lon=<longitude>` — lat/lon must be finite and in range (`[-90, 90]` / `[-180, 180]`); anything else returns 400. Returns the city plus `distance_km` (great-circle, 2 decimals).
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