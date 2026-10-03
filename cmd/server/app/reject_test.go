package app

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncLog is a goroutine-safe log sink: the server writes from its own
// goroutines while the test reads.
type syncLog struct{ b chan []byte }

func newSyncLog() *syncLog { return &syncLog{b: make(chan []byte, 64)} }

func (s *syncLog) Write(p []byte) (int, error) {
	s.b <- append([]byte(nil), p...)
	return len(p), nil
}

// drain returns everything logged so far.
func (s *syncLog) drain() string {
	var sb strings.Builder
	for {
		select {
		case p := <-s.b:
			sb.Write(p)
		default:
			return sb.String()
		}
	}
}

// TestRejectedRequestsAreObserved pins that requests the HTTP server rejects
// before routing (413 body too large, 431 headers too large) appear in
// /metrics under the "(rejected)" label and in the access log, instead of
// being invisible. Real sockets: httptest-style app.Test bypasses the
// server-level checks that produce these statuses.
func TestRejectedRequestsAreObserved(t *testing.T) {
	logs := newSyncLog()
	reg := metrics.NewRegistry()
	a := New(&finder.Finder{}, reg, log.New(logs, "", 0))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = a.Listener(ln) }()
	t.Cleanup(func() { _ = a.Shutdown() })
	addr := ln.Addr().String()

	// 413: a declared body above Config().BodyLimit. The headers alone are
	// enough for the server to reject, and sending no body avoids racing the
	// server's close against a client still writing.
	assert.Equal(t, 413, rawStatus(t, addr,
		"POST /nearest/batch HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: "+
			strconv.Itoa(Config().BodyLimit+1)+"\r\n\r\n"))

	// 431: a header larger than the read buffer.
	assert.Equal(t, 431, rawStatus(t, addr,
		"GET /healthz HTTP/1.1\r\nHost: x\r\nX-Big: "+strings.Repeat("a", 16<<10)+"\r\n\r\n"))

	out := reg.Render()
	assert.Contains(t, out, `http_requests_total{path="(rejected)",status="413"} 1`)
	assert.Contains(t, out, `http_requests_total{path="(rejected)",status="431"} 1`)

	logged := logs.drain()
	assert.Contains(t, logged, " - - 413 ")
	assert.Contains(t, logged, " - - 431 ")
	assert.NotContains(t, logged, "X-Big", "raw rejected input must never be logged")
}

// rawStatus sends raw over a fresh connection and returns the status code of
// the reply.
func rawStatus(t *testing.T, addr, raw string) int {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte(raw)) // the server may close before reading it all
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestRoutedErrorsAreNotCountedAsRejected: a 404 produced by the router runs
// the middleware chain, so it is observed once, under its own label.
func TestRoutedErrorsAreNotCountedAsRejected(t *testing.T) {
	reg := metrics.NewRegistry()
	a := New(&finder.Finder{}, reg, log.New(io.Discard, "", 0))
	resp, err := a.Test(httptest.NewRequest("GET", "/nope", nil))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
	out := reg.Render()
	assert.Contains(t, out, `http_requests_total{path="(unrouted)",status="404"} 1`)
	assert.NotContains(t, out, rejectedLabel)
}
