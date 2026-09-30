package main

import (
	"log"
	"os"
	"time"

	"github.com/SamyRai/cityFinder/cmd/server/routes"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/initializer"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	configPath, exists := os.LookupEnv("CONFIG_PATH")
	if !exists {
		configPath = "config.json"
	}

	cfg, err := config.LoadConfig(configPath)
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
	})
	// fasthttp performs no panic recovery of its own: without this middleware
	// any handler panic terminates the process. It must be registered before
	// all other middleware so it wraps the full handler chain.
	app.Use(recover.New())
	app.Use(Logger())
	routes.SetupRoutes(app, mainFinder)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Fatal(app.Listen(":" + port))
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
