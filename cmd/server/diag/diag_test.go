package diag

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewServerServesPprofOnly(t *testing.T) {
	srv := httptest.NewServer(NewServer("127.0.0.1:0").Handler)
	defer srv.Close()

	for path, want := range map[string]int{
		"/debug/pprof/":                  http.StatusOK,
		"/debug/pprof/heap?debug=1":      http.StatusOK,
		"/debug/pprof/goroutine?debug=1": http.StatusOK,
		"/debug/pprof/cmdline":           http.StatusOK,
		"/healthz":                       http.StatusNotFound,
		"/nearest?lat=1&lon=1":           http.StatusNotFound,
	} {
		resp, err := http.Get(srv.URL + path)
		if !assert.NoError(t, err, path) {
			continue
		}
		_ = resp.Body.Close()
		assert.Equal(t, want, resp.StatusCode, path)
	}
}
