// Package diag serves Go's runtime profiling endpoints (net/http/pprof) on a
// separate, opt-in listener, so CPU/heap/mutex/block profiles and execution
// traces can be captured from a server under its real workload — the input
// profile-guided optimization (PGO) and regression diffs (pprof -base) need.
//
// It is never mounted on the public API port. Bind it to loopback (the
// default suggestion is 127.0.0.1:6060) and reach it through a port-forward;
// pprof exposes goroutine stacks and heap contents, so it must not be
// reachable from untrusted networks.
package diag

import (
	"net/http"
	"net/http/pprof"
	"time"
)

// maxProfileWindow bounds a single CPU profile or trace request: the write
// deadline must outlast the requested ?seconds= window.
const maxProfileWindow = 2 * time.Minute

// NewServer returns an http.Server for addr that serves only the
// /debug/pprof/ endpoints, on its own mux (never http.DefaultServeMux, which
// any imported package can register handlers on). The caller runs
// ListenAndServe and closes it on shutdown.
func NewServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      maxProfileWindow + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
