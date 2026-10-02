package app

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/cmd/server/routes"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/gofiber/fiber/v2"
)

// HTTP benchmarks: one round-trip per route through fiber's app.Test, which
// serves the request over an in-memory connection — HTTP parsing, routing,
// handlers, JSON encoding and the response write, but NO TCP, TLS or kernel
// network stack. The absolute numbers therefore include app.Test's own
// per-call overhead (a goroutine and a pipe connection per request) and bound
// the application's in-process cost, not network-exposed latency.
//
// Every route runs two variants on the same fixture:
//
//   - core: bare fiber.New() + the routes — the handler and lookup cost.
//   - production: the exact stack cmd/server serves (app.New: production
//     fiber config incl. ETag, panic recovery, one access-log line per
//     request, request metrics). The access log is written to /dev/null — a
//     real file, so formatting and the write syscall are both measured
//     (log.Logger skips ALL work for io.Discard, which would hide the
//     logger's cost entirely); a terminal or container stdout is slower.
//
// production − core is the per-request middleware cost on this machine.

// benchCityCount sizes the fixture: large enough that the lookups behind the
// handlers are non-trivial, small enough to build in milliseconds. The core
// lookup costs at realistic scale are measured by the lib/finder benchmarks.
const benchCityCount = 10_000

// benchFinder builds the shared fixture: benchCityCount random-world cities
// with admin codes and names, and 1000 postal codes.
func benchFinder(b *testing.B) *finder.Finder {
	b.Helper()
	old := log.Writer()
	log.SetOutput(io.Discard) // index builds log per call
	defer log.SetOutput(old)

	rng := rand.New(rand.NewSource(11))
	cities := make([]city.SpatialCity, benchCityCount)
	admin1 := make(map[string]string)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:       fmt.Sprintf("Benchville %05d", i),
				Country:    "TC",
				Latitude:   rng.Float64()*180 - 90,
				Longitude:  rng.Float64()*360 - 180,
				Population: int32(1000 + i%90000),
			},
			Admin1Code: fmt.Sprintf("%02d", i%90),
			Admin2Code: fmt.Sprintf("%03d", i%900),
		}
		admin1[fmt.Sprintf("TC.%02d", i%90)] = fmt.Sprintf("Region %d", i%90)
	}
	s2f, err := coordinates.BuildIndex(cities)
	if err != nil {
		b.Fatal(err)
	}
	s2f.Admin1Names = admin1
	pf := postalCode.NewPostalCodeFinder()
	for i := 0; i < 1000; i++ {
		pf.AddPostalCode(dataLoader.PostalCodeEntry{
			CountryCode: "TC",
			PostalCode:  fmt.Sprintf("%05d", i),
			PlaceName:   fmt.Sprintf("Benchville %05d", i),
			Latitude:    40 + float64(i)/1000,
			Longitude:   -74 + float64(i)/1000,
			Accuracy:    1,
		})
	}
	return &finder.Finder{
		S2Finder:         s2f,
		NameFinder:       name.BuildIndex(cities),
		PostalCodeFinder: pf,
	}
}

// benchAccessLog returns the production access logger writing to /dev/null:
// a real file descriptor, unlike io.Discard (for which log.Logger skips
// formatting and writing altogether).
func benchAccessLog(b *testing.B) *log.Logger {
	b.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = f.Close() })
	return log.New(f, "", log.LstdFlags)
}

// benchVariants returns the core and production apps over one fixture.
func benchVariants(b *testing.B) []struct {
	name string
	app  *fiber.App
} {
	b.Helper()
	f := benchFinder(b)
	core := fiber.New()
	routes.SetupRoutes(core, f)
	reg := metrics.NewRegistry()
	return []struct {
		name string
		app  *fiber.App
	}{
		{"core", core},
		{"production", New(f, reg, benchAccessLog(b))},
	}
}

// benchRequest issues one request and requires a 200 so a routing or fixture
// regression fails the benchmark instead of measuring error responses. The
// request is built per call: its body is a consumable reader, so reusing one
// request would send an empty body from the second iteration on.
func benchRequest(b *testing.B, app *fiber.App, method, path string, body []byte) {
	b.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req)
	if err != nil {
		b.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		b.Fatalf("%s %s: status %d, want 200", method, path, resp.StatusCode)
	}
}

