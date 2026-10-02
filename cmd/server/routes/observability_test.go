package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// observedApp wires the routes with a metrics registry and one extra route
// that panics, registered after setup so the middleware applies to it.
func observedApp(t *testing.T) (*fiber.App, *metrics.Registry) {
	t.Helper()
	s2f, err := coordinates.BuildIndex(adminRouteCities)
	require.NoError(t, err)
	reg := metrics.NewRegistry()
	app := fiber.New()
	SetupRoutesWithMetrics(app, &finder.Finder{S2Finder: s2f}, reg)
	app.Get("/boom", func(c *fiber.Ctx) error { panic("boom") })
	return app, reg
}

func status(t *testing.T, app *fiber.App, method, path string) int {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestMetricsRecordErrorStatusesAndUnroutedRequests pins that requests ending
// in a fiber error are counted with the status the client received. Fiber's
// error handler runs after the middleware chain, so reading the response
// status inside the middleware saw the default 200: unrouted 404 floods were
// counted as healthy 200s under the label "/".
func TestMetricsRecordErrorStatusesAndUnroutedRequests(t *testing.T) {
	app, reg := observedApp(t)
	require.Equal(t, 404, status(t, app, "GET", "/no-such-route"))
	require.Equal(t, 404, status(t, app, "GET", "/a/b/c"))
	require.Equal(t, 405, status(t, app, "DELETE", "/nearest"))

	out := reg.Render()
	assert.Contains(t, out, `http_requests_total{path="(unrouted)",status="404"} 2`)
	assert.Contains(t, out, `status="405"`)
	assert.NotContains(t, out, `status="200"`, "no request in this test succeeded")
	assert.NotContains(t, out, `path="/"`, "the catch-all middleware path is not a route label")
}

// TestMetricsRecordHandlerPanics pins that a panicking handler is recovered
// INSIDE the instrumentation: the client gets a 500 and the request is
// counted as one.
func TestMetricsRecordHandlerPanics(t *testing.T) {
	app, reg := observedApp(t)
	require.Equal(t, 500, status(t, app, "GET", "/boom"))
	assert.Contains(t, reg.Render(), `http_requests_total{path="/boom",status="500"} 1`)
}

// TestBatchWorkersNeverExceedThePopulationGate pins that a single batch
// cannot saturate the population gate on its own. Workers used to scale with
// GOMAXPROCS while the gate is capped at 8 and fails fast, so on a host with
// more than 8 cores one population batch on an idle server could 503 itself.
func TestBatchWorkersNeverExceedThePopulationGate(t *testing.T) {
	saved := maxBatchWorkers
	t.Cleanup(func() { maxBatchWorkers = saved })
	maxBatchWorkers = 64 // a large host
	for _, n := range []int{1, 7, 8, 9, 100} {
		got := batchWorkerCount(n, true)
		assert.LessOrEqual(t, got, cap(populationGate), "n=%d with population points", n)
		assert.GreaterOrEqual(t, got, 1)
		assert.LessOrEqual(t, got, n)
	}
	assert.Equal(t, 64, batchWorkerCount(100, false), "distance-only batches keep the full fan-out")
}
