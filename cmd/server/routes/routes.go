// Package routes registers the data endpoints. Middleware (panic recovery,
// access log, ETag, request metrics) is owned by package app, which fixes
// the order; this package only owns the handlers and the state they share.
package routes

import (
	"runtime"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/gofiber/fiber/v2"
)

// healthzBody is the fixed liveness response, served without a per-probe
// marshal.
var healthzBody = []byte(`{"status":"ok"}`)

// Handlers owns everything the data handlers share: the finder, the
// population gate, the batch fan-out limit and the per-point query core.
// Nothing here is process-global, so two apps in one process (tests,
// benchmarks) never contend on the same gate.
type Handlers struct {
	finder *finder.Finder

	// gate bounds how many rank=population queries may execute at once.
	// Each query escalates search discs under an anytime bound; a far-from
	// land (mid-ocean) point escalates to a full-index scan that allocates a
	// multi-hundred-MB result slice and can run for seconds — N simultaneous
	// such queries is a memory-spike/OOM vector, so saturation sheds load
	// with 503 + Retry-After instead of queueing unbounded work. Cheap
	// (land) population queries hold a slot only for microseconds-to-
	// milliseconds and do not saturate the gate in practice.
	gate chan struct{}

	// maxBatchWorkers bounds the POST /nearest/batch fan-out. CPU-bound
	// lookups gain nothing beyond core count, and the gate independently
	// caps the expensive class, so one worker per P (capped by the batch
	// size) is the honest ceiling.
	maxBatchWorkers int

	// execute is the per-point query core of the batch handler: the
	// Handlers' own executeNearest, replaceable in tests that need a
	// failing core.
	execute nearestExecutor
}

// New returns the handlers over f with the production gate size and batch
// fan-out.
func New(f *finder.Finder) *Handlers {
	h := &Handlers{
		finder:          f,
		gate:            make(chan struct{}, gateSize(runtime.GOMAXPROCS(0))),
		maxBatchWorkers: runtime.GOMAXPROCS(0),
	}
	h.execute = h.executeNearest
	return h
}

// SetupRoutes registers the data routes without a metrics endpoint (tests
// and embedded use) and returns the handlers that own their shared state.
func SetupRoutes(app *fiber.App, mainFinder *finder.Finder) *Handlers {
	return SetupRoutesWithMetrics(app, mainFinder, nil)
}

// SetupRoutesWithMetrics registers the data routes plus, when reg is
// non-nil, the GET /metrics endpoint. Recording requests is the metrics
// middleware's job (metrics.Middleware, registered by package app).
func SetupRoutesWithMetrics(app *fiber.App, mainFinder *finder.Finder, reg *metrics.Registry) *Handlers {
	h := New(mainFinder)
	h.Register(app, reg)
	return h
}

// Register adds the routes to app.
func (h *Handlers) Register(app *fiber.App, reg *metrics.Registry) {
	// Liveness probe: registered before the data routes and never touches the
	// finders, so it stays cheap and answers even when data loading is slow
	// or the indexes are degraded.
	app.Get("/healthz", healthzHandler)

	if reg != nil {
		app.Get("/metrics", metricsHandler(h.finder, reg))
	}

	app.Get("/nearest", h.nearest)
	app.Post("/nearest/batch", h.batch)
	app.Get("/coordinates", coordinatesHandler(h.finder))
	app.Get("/autocomplete", autocompleteHandler(h.finder))
	app.Get("/postalCode", postalCodeHandler(h.finder))
}

// healthzHandler serves the liveness probe.
func healthzHandler(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return c.Send(healthzBody)
}
