package app

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestRequestLoggerRecordsErrorAndPanicStatuses pins that the access log
// carries the status the client received: router 404/405 and recovered
// panics used to be logged as 200.
func TestRequestLoggerRecordsErrorAndPanicStatuses(t *testing.T) {
	var logBuf bytes.Buffer
	a := New(&finder.Finder{}, metrics.NewRegistry(), log.New(&logBuf, "", 0))
	a.Get("/panic", func(c *fiber.Ctx) error { panic("boom") })
	for path, want := range map[string]int{"/no-such-route": 404, "/panic": 500} {
		resp, err := a.Test(httptest.NewRequest("GET", path, nil))
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, want, resp.StatusCode, path)
	}
	assert.Contains(t, logBuf.String(), "GET /no-such-route 404")
	assert.Contains(t, logBuf.String(), "GET /panic 500")
}

// TestAppendAccessLineMatchesPrintfFormat: the hand-built access-log line is
// byte-identical to the format it replaced, for every field shape.
func TestAppendAccessLineMatchesPrintfFormat(t *testing.T) {
	zones := []*time.Location{time.UTC, time.FixedZone("x", 5*3600+1800), time.FixedZone("y", -7*3600)}
	for i, tc := range []struct {
		method, path string
		status, size int
		elapsed      time.Duration
	}{
		{"GET", "/nearest", 200, 81, 1234567 * time.Nanosecond},
		{"POST", "/nearest/batch", 503, 0, 3 * time.Second},
		{"GET", "/", 404, 9, 0},
		{"DELETE", "/a b/%C3%A9", 405, 1 << 20, 999 * time.Nanosecond},
		{"GET", "", 500, 21, 90 * time.Minute},
	} {
		start := time.Date(2026, 10, 3, 1, 2, 3, 456789, zones[i%len(zones)])
		want := fmt.Sprintf("%s %s %s %d %s %d", start.Format(time.RFC3339), tc.method, tc.path, tc.status, tc.elapsed, tc.size)
		assert.Equal(t, want, string(appendAccessLine(nil, start, tc.method, tc.path, tc.status, tc.elapsed, tc.size)))
	}
}
