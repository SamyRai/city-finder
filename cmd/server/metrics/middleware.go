package metrics

import (
	"time"

	"github.com/gofiber/fiber/v2"
)

// Middleware records every request into reg (route pattern, completed
// status, latency), except scrapes of /metrics itself. Register it OUTSIDE
// the recover middleware so a recovered handler panic is observed as the 500
// the client receives.
func Middleware(reg *Registry) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		if pattern := RouteLabel(c, err); pattern != "/metrics" { // a scrape must not grow its own counts
			reg.ObserveRequest(pattern, CompletedStatus(c, err), time.Since(start))
		}
		return err
	}
}
