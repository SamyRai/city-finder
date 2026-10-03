// cmd/server/routes.go
package routes

import (
	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// healthzBody is the fixed liveness response, served without a per-probe
// marshal.
var healthzBody = []byte(`{"status":"ok"}`)

// SetupRoutes registers the data routes without a metrics registry (tests
// and embedded use); the metrics middleware and endpoint are skipped.
func SetupRoutes(app *fiber.App, mainFinder *finder.Finder) {
	SetupRoutesWithMetrics(app, mainFinder, nil)
}

// SetupRoutesWithMetrics registers the data routes plus, when reg is
// non-nil, a request-metrics middleware and the GET /metrics endpoint.
func SetupRoutesWithMetrics(app *fiber.App, mainFinder *finder.Finder, reg *metrics.Registry) {
	if reg != nil {
		app.Use(metricsMiddleware(reg))
	}
	// Handler panics are recovered HERE, inside the instrumentation, so the
	// metrics middleware above (and any access log further out) observes them
	// as the 500 the client receives. A recover registered outermost only
	// would let the panic unwind through every middleware unrecorded.
	app.Use(recover.New())

	// Liveness probe: registered before the data routes and never touches the
	// finders, so it stays cheap and answers even when data loading is slow
	// or the indexes are degraded.
	app.Get("/healthz", healthzHandler)

	if reg != nil {
		app.Get("/metrics", metricsHandler(mainFinder, reg))
	}

	app.Get("/nearest", nearestHandler(mainFinder))
	app.Post("/nearest/batch", batchHandler(mainFinder))
	app.Get("/coordinates", coordinatesHandler(mainFinder))
	app.Get("/autocomplete", autocompleteHandler(mainFinder))
	app.Get("/postalCode", postalCodeHandler(mainFinder))
}

// healthzHandler serves the liveness probe.
func healthzHandler(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return c.Send(healthzBody)
}
