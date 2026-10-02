package routes

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net/http/httptest"
	"testing"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/gofiber/fiber/v2"
)

// benchCityCount sizes the HTTP benchmark fixture: large enough that the S2
// and name lookups behind the handlers are non-trivial, small enough that
// every benchmark builds its app in milliseconds. At this size the measured
// per-request cost is dominated by the framework + serialization shell, with
// the core lookup costs measured separately by the lib/finder benchmarks.
const benchCityCount = 10_000

// benchFixture is the shared state behind the HTTP benchmark apps.
type benchFixture struct {
	cities    []city.SpatialCity
	admin1    map[string]string
	finder    *finder.Finder
	postalHit string
}

func newBenchFixture(b *testing.B) *benchFixture {
	b.Helper()
	rng := rand.New(rand.NewSource(11))
	cities := make([]city.SpatialCity, benchCityCount)
	admin1 := make(map[string]string)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:       fmt.Sprintf("Benchville %05d", i),
				Country:    "TC",
				Latitude:   (rng.Float64()*180 - 90),
				Longitude:  (rng.Float64()*360 - 180),
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
	return &benchFixture{
		cities: cities,
		admin1: admin1,
		finder: &finder.Finder{
			S2Finder:         s2f,
			NameFinder:       name.BuildIndex(cities),
			PostalCodeFinder: pf,
		},
		postalHit: "00432",
	}
}

// benchApp wires the fixture into a Fiber app, optionally with a metrics
// registry (the /metrics benchmarks need one; the others measure the plain
// routes without middleware).
func benchApp(b *testing.B, f *benchFixture, reg *metrics.Registry) *fiber.App {
	b.Helper()
	app := fiber.New()
	SetupRoutesWithMetrics(app, f.finder, reg)
	return app
}

// benchGet issues one GET and requires a 200 so a routing or fixture
// regression fails the benchmark instead of measuring 404s.
func benchGet(b *testing.B, app *fiber.App, path string) {
	b.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", path, nil))
	if err != nil {
		b.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		b.Fatalf("GET %s: status %d, want 200", path, resp.StatusCode)
	}
}

// The three points below are fixed fixtures so every run measures the same
// lookups; they sit amid the random point cloud, so all ranks resolve on
// land-shaped escalations.
const (
	benchLat = 12.34
	benchLon = 56.78
)

func BenchmarkHTTPNearest(b *testing.B) {
	f := newBenchFixture(b)
	app := benchApp(b, f, nil)
	b.ReportAllocs()
	for b.Loop() {
		benchGet(b, app, fmt.Sprintf("/nearest?lat=%f&lon=%f", benchLat, benchLon))
	}
}

func BenchmarkHTTPNearestAdmin(b *testing.B) {
	f := newBenchFixture(b)
	app := benchApp(b, f, nil)
	b.ReportAllocs()
	for b.Loop() {
		benchGet(b, app, fmt.Sprintf("/nearest?lat=%f&lon=%f&include=admin", benchLat, benchLon))
	}
}

func BenchmarkHTTPNearestPopulation(b *testing.B) {
	f := newBenchFixture(b)
	app := benchApp(b, f, nil)
	b.ReportAllocs()
	for b.Loop() {
		benchGet(b, app, fmt.Sprintf("/nearest?lat=%f&lon=%f&rank=population", benchLat, benchLon))
	}
}

// BenchmarkHTTPNearestBatch measures a 10-point batch through the
// parallel-execute path (two points carry include=admin).
func BenchmarkHTTPNearestBatch(b *testing.B) {
	f := newBenchFixture(b)
	app := benchApp(b, f, nil)
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
		fmt.Fprintf(&body, `{"lat":%f,"lon":%f%s}`, benchLat+float64(i)*0.01, benchLon-float64(i)*0.01, include)
	}
	body.WriteString(`]}`)
	payload := body.Bytes()
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest("POST", "/nearest/batch", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			b.Fatalf("batch: status %d, want 200", resp.StatusCode)
		}
	}
}

func BenchmarkHTTPCoordinates(b *testing.B) {
	f := newBenchFixture(b)
	app := benchApp(b, f, nil)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		benchGet(b, app, fmt.Sprintf("/coordinates?name=Benchville%%20%05d&country-code=TC", (i*97)%benchCityCount))
		i++
	}
}

func BenchmarkHTTPAutocomplete(b *testing.B) {
	f := newBenchFixture(b)
	app := benchApp(b, f, nil)
	b.ReportAllocs()
	for b.Loop() {
		benchGet(b, app, "/autocomplete?name=Benchville%2000&country-code=TC")
	}
}

func BenchmarkHTTPPostalCode(b *testing.B) {
	f := newBenchFixture(b)
	app := benchApp(b, f, nil)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		benchGet(b, app, fmt.Sprintf("/postalCode?code=%s&country-code=TC", f.postalHit))
		i++
	}
}

// BenchmarkHTTPMetrics measures a full scrape: the runtime gauges, the
// registry render, and the response send.
func BenchmarkHTTPMetrics(b *testing.B) {
	f := newBenchFixture(b)
	reg := metrics.NewRegistry()
	reg.ObserveRequest("/nearest", 200, 250_000)
	reg.SetGauge("fuzzy_build_state", 2)
	app := benchApp(b, f, reg)
	b.ReportAllocs()
	for b.Loop() {
		benchGet(b, app, "/metrics")
	}
}
