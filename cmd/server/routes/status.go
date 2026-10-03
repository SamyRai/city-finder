package routes

import (
	"errors"

	"github.com/gofiber/fiber/v2"
)

// CompletedStatus returns the HTTP status a request ends with. A handler (or
// fiber's router, for 404/405) may return an error that fiber's error handler
// turns into the response AFTER the middleware chain has unwound, so inside a
// middleware the response still carries the default 200; the error's code is
// the status the client actually receives.
func CompletedStatus(c *fiber.Ctx, err error) int {
	if err == nil {
		return c.Response().StatusCode()
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return fiber.StatusInternalServerError
}

// RouteLabel returns the route pattern a request matched, for metrics and
// logs. When no route matches, fiber's router answers 404/405 and reports the
// catch-all middleware ("/") as the request's route; that case is labelled
// "(unrouted)" so 404/405 floods stay one bounded series instead of
// masquerading as a "/" route (this API registers no "/" route). err is the
// error the handler chain returned.
func RouteLabel(c *fiber.Ctx, err error) string {
	path := c.Route().Path
	var fe *fiber.Error
	if path == "/" && errors.As(err, &fe) &&
		(fe.Code == fiber.StatusNotFound || fe.Code == fiber.StatusMethodNotAllowed) {
		return "(unrouted)"
	}
	return path
}
