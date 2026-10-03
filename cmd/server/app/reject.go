package app

import (
	"log"
	"time"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/gofiber/fiber/v2"
)

// rejectedLabel is the metrics path label of requests the HTTP server
// refused before routing (413 body too large, 431 headers too large, 408
// read timeout, malformed request): one bounded series, never user input.
const rejectedLabel = "(rejected)"

// chainEnteredKey marks a request context as having entered the middleware
// chain. fiber calls the app's ErrorHandler both for errors returned by the
// chain (already observed by the middleware, which runs first) and for
// requests fasthttp rejects before any middleware runs; the mark tells the
// two apart.
type chainEnteredKey struct{}

// errorHandler wraps fiber's default error handler. Requests rejected before
// routing never reach the metrics middleware or the access logger, so they
// would be invisible; they are recorded here under rejectedLabel and as one
// access-log line. The method and path are logged as "-": a request that
// failed to parse has no trustworthy ones, and its raw bytes are never
// echoed. reg may be nil (no metrics).
func errorHandler(reg *metrics.Registry, requestLog *log.Logger) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		if c.Locals(chainEnteredKey{}) == nil {
			status := metrics.CompletedStatus(c, err)
			if reg != nil {
				reg.ObserveRequest(rejectedLabel, status, 0)
			}
			logRejected(requestLog, status)
		}
		return fiber.DefaultErrorHandler(c, err)
	}
}

// logRejected writes the access-log line of a pre-routing rejection.
func logRejected(l *log.Logger, status int) {
	b := appendAccessLine(nil, time.Now(), "-", "-", status, 0, 0)
	_ = l.Output(2, string(b))
}
