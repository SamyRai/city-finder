// cmd/server/routes.go
package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	rmetrics "runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// healthzBody is the fixed liveness response, served without a per-probe
// marshal.
var healthzBody = []byte(`{"status":"ok"}`)

// SetupRoutes registers the data routes without a metrics registry (tests
// and embedded use); the metrics middleware and endpoint are skipped.
func SetupRoutes(app *fiber.App, mainFinder *finder.Finder) {
	SetupRoutesWithMetrics(app, mainFinder, nil)
}

// SetupRoutesWithMetrics registers the data routes plus, when reg is
// non-nil, a request-metrics middleware and the GET /metrics endpoint.
func SetupRoutesWithMetrics(app *fiber.App, mainFinder *finder.Finder, reg *metrics.Registry) {
	if reg != nil {
		app.Use(func(c *fiber.Ctx) error {
			start := time.Now()
			err := c.Next()
			if pattern := RouteLabel(c, err); pattern != "/metrics" { // a scrape must not grow its own counts
				reg.ObserveRequest(pattern, CompletedStatus(c, err), time.Since(start))
			}
			return err
		})
	}
	// Handler panics are recovered HERE, inside the instrumentation, so the
	// metrics middleware above (and any access log further out) observes them
	// as the 500 the client receives. A recover registered outermost only
	// would let the panic unwind through every middleware unrecorded.
	app.Use(recover.New())

	// Liveness probe: registered before the data routes and never touches the
	// finders, so it stays cheap and answers even when data loading is slow
	// or the indexes are degraded.
	app.Get("/healthz", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		return c.Send(healthzBody)
	})

	if reg != nil {
		// fuzzy_budget_trips_total is scraped from the library counter as a
		// delta since the previous scrape; Swap makes concurrent scrapes
		// count each trip exactly once.
		var lastTrips atomic.Uint64
		lastTrips.Store(name.FuzzyBudgetTrips())
		app.Get("/metrics", func(c *fiber.Ctx) error {
			cur := name.FuzzyBudgetTrips()
			if delta := cur - lastTrips.Swap(cur); delta > 0 {
				reg.AddCounter("fuzzy_budget_trips_total", int64(delta))
			}
			// 0 = not built, 1 = building, 2 = built, 3 = disabled —
			// name.Finder.FuzzyBuildState documents the mapping.
			reg.SetGauge("fuzzy_build_state", float64(mainFinder.FuzzyBuildState()))
			// Runtime gauges, computed per scrape. The heap figure comes
			// from runtime/metrics rather than runtime.ReadMemStats so a
			// scrape never pays that call's stop-the-world. The sample slice
			// is scrape-local on purpose: Read writes into it, so a shared
			// package-level slice would race across concurrent scrapes.
			reg.SetGauge("go_goroutines", float64(runtime.NumGoroutine()))
			samples := []rmetrics.Sample{{Name: heapAllocMetricName}}
			rmetrics.Read(samples)
			if v := samples[0].Value; v.Kind() == rmetrics.KindUint64 {
				reg.SetGauge("go_heap_alloc_bytes", float64(v.Uint64()))
			} else {
				reg.SetGauge("go_heap_alloc_bytes", v.Float64())
			}
			return c.Type("text", "plain").SendString(reg.Render())
		})
	}

	app.Get("/nearest", func(c *fiber.Ctx) error {
		lat, ok := parseCoordinate(c.Query("lat"))
		if !ok {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid latitude")
		}
		lon, ok := parseCoordinate(c.Query("lon"))
		if !ok {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid longitude")
		}

		if lat < -90 || lat > 90 {
			return c.Status(fiber.StatusBadRequest).SendString("Latitude must be between -90 and 90")
		}

		if lon < -180 || lon > 180 {
			return c.Status(fiber.StatusBadRequest).SendString("Longitude must be between -180 and 180")
		}

		rank, ok := parseRank(c.Query("rank"))
		if !ok {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid rank")
		}

		includeAdmin, ok := parseInclude(c.Query("include"))
		if !ok {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid include")
		}

		// The lookup itself (gate, finder dispatch, response construction) is
		// the shared core behind both /nearest transports.
		response, outcome := executeNearest(mainFinder, lat, lon, rank, includeAdmin)
		switch outcome {
		case nearestSaturated:
			c.Set(fiber.HeaderRetryAfter, "1")
			return c.Status(fiber.StatusServiceUnavailable).
				SendString("population ranking is saturated, retry shortly")
		case nearestError:
			return c.Status(fiber.StatusInternalServerError).SendString("internal server error")
		case nearestNotFound:
			return c.Status(fiber.StatusNotFound).SendString(fmt.Sprintf("City not found for lat: %f, lon: %f", lat, lon))
		}
		return c.JSON(response)
	})

	// POST /nearest/batch: one round trip for many /nearest lookups. Each
	// point carries the GET endpoint's parameters (lat/lon required,
	// rank/include optional with the same whitelist); the reply is a
	// parallel results array — the object GET would return for that point,
	// or null where GET would 404. Points execute concurrently through the
	// same query core (population gate included), so a batch observes the
	// same per-point semantics as the GETs it replaces.
	app.Post("/nearest/batch", func(c *fiber.Ctx) error {
		if c.Get(fiber.HeaderContentType) != "application/json" {
			return c.Status(fiber.StatusBadRequest).
				SendString("Content-Type must be application/json")
		}

		// Strict decode: unknown fields are named in the 400 (this handler
		// ignores nothing, like it is case-sensitive everywhere else); a
		// second Decode must then hit EOF, so trailing garbage is malformed
		// too. Schema type mismatches (e.g. a string for lat) share the
		// "invalid JSON body" fate: the body is not a valid request document.
		dec := json.NewDecoder(bytes.NewReader(c.Body()))
		dec.DisallowUnknownFields()
		var req batchRequest
		if err := dec.Decode(&req); err != nil {
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) ||
				!strings.HasPrefix(err.Error(), "json: unknown field ") {
				return c.Status(fiber.StatusBadRequest).SendString("invalid JSON body")
			}
			return c.Status(fiber.StatusBadRequest).SendString(err.Error())
		}
		var trailing any
		if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
			return c.Status(fiber.StatusBadRequest).SendString("invalid JSON body")
		}

		if len(req.Points) == 0 {
			return c.Status(fiber.StatusBadRequest).
				SendString("points must contain at least one entry")
		}
		if len(req.Points) > maxBatchPoints {
			return c.Status(fiber.StatusBadRequest).
				SendString(fmt.Sprintf("points must contain at most %d entries", maxBatchPoints))
		}

		// Validate every point before executing any, in request order; the
		// first failure wins and names the offending index.
		queries := make([]validatedPoint, 0, len(req.Points))
		for i, point := range req.Points {
			query, reason := validateBatchPoint(point)
			if reason != "" {
				return c.Status(fiber.StatusBadRequest).
					SendString(fmt.Sprintf("points[%d]: %s", i, reason))
			}
			queries = append(queries, query)
		}

		// Execute the points concurrently: lookups are independent, and a
		// sequential batch of far-from-land rank=population points would run
		// for minutes (each scan takes seconds). Fan-out is bounded by
		// maxBatchWorkers; the expensive class stays additionally bounded by
		// the populationGate inside executeNearest, exactly like parallel
		// GETs would be. Results land at each point's index, so the response
		// array stays parallel to the request regardless of completion
		// order; the first saturated/error outcome in REQUEST order still
		// fails the whole request.
		//
		// Once a point fails, every LATER point is skipped: its result would
		// be discarded, and a population point would hold a gate slot for
		// nothing. Earlier points still run, so the first failure in request
		// order is still the one reported (firstFailure only ever decreases,
		// and a skipped index is always above a failed one).
		responses := make([]nearestCityResponse, len(queries))
		outcomes := make([]nearestOutcome, len(queries))
		var firstFailure atomic.Int64
		firstFailure.Store(int64(len(queries)))
		work := make(chan int)
		hasPopulation := false
		for _, q := range queries {
			hasPopulation = hasPopulation || q.rank == coordinates.RankPopulation
		}
		workers := batchWorkerCount(len(queries), hasPopulation)
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				for i := range work {
					if int64(i) > firstFailure.Load() {
						continue // a result after the first failure is never used
					}
					responses[i], outcomes[i] = executeNearest(
						mainFinder, queries[i].lat, queries[i].lon,
						queries[i].rank, queries[i].includeAdmin)
					if outcomes[i] == nearestSaturated || outcomes[i] == nearestError {
						lowerFirstFailure(&firstFailure, int64(i))
					}
				}
			}()
		}
		for i := range queries {
			work <- i
		}
		close(work)
		wg.Wait()

		results := make([]*nearestCityResponse, 0, len(queries))
		for i, outcome := range outcomes {
			switch outcome {
			case nearestSaturated:
				// A saturated gate mid-batch fails the whole request. Slots
				// are released inside executeNearest before it returns, so
				// this request holds none on the way out.
				c.Set(fiber.HeaderRetryAfter, "1")
				return c.Status(fiber.StatusServiceUnavailable).
					SendString("population ranking is saturated, retry shortly")
			case nearestError:
				return c.Status(fiber.StatusInternalServerError).SendString("internal server error")
			case nearestNotFound:
				results = append(results, nil) // the batch form of GET's 404
			default: // nearestOK
				results = append(results, &responses[i])
			}
		}
		return c.JSON(batchResponse{Results: results})
	})

	app.Get("/coordinates", func(c *fiber.Ctx) error {
		name := strings.TrimSpace(c.Query("name"))
		if name == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Name is required")
		}
		if utf8.RuneCountInString(name) > maxNameRunes {
			return c.Status(fiber.StatusBadRequest).
				SendString(fmt.Sprintf("Name too long (max %d characters)", maxNameRunes))
		}
		countryCode := strings.ToUpper(strings.TrimSpace(c.Query("country-code")))
		if countryCode == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Country code is required")
		}

		city := mainFinder.FindCityByName(name, countryCode)
		if city == nil {
			return c.Status(fiber.StatusNotFound).SendString("City not found")
		}

		return c.JSON(city)
	})

	// autocompleteMatch is one /autocomplete entry: the indexed name plus
	// the first city it resolves to (homonyms collapse to their first city
	// in load order, mirroring the exact-lookup phase).
	type autocompleteMatch struct {
		Name string     `json:"name"`
		City *city.City `json:"city"`
	}

	app.Get("/autocomplete", func(c *fiber.Ctx) error {
		namePrefix := strings.TrimSpace(c.Query("name"))
		if namePrefix == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Name is required")
		}
		if utf8.RuneCountInString(namePrefix) > maxNameRunes {
			return c.Status(fiber.StatusBadRequest).
				SendString(fmt.Sprintf("Name too long (max %d characters)", maxNameRunes))
		}
		countryCode := strings.ToUpper(strings.TrimSpace(c.Query("country-code")))
		if countryCode == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Country code is required")
		}

		limit := 10
		if raw := c.Query("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 50 {
				return c.Status(fiber.StatusBadRequest).SendString("Invalid limit (must be 1-50)")
			}
			limit = parsed
		}

		matches := mainFinder.PrefixNames(countryCode, namePrefix, limit)
		response := struct {
			Matches []autocompleteMatch `json:"matches"`
		}{Matches: make([]autocompleteMatch, 0, len(matches))}
		for _, m := range matches {
			response.Matches = append(response.Matches, autocompleteMatch{Name: m.Name, City: m.City})
		}
		return c.JSON(response)
	})

	app.Get("/postalCode", func(c *fiber.Ctx) error {
		// Inner spaces are significant (e.g. GB "SW1A 1AA"); only surrounding
		// whitespace is trimmed so exact GeoNames lookups keep working.
		postalCode := strings.TrimSpace(c.Query("code"))
		countryCode := strings.ToUpper(strings.TrimSpace(c.Query("country-code")))
		if postalCode == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Postal code is required")
		}
		if countryCode == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Country code is required")
		}
		city := mainFinder.FindCityByPostalCode(postalCode, countryCode)
		if city == nil {
			return c.Status(fiber.StatusNotFound).SendString("City not found")
		}

		return c.JSON(city)
	})
}
