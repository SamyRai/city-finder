package routes

import (
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMetricsApp wires SetupRoutesWithMetrics over the same fixture
// setupTestApp uses, for tests that exercise the metrics surface.
func setupMetricsApp(t *testing.T) *fiber.App {
	t.Helper()
	s2f, err := coordinates.BuildIndex(adminRouteCities)
	require.NoError(t, err)
	s2f.Admin1Names = adminRouteNames
	app := fiber.New()
	SetupRoutesWithMetrics(app, &finder.Finder{S2Finder: s2f}, metrics.NewRegistry())
	return app
}

// setupNameApp wires a finder that also carries a name index, for the
// /coordinates handler tests.
func setupNameApp(t *testing.T) *fiber.App {
	t.Helper()
	s2f, err := coordinates.BuildIndex(adminRouteCities)
	require.NoError(t, err)
	app := fiber.New()
	SetupRoutes(app, &finder.Finder{S2Finder: s2f, NameFinder: name.BuildIndex(adminRouteCities)})
	return app
}

func TestCoordinates_NameTooLong(t *testing.T) {
	app := setupNameApp(t)

	status, body := get(t, app, "/coordinates?name="+strings.Repeat("x", 201)+"&country-code=US")
	assert.Equal(t, 400, status)
	assert.Contains(t, body, "Name too long")

	// The boundary itself is accepted (validation runs before lookup).
	status, _ = get(t, app, "/coordinates?name="+strings.Repeat("x", 200)+"&country-code=US")
	assert.Equal(t, 404, status, "a 200-rune name passes the cap and fails as a normal miss")
}

func TestNearest_PopulationSaturationSheds503(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	// Fill every population gate slot: the next rank=population query must
	// shed with 503 + Retry-After instead of queueing behind the scans.
	for i := 0; i < populationGateConcurrency(); i++ {
		populationGate <- struct{}{}
	}
	req := httptest.NewRequest("GET", "/nearest?lat=37.77&lon=-122.41&rank=population", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 503, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get("Retry-After"))
	for i := 0; i < populationGateConcurrency(); i++ {
		<-populationGate
	}

	// With the gate drained the same query succeeds.
	status, _ := get(t, app, "/nearest?lat=37.77&lon=-122.41&rank=population")
	assert.Equal(t, 200, status)
}

func TestMetricsEndpoint(t *testing.T) {
	app := setupMetricsApp(t)

	status, _ := get(t, app, "/healthz")
	require.Equal(t, 200, status)
	status, _ = get(t, app, "/nearest?lat=37.77&lon=-122.41")
	require.Equal(t, 200, status)

	resp, err := app.Test(httptest.NewRequest("GET", "/metrics", nil))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := string(body)

	assert.Contains(t, out, `http_requests_total{path="/healthz",status="200"} 1`)
	assert.Contains(t, out, `http_requests_total{path="/nearest",status="200"} 1`)
	assert.Contains(t, out, `http_request_duration_seconds_count{path="/nearest"} 1`)
	// The scrape must not count itself.
	assert.NotContains(t, out, `path="/metrics"`)
	// The fuzzy build-state gauge is always present on the metrics surface
	// (0 = not built here; the fixture never triggers a fuzzy build).
	assert.Contains(t, out, "# TYPE fuzzy_build_state gauge\nfuzzy_build_state 0")
	// Runtime gauges are computed per scrape: goroutine count and live heap.
	assert.Contains(t, out, "# TYPE go_goroutines gauge\ngo_goroutines ")
	assert.Contains(t, out, "# TYPE go_heap_alloc_bytes gauge\ngo_heap_alloc_bytes ")

	// SetupRoutes with a nil registry keeps serving without the metrics
	// surface (tests and embedded use).
	bare := setupTestApp(t, adminRouteNames)
	resp, err = bare.Test(httptest.NewRequest("GET", "/metrics", nil))
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode, "no /metrics route may exist without a registry")
}

// TestMetricsEndpoint_ConcurrentScrapes pins scrape-path safety: parallel
// scrapes share the registry (mutex-guarded) and the runtime-gauge read,
// which writes into a sample slice. The gauge slice was once a shared
// package-level variable — a data race across concurrent scrapes that serial
// tests cannot see (found by review, 2026-10-02); the slice is now
// scrape-local. Run under -race this test fails on any shared scrape state.
func TestMetricsEndpoint_ConcurrentScrapes(t *testing.T) {
	app := setupMetricsApp(t)

	const scrapers, perScraper = 8, 25
	var wg sync.WaitGroup
	wg.Add(scrapers)
	for i := 0; i < scrapers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perScraper; j++ {
				resp, err := app.Test(httptest.NewRequest("GET", "/metrics", nil))
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Errorf("scrape status %d, want 200", resp.StatusCode)
					return
				}
			}
		}()
	}
	wg.Wait()
}
