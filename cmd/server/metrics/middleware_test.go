package metrics

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// observed returns an app with the metrics middleware and a few routes.
func observed() (*fiber.App, *Registry) {
	reg := NewRegistry()
	app := fiber.New()
	app.Use(Middleware(reg))
	app.Get("/ok", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/teapot", func(c *fiber.Ctx) error { return c.Status(418).SendString("short") })
	app.Get("/fail", func(c *fiber.Ctx) error { return fiber.ErrBadRequest })
	app.Get("/boom", func(c *fiber.Ctx) error { return assert.AnError })
	app.Get("/metrics", func(c *fiber.Ctx) error { return c.SendString(reg.Render()) })
	return app, reg
}

func do(t *testing.T, app *fiber.App, method, path string) int {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestMiddlewareRecordsCompletedStatus: the status counted is the one the
// client receives, including statuses fiber's error handler writes only
// after the middleware chain has returned.
func TestMiddlewareRecordsCompletedStatus(t *testing.T) {
	app, reg := observed()
	assert.Equal(t, 200, do(t, app, "GET", "/ok"))
	assert.Equal(t, 418, do(t, app, "GET", "/teapot"))
	assert.Equal(t, 400, do(t, app, "GET", "/fail"))
	assert.Equal(t, 500, do(t, app, "GET", "/boom"), "a plain error is a 500")

	out := reg.Render()
	assert.Contains(t, out, `http_requests_total{path="/ok",status="200"} 1`)
	assert.Contains(t, out, `http_requests_total{path="/teapot",status="418"} 1`)
	assert.Contains(t, out, `http_requests_total{path="/fail",status="400"} 1`)
	assert.Contains(t, out, `http_requests_total{path="/boom",status="500"} 1`)
}

// TestMiddlewareLabelsUnroutedAndSkipsScrapes: 404/405 from the router share
// one "(unrouted)" series instead of masquerading as route "/", and scrapes
// of /metrics never count themselves.
func TestMiddlewareLabelsUnroutedAndSkipsScrapes(t *testing.T) {
	app, reg := observed()
	assert.Equal(t, 404, do(t, app, "GET", "/nope"))
	assert.Equal(t, 405, do(t, app, "POST", "/ok"))
	assert.Equal(t, 200, do(t, app, "GET", "/metrics"))

	out := reg.Render()
	assert.Contains(t, out, `http_requests_total{path="(unrouted)",status="404"} 1`)
	assert.Contains(t, out, `http_requests_total{path="(unrouted)",status="405"} 1`)
	assert.NotContains(t, out, `path="/metrics"`)
	assert.NotContains(t, out, `path="/"`)
}
