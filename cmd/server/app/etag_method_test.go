package app

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestETagOnlyForGetAndHead pins RFC 9110 13.1.2 scoping: validators and
// 304s apply to GET/HEAD here. A POST (e.g. /nearest/batch) whose
// If-None-Match happens to match the tag of its 200 response must be served
// in full with no ETag, never turned into a 304.
func TestETagOnlyForGetAndHead(t *testing.T) {
	a := fiber.New()
	a.Use(ETag())
	h := func(c *fiber.Ctx) error { return c.SendString("payload") }
	a.Get("/r", h)
	a.Post("/r", h)
	a.Put("/r", h)
	a.Delete("/r", h)
	a.Patch("/r", h)

	resp, err := a.Test(httptest.NewRequest("GET", "/r", nil))
	require.NoError(t, err)
	tag := resp.Header.Get("ETag")
	require.NotEmpty(t, tag)

	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		req := httptest.NewRequest(method, "/r", nil)
		req.Header.Set("If-None-Match", tag)
		resp, err := a.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 200, resp.StatusCode, method)
		assert.Empty(t, resp.Header.Get("ETag"), method)
	}

	head := httptest.NewRequest("HEAD", "/r", nil)
	head.Header.Set("If-None-Match", tag)
	resp, err = a.Test(head)
	require.NoError(t, err)
	assert.Equal(t, 304, resp.StatusCode, "HEAD keeps validator handling")
}
