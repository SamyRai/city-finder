// cmd/server/routes.go
package routes

import (
	"fmt"
	"log"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/gofiber/fiber/v2"
)

// parseCoordinate parses and validates a lat/lon query parameter. It rejects
// values that are not finite numbers: NaN passes strconv.ParseFloat but fails
// every range comparison, and ±Inf must not leak into the spatial index.
func parseCoordinate(raw, name string) (float64, bool) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		log.Printf("Error parsing %s: %v", name, err)
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
			pattern := c.Route().Path
			if pattern == "" {
				pattern = "(unrouted)"
			}
			if pattern != "/metrics" { // a scrape must not grow its own counts
				reg.ObserveRequest(pattern, c.Response().StatusCode(), time.Since(start))
			}
			return err
		})
	}

	// Liveness probe: registered before the data routes and never touches the
	// finders, so it stays cheap and answers even when data loading is slow
	// or the indexes are degraded.
	app.Get("/healthz", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
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
			return c.Type("text", "plain").SendString(reg.Render())
		})
	}

	app.Get("/nearest", func(c *fiber.Ctx) error {
		lat, ok := parseCoordinate(c.Query("lat"), "lat")
		if !ok {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid latitude")
		}
		lon, ok := parseCoordinate(c.Query("lon"), "lon")
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

		if rank == coordinates.RankPopulation {
			select {
			case populationGate <- struct{}{}:
				defer func() { <-populationGate }()
			default:
				c.Set(fiber.HeaderRetryAfter, "1")
				return c.Status(fiber.StatusServiceUnavailable).
					SendString("population ranking is saturated, retry shortly")
			}
		}

		// The two paths share the query core; only the attribution read
		// differs. Default requests keep calling FindNearestCity so their
		// bodies — and latency profile — stay byte-identical to v1.0.
		var nearest *city.City
		var distanceKm float64
		var admin coordinates.AdminAttribution
		var err error
		if includeAdmin {
			nearest, distanceKm, admin, err = mainFinder.S2Finder.NearestPlaceWithAdmin(lat, lon, rank)
		} else {
			nearest, distanceKm, err = mainFinder.FindNearestCity(lat, lon, rank)
		}
		if err != nil {
			log.Printf("Error finding city for lat=%f lon=%f: %v", lat, lon, err)
			return c.Status(fiber.StatusInternalServerError).SendString("internal server error")
		}
		if nearest == nil {
			return c.Status(fiber.StatusNotFound).SendString(fmt.Sprintf("City not found for lat: %f, lon: %f", lat, lon))
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
		return c.JSON(response)
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
