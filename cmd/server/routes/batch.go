package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/gofiber/fiber/v2"
)

// maxBatchPoints caps a POST /nearest/batch request. The batch exists so a
// client pays one round trip instead of N; past ~100 points a batch of
// population-gated lookups (bounded by the gate) would hold a connection far
// longer than the equivalent parallel GETs, so larger batches are a 400, not
// a slowdown.
const maxBatchPoints = 100

// batchWorkerCount returns the fan-out for a batch of n points. A batch that
// contains population-ranked points never runs more workers than the
// population gate has slots: the gate fails fast, so a fan-out wider than the
// gate (GOMAXPROCS > 8) would let a single batch on an idle server saturate
// it and 503 itself. Distance-only batches keep the full fan-out.
func (h *Handlers) batchWorkerCount(n int, hasPopulation bool) int {
	workers := min(n, h.maxBatchWorkers)
	if hasPopulation {
		workers = min(workers, cap(h.gate))
	}
	return max(workers, 1)
}

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

// nearestExecutor is the per-point query core of the batch handler:
// Handlers.executeNearest in production, a stub in tests that need a failing
// core.
type nearestExecutor func(lat, lon float64, rank coordinates.Rank, includeAdmin bool) (nearestCityResponse, nearestOutcome)

// runPoint executes one batch point and converts a panic in the executor
// into nearestError, the outcome GET serves as 500. Worker goroutines are
// outside fiber's recover middleware, so an unrecovered panic here would
// terminate the whole process. onPanic runs with the panic value and stack
// so the caller can log once per request.
func runPoint(exec nearestExecutor, q validatedPoint, onPanic func(v any, stack []byte)) (resp nearestCityResponse, outcome nearestOutcome) {
	defer func() {
		if v := recover(); v != nil {
			onPanic(v, debug.Stack())
			resp, outcome = nearestCityResponse{}, nearestError
		}
	}()
	return exec(q.lat, q.lon, q.rank, q.includeAdmin)
}

// batch serves POST /nearest/batch: one round trip for many /nearest
// lookups. Each point carries the GET endpoint's parameters (lat/lon
// required, rank/include optional with the same whitelist); the reply is a
// parallel results array — the object GET would return for that point, or
// null where GET would 404. Points execute concurrently through the same
// query core (population gate included), so a batch observes the same
// per-point semantics as the GETs it replaces.
func (h *Handlers) batch(c *fiber.Ctx) error {
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
	workers := h.batchWorkerCount(len(queries), hasPopulation)
	// One log line (with stack) per request, however many points panic.
	var logPanic sync.Once
	onPanic := func(v any, stack []byte) {
		logPanic.Do(func() { log.Printf("panic in /nearest/batch worker: %v\n%s", v, stack) })
	}
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range work {
				if int64(i) > firstFailure.Load() {
					continue // a result after the first failure is never used
				}
				responses[i], outcomes[i] = runPoint(h.execute, queries[i], onPanic)
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
}
