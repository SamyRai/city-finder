// Package app assembles the production HTTP application: the fiber
// configuration, the middleware chain, and the routes. cmd/server builds its
// app here, and so do the HTTP benchmarks' "production" variants — one owner
// for the serving stack, so a benchmark that claims to measure what a request
// pays in production measures exactly the stack that serves traffic.
package app

import (
	"log"
	"strconv"
	"sync"
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
		// ETag is the ETag() middleware (same tags, same 304s), not fiber's
		// deprecated Config.ETag, which rebuilds a CRC table per response.
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
	a.Use(ETag())
	routes.SetupRoutesWithMetrics(a, f, reg)
	return a
}

// RequestLogger writes a single line per request to l: timestamp, method,
// path (query string excluded), status, latency, and response size in bytes.
// The status is the one the client receives, including router 404/405s and
// recovered panics (500), which fiber's error handler writes only after the
// middleware chain has returned.
// Request bodies, query parameters, and multipart forms are never logged.
func RequestLogger(l *log.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		bp := accessLineBuf.Get().(*[]byte)
		*bp = appendAccessLine((*bp)[:0], start, c.Method(), c.Path(),
			routes.CompletedStatus(c, err), // the status the client receives, incl. 404/405/500
			time.Since(start), len(c.Response().Body()))
		_ = l.Output(2, string(*bp))
		accessLineBuf.Put(bp)
		return err
	}
}

// accessLineBuf recycles access-log line buffers across requests.
var accessLineBuf = sync.Pool{New: func() any { b := make([]byte, 0, 128); return &b }}

// appendAccessLine appends one access-log line, byte-identical to
// fmt.Sprintf("%s %s %s %d %s %d", start.Format(time.RFC3339), method, path,
// status, elapsed, size) but without boxing six arguments per request.
func appendAccessLine(b []byte, start time.Time, method, path string, status int, elapsed time.Duration, size int) []byte {
	b = start.AppendFormat(b, time.RFC3339)
	b = append(b, ' ')
	b = append(b, method...)
	b = append(b, ' ')
	b = append(b, path...)
	b = append(b, ' ')
	b = strconv.AppendInt(b, int64(status), 10)
	b = append(b, ' ')
	b = append(b, elapsed.String()...)
	b = append(b, ' ')
	return strconv.AppendInt(b, int64(size), 10)
}
