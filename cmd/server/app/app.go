// Package app assembles the production HTTP application: the fiber
// configuration, the middleware chain, and the routes. cmd/server builds its
// app here, and so do the HTTP benchmarks' "production" variants — one owner
// for the serving stack, so a benchmark that claims to measure what a request
// pays in production measures exactly the stack that serves traffic.
package app

import (
	"log"
	"time"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/cmd/server/routes"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// Config returns the production fiber configuration.
func Config() fiber.Config {
	return fiber.Config{
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
		BodyLimit:    1 << 20, // 1MB; POST /nearest/batch peaks at ~6KB for 100 points
		ETag:         true,
		// fasthttp's default admission control (256k) is effectively
		// unbounded: each accepted connection costs a goroutine plus
		// buffers, so a flood ties up memory the multi-GB index heap cannot
		// spare. 1024 concurrent connections is far above any legitimate
		// load for this API and caps the per-connection overhead.
		Concurrency: 1024,
	}
}

// New returns the production app: panic recovery, one access-log line per
// request on requestLog, request metrics on reg (nil disables the metrics
// middleware and GET /metrics), and every data route over f.
func New(f *finder.Finder, reg *metrics.Registry, requestLog *log.Logger) *fiber.App {
	a := fiber.New(Config())
	// fasthttp performs no panic recovery of its own: without this middleware
	// any handler panic terminates the process. It must be registered before
	// all other middleware so it wraps the full handler chain.
	a.Use(recover.New())
	a.Use(RequestLogger(requestLog))
	routes.SetupRoutesWithMetrics(a, f, reg)
	return a
}

// RequestLogger writes a single line per request to l: timestamp, method,
// path (query string excluded), status, latency, and response size in bytes.
// Request bodies, query parameters, and multipart forms are never logged.
func RequestLogger(l *log.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		l.Printf("%s %s %s %d %s %d",
			start.Format(time.RFC3339),
			c.Method(),
			c.Path(),
			c.Response().StatusCode(),
			time.Since(start),
			len(c.Response().Body()),
		)
		return err
	}
}
