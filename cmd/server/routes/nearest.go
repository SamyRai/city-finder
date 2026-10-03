package routes

import (
	"fmt"
	"log"
	"math"
	"runtime"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/gofiber/fiber/v2"
)

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

// nearestHandler serves GET /nearest.
func nearestHandler(mainFinder *finder.Finder) fiber.Handler {
	return func(c *fiber.Ctx) error {
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
	}
}
