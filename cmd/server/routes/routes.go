// cmd/server/routes.go
package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
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

// parseCoordinate parses and validates a lat/lon query parameter. It rejects
// values that are not finite numbers: NaN passes strconv.ParseFloat but fails
// every range comparison, and ±Inf must not leak into the spatial index.
func parseCoordinate(raw string) (float64, bool) {
	// No log on a parse failure: the 400 is already visible in metrics and
	// the access log, and echoing client-supplied input into the log on
	// every bad request was an unauthenticated log-amplification path.
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// parseRank validates the optional rank query parameter of /nearest. Absent
// or empty selects the default distance ranking (backward compatible); the
// only accepted values are the exact lowercase "distance" and "population"
// (case-sensitive, like every other parameter on this handler).
func parseRank(raw string) (coordinates.Rank, bool) {
	switch raw {
	case "", "distance":
		return coordinates.RankDistance, true
	case "population":
		return coordinates.RankPopulation, true
	default:
		return 0, false
	}
}

// parseInclude validates the optional include query parameter of /nearest.
// Absent or empty requests the plain v1.0 response; the only accepted value
// is the exact lowercase "admin" (case-sensitive, like every other parameter
// on this handler). Any other value is invalid, not ignored.
func parseInclude(raw string) (includeAdmin bool, ok bool) {
	switch raw {
	case "", "admin":
		return raw == "admin", true
	default:
		return false, false
	}
}

// nearestCityResponse is the /nearest payload: the matched city plus its
// distance from the query point. city.City has no json tags, so its four
// fields marshal capitalized; embedding it keeps that serialization exactly
// as before and only appends distance_km (rounded to 2 decimal places).
// Population is set only for population-ranked requests: the nil pointer is
// omitted from the JSON, so distance-ranked bodies stay byte-identical to the
// pre-ranking API (the embedded City.Population itself stays json:"-"). When
// present it carries the winning city's actual Population int32.
//
// The admin1/admin2 fields are set only for include=admin requests and are
// likewise omitted entirely (omitempty on the zero values) from every other
// response, keeping default bodies byte-identical to v1.0. Boundary caveat
// (documented in the API spec): attribution follows the NEAREST city, not
// polygon containment — near a border it may report the region the closest
// city sits in, across the line.
type nearestCityResponse struct {
	city.City
	DistanceKm float64 `json:"distance_km"`
	Population *int32  `json:"Population,omitempty"`

	Admin1Code string `json:"admin1_code,omitempty"` // raw per-country code ("06"), when include=admin
	Admin1Name string `json:"admin1_name,omitempty"` // from the optional names dataset; omitted in codes-only mode
	Admin2Code string `json:"admin2_code,omitempty"` // raw per-country code ("075"), omitted when the city has none
}

// maxNameRunes caps the /coordinates name parameter. The longest real place
// names are well under 100 runes; the cap also bounds the fuzzy path, whose
// query-gram dedup is quadratic in name length.
const maxNameRunes = 200

// populationGate bounds how many rank=population queries may execute at
// once. Each query escalates search discs under an anytime bound; a far-from
// land (mid-ocean) point escalates to a full-index scan that allocates a
// multi-hundred-MB result slice and can run for seconds — N simultaneous
// such queries is a memory-spike/OOM vector, so saturation sheds load with
// 503 + Retry-After instead of queueing unbounded work. Cheap (land)
// population queries hold a slot only for microseconds-to-milliseconds and
// do not saturate the gate in practice.
var populationGate = make(chan struct{}, populationGateConcurrency())

// populationGateConcurrency sizes the gate to the CPU: the scans are
// CPU-bound, so more slots than cores only adds memory pressure; 8 keeps a
// hard ceiling on worst-case transient allocation.
func populationGateConcurrency() int {
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

// nearestOutcome enumerates the ways a /nearest lookup can end. Each
// transport — the GET handler and the batch handler — maps the outcomes to
// its own wire form while executing exactly one query core.
type nearestOutcome int

const (
	nearestOK        nearestOutcome = iota // city found, response built
	nearestNotFound                        // finder returned no city (GET's 404)
	nearestSaturated                       // populationGate full (503 + Retry-After)
	nearestError                           // internal finder error (500)
)

// executeNearest is the query core shared by GET /nearest and POST
// /nearest/batch: population-gate admission, finder dispatch, and response
// construction. The two finder paths share everything but the attribution
// read; default requests keep calling FindNearestCity so their bodies — and
// latency profile — stay byte-identical to v1.0. A population-gate slot is
// held only for the duration of one lookup and released before returning, so
// a batch calling this per point observes the same discipline as GET.
func executeNearest(f *finder.Finder, lat, lon float64, rank coordinates.Rank, includeAdmin bool) (nearestCityResponse, nearestOutcome) {
	if rank == coordinates.RankPopulation {
		select {
		case populationGate <- struct{}{}:
			defer func() { <-populationGate }()
		default:
			return nearestCityResponse{}, nearestSaturated
		}
	}

	var nearest *city.City
	var distanceKm float64
	var admin coordinates.AdminAttribution
	var err error
	if includeAdmin {
		nearest, distanceKm, admin, err = f.S2Finder.NearestPlaceWithAdmin(lat, lon, rank)
	} else {
		nearest, distanceKm, err = f.FindNearestCity(lat, lon, rank)
	}
	if err != nil {
		log.Printf("Error finding city for lat=%f lon=%f: %v", lat, lon, err)
		return nearestCityResponse{}, nearestError
	}
	if nearest == nil {
		return nearestCityResponse{}, nearestNotFound
	}
	response := nearestCityResponse{
		City:       *nearest,
		DistanceKm: math.Round(distanceKm*100) / 100,
	}
	if rank == coordinates.RankPopulation {
		population := nearest.Population
		response.Population = &population
	}
	if includeAdmin {
		response.Admin1Code = admin.Admin1Code
		response.Admin1Name = admin.Admin1Name // "" (omitted) in codes-only mode
		response.Admin2Code = admin.Admin2Code // "" (omitted) when the city has none
	}
	return response, nearestOK
}

// maxBatchPoints caps a POST /nearest/batch request. The batch exists so a
// client pays one round trip instead of N; past ~100 points a batch of
// population-gated lookups (bounded by the gate) would hold a connection far
// longer than the equivalent parallel GETs, so larger batches are a 400, not
// a slowdown.
const maxBatchPoints = 100

// maxBatchWorkers bounds the POST /nearest/batch fan-out. CPU-bound lookups
// gain nothing beyond core count, and the population gate independently caps
// the expensive class, so one worker per P (capped by the batch size) is the
// honest ceiling.
var maxBatchWorkers = runtime.GOMAXPROCS(0)

// batchWorkerCount returns the fan-out for a batch of n points. A batch that
// contains population-ranked points never runs more workers than the
// population gate has slots: the gate fails fast, so a fan-out wider than the
// gate (GOMAXPROCS > 8) would let a single batch on an idle server saturate
// it and 503 itself. Distance-only batches keep the full fan-out.
func batchWorkerCount(n int, hasPopulation bool) int {
	workers := min(n, maxBatchWorkers)
	if hasPopulation {
		workers = min(workers, cap(populationGate))
	}
	return max(workers, 1)
}

// heapAllocMetricName is the runtime gauge backing go_heap_alloc_bytes:
// /memory/classes/heap/objects:bytes is maintained continuously by the
// runtime, so reading it needs no stop-the-world.
const heapAllocMetricName = "/memory/classes/heap/objects:bytes"

// batchRequest is the POST /nearest/batch body: one point per lookup.
type batchRequest struct {
	Points []batchPoint `json:"points"`
}

// batchPoint is one entry of a POST /nearest/batch request, carrying the GET
// endpoint's parameters. lat/lon are pointers so an absent field is
// distinguishable from a legitimate 0 coordinate; rank/include are pointers
// so absence (the per-point default) differs from any explicit value.
type batchPoint struct {
	Lat     *float64 `json:"lat"`
	Lon     *float64 `json:"lon"`
	Rank    *string  `json:"rank"`
	Include *string  `json:"include"`
}

// batchResponse is the POST /nearest/batch reply: a results array parallel
// to the request's points. A not-found point is a nil entry, which marshals
// as JSON null — the batch form of the GET handler's 404.
type batchResponse struct {
	Results []*nearestCityResponse `json:"results"`
}

// validatedPoint is one batch point after validation, in the shape
// executeNearest consumes.
type validatedPoint struct {
	lat          float64
	lon          float64
	rank         coordinates.Rank
	includeAdmin bool
}

// orEmpty treats an absent JSON string field exactly like the GET handler
// treats an absent query parameter: the empty value that selects the default.
func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// validateBatchPoint applies the /nearest parameter rules to one batch point
// in the GET handler's validation order — lat, lon, lat range, lon range,
// rank, include — and returns the parsed point plus the first failure's
// plain-text reason ("" when the point is valid), so the caller can report
// "points[i]: <reason>" with the GET error texts.
func validateBatchPoint(p batchPoint) (validatedPoint, string) {
	var v validatedPoint
	if p.Lat == nil || math.IsNaN(*p.Lat) || math.IsInf(*p.Lat, 0) {
		return v, "Invalid latitude"
	}
	if p.Lon == nil || math.IsNaN(*p.Lon) || math.IsInf(*p.Lon, 0) {
		return v, "Invalid longitude"
	}
	v.lat, v.lon = *p.Lat, *p.Lon
	if v.lat < -90 || v.lat > 90 {
		return v, "Latitude must be between -90 and 90"
	}
	if v.lon < -180 || v.lon > 180 {
		return v, "Longitude must be between -180 and 180"
	}
	rank, ok := parseRank(orEmpty(p.Rank))
	if !ok {
		return v, "Invalid rank"
	}
	v.rank = rank
	v.includeAdmin, ok = parseInclude(orEmpty(p.Include))
	if !ok {
		return v, "Invalid include"
	}
	return v, ""
}

// healthzBody is the fixed liveness response, served without a per-probe
// marshal.
var healthzBody = []byte(`{"status":"ok"}`)

// lowerFirstFailure lowers *first to i unless it already holds a lower
// index.
func lowerFirstFailure(first *atomic.Int64, i int64) {
	for {
		cur := first.Load()
		if i >= cur || first.CompareAndSwap(cur, i) {
			return
		}
	}
}

// CompletedStatus returns the HTTP status a request ends with. A handler (or
// fiber's router, for 404/405) may return an error that fiber's error handler
// turns into the response AFTER the middleware chain has unwound, so inside a
// middleware the response still carries the default 200; the error's code is
// the status the client actually receives.
func CompletedStatus(c *fiber.Ctx, err error) int {
	if err == nil {
		return c.Response().StatusCode()
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return fiber.StatusInternalServerError
}

// RouteLabel returns the route pattern a request matched, for metrics and
// logs. When no route matches, fiber's router answers 404/405 and reports the
// catch-all middleware ("/") as the request's route; that case is labelled
// "(unrouted)" so 404/405 floods stay one bounded series instead of
// masquerading as a "/" route (this API registers no "/" route). err is the
// error the handler chain returned.
func RouteLabel(c *fiber.Ctx, err error) string {
	path := c.Route().Path
	var fe *fiber.Error
	if path == "/" && errors.As(err, &fe) &&
		(fe.Code == fiber.StatusNotFound || fe.Code == fiber.StatusMethodNotAllowed) {
		return "(unrouted)"
	}
	return path
}

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