// runRouteBenchmark runs paths round-robin (precomputed, so no formatting
// happens inside the timed loop) against both variants.
func runRouteBenchmark(b *testing.B, method string, paths []string, body []byte) {
	for _, v := range benchVariants(b) {
		b.Run(v.name, func(b *testing.B) {
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				benchRequest(b, v.app, method, paths[i%len(paths)], body)
				i++
			}
		})
	}
}

// nearestPaths returns n /nearest URLs over seeded random global points with
// the given extra query string.
func nearestPaths(n int, extra string) []string {
	rng := rand.New(rand.NewSource(3))
	paths := make([]string, n)
	for i := range paths {
		paths[i] = fmt.Sprintf("/nearest?lat=%.5f&lon=%.5f%s", rng.Float64()*180-90, rng.Float64()*360-180, extra)
	}
	return paths
}

func BenchmarkHTTPNearest(b *testing.B) {
	runRouteBenchmark(b, "GET", nearestPaths(256, ""), nil)
}

func BenchmarkHTTPNearestAdmin(b *testing.B) {
	runRouteBenchmark(b, "GET", nearestPaths(256, "&include=admin"), nil)
}

func BenchmarkHTTPNearestPopulation(b *testing.B) {
	runRouteBenchmark(b, "GET", nearestPaths(256, "&rank=population"), nil)
}

// BenchmarkHTTPNearestBatch measures a 10-point batch through the
// parallel-execute path (two points carry include=admin).
func BenchmarkHTTPNearestBatch(b *testing.B) {
	var body bytes.Buffer
	body.WriteString(`{"points":[`)
	for i := 0; i < 10; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		include := ""
		if i%5 == 0 {
			include = `,"include":"admin"`
		}
		fmt.Fprintf(&body, `{"lat":%f,"lon":%f%s}`, 12.34+float64(i)*0.01, 56.78-float64(i)*0.01, include)
	}
	body.WriteString(`]}`)
	runRouteBenchmark(b, "POST", []string{"/nearest/batch"}, body.Bytes())
}

func BenchmarkHTTPCoordinates(b *testing.B) {
	paths := make([]string, 1024)
	for i := range paths {
		paths[i] = fmt.Sprintf("/coordinates?name=Benchville%%20%05d&country-code=TC", (i*97)%benchCityCount)
	}
	runRouteBenchmark(b, "GET", paths, nil)
}

func BenchmarkHTTPAutocomplete(b *testing.B) {
	paths := make([]string, 100)
	for i := range paths {
		paths[i] = fmt.Sprintf("/autocomplete?name=Benchville%%20%02d&country-code=TC", i)
	}
	runRouteBenchmark(b, "GET", paths, nil)
}

func BenchmarkHTTPPostalCode(b *testing.B) {
	paths := make([]string, 1000)
	for i := range paths {
		paths[i] = fmt.Sprintf("/postalCode?code=%05d&country-code=TC", (i*97)%1000)
	}
	runRouteBenchmark(b, "GET", paths, nil)
}

// BenchmarkHTTPMetrics measures a full production scrape: the runtime
// gauges, the registry render over the series the other routes' traffic
// leaves behind, and the response send. Only the production variant serves
// /metrics.
func BenchmarkHTTPMetrics(b *testing.B) {
	f := benchFinder(b)
	reg := metrics.NewRegistry()
	app := New(f, reg, benchAccessLog(b))
	// Populate the registry the way live traffic does, so the scrape renders
	// a realistic series set rather than an empty registry.
	for _, p := range append(nearestPaths(8, ""), "/healthz", "/autocomplete?name=Ben&country-code=TC", "/postalCode?code=00001&country-code=TC") {
		benchRequest(b, app, "GET", p, nil)
	}
	b.ReportAllocs()
	for b.Loop() {
		benchRequest(b, app, "GET", "/metrics", nil)
	}
}
