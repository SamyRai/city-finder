package app

import (
	"bytes"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewWiresProductionStack pins the middleware chain cmd/server serves:
// every request writes one access-log line (query string excluded), routed
// requests are counted on the metrics registry, and /metrics is mounted.
func TestNewWiresProductionStack(t *testing.T) {
	var logBuf bytes.Buffer
	reg := metrics.NewRegistry()
	a := New(&finder.Finder{}, reg, log.New(&logBuf, "", 0))

	resp, err := a.Test(httptest.NewRequest("GET", "/healthz?secret=x", nil))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, logBuf.String(), "GET /healthz 200")
	assert.NotContains(t, logBuf.String(), "secret", "query strings must never be logged")

	resp, err = a.Test(httptest.NewRequest("GET", "/metrics", nil))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, strings.Contains(string(body), `path="/healthz"`), "the healthz request must be counted: %s", body)
}

// TestNewRecoversHandlerPanics pins that recover is in the chain: a panicking
// handler yields a 500 instead of terminating the process.
func TestNewRecoversHandlerPanics(t *testing.T) {
	a := New(&finder.Finder{}, nil, log.New(io.Discard, "", 0))
	a.Get("/panic", func(c *fiber.Ctx) error { panic("boom") })
	resp, err := a.Test(httptest.NewRequest("GET", "/panic", nil))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}
