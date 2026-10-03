package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/SamyRai/cityFinder/cmd/server/app"
	"github.com/SamyRai/cityFinder/cmd/server/diag"
	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/initializer"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	mainFinder, err := initializer.Initialize(cfg)
	if err != nil {
		log.Fatalf("Initialization failed: %v", err)
	}
	// Index building and decoding leave gigabytes of garbage at prod scale.
	// Return it to the OS now: otherwise the background scavenger releases
	// it gradually and the container's RSS sits near the boot peak.
	debug.FreeOSMemory()
	// Build the fuzzy (n-gram) index in the background so no user query
	// pays the in-request build: until it lands, typo lookups get exact-only
	// results (~94 s and +~1.2 GiB resident at prod scale — see
	// name.Finder.WarmFuzzy). Non-blocking; serving starts immediately.
	mainFinder.WarmFuzzy()

	registry := metrics.NewRegistry()
	server := app.New(mainFinder, registry, log.Default())

	// Opt-in profiling listener (see package diag): off unless PPROF_ADDR is
	// set, and never on the public port. Started after initialization so
	// profiles cover serving, not index loading.
	var pprofServer *http.Server
	if addr := os.Getenv("PPROF_ADDR"); addr != "" {
		pprofServer = diag.NewServer(addr)
		go func() {
			if err := pprofServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("pprof listener on %s stopped: %v", addr, err)
			}
		}()
		log.Printf("pprof listening on %s (PPROF_ADDR)", addr)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	// Graceful shutdown: SIGINT/SIGTERM stop the listener and give in-flight
	// requests up to shutdownTimeout to finish. The signal handler is armed
	// only after initialization so a signal during index loading keeps its
	// default process-killing behavior. fasthttp maps the closed listener to
	// io.EOF, so Listen returns nil after a graceful shutdown; only genuine
	// listen errors reach log.Fatal.
	const shutdownTimeout = 10 * time.Second
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- server.Listen(":" + port)
	}()

	select {
	case err := <-listenErr:
		if err != nil {
			log.Fatalf("Server error: %v", err)
		}
	case <-sigCtx.Done():
		log.Println("shutting down")
		if pprofServer != nil {
			_ = pprofServer.Close()
		}
		for {
			// A shutdown that races ahead of the serve loop (signal arriving
			// in the moment between starting Listen and the listener being
			// registered) is a no-op and must be retried, otherwise the
			// process would keep serving forever with the signal swallowed.
			if err := server.ShutdownWithTimeout(shutdownTimeout); err != nil {
				log.Printf("Shutdown error: %v", err)
			}
			select {
			case err := <-listenErr:
				if err != nil {
					log.Fatalf("Server error: %v", err)
				}
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}
