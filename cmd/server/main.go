package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/cmd/server/routes"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/initializer"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
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

	app := fiber.New(fiber.Config{
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
		BodyLimit:    1 << 20, // 1MB; the API is GET-only
		ETag:         true,
		// fasthttp's default admission control (256k) is effectively
		// unbounded: each accepted connection costs a goroutine plus
		// buffers, so a flood ties up memory the 5.7 GB index heap cannot
		// spare. 1024 concurrent connections is far above any legitimate
		// load for this API and caps the per-connection overhead.
		Concurrency: 1024,
	})
	// fasthttp performs no panic recovery of its own: without this middleware
	// any handler panic terminates the process. It must be registered before
	// all other middleware so it wraps the full handler chain.
	app.Use(recover.New())
	app.Use(Logger())
	registry := metrics.NewRegistry()
	routes.SetupRoutesWithMetrics(app, mainFinder, registry)

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
		listenErr <- app.Listen(":" + port)
	}()

	select {
	case err := <-listenErr:
		if err != nil {
			log.Fatalf("Server error: %v", err)
		}
	case <-sigCtx.Done():
		log.Println("shutting down")
		for {
			// A shutdown that races ahead of the serve loop (signal arriving
			// in the moment between starting Listen and the listener being
			// registered) is a no-op and must be retried, otherwise the
			// process would keep serving forever with the signal swallowed.
			if err := app.ShutdownWithTimeout(shutdownTimeout); err != nil {
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

// Logger writes a single line per request: timestamp, method, path (query
// string excluded), status, latency, and response size in bytes. Request
// bodies, query parameters, and multipart forms are never logged.
func Logger() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		log.Printf("%s %s %s %d %s %d",
			start.Format(time.RFC3339),
			c.Method(),
			c.Path(),
			c.Response().StatusCode(),
			time.Since(start),
			len(c.Response().Body()),
		)
		return err
	}
}
