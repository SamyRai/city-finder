// cmd/server/routes.go
package routes

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
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

// nearestCityResponse is the /nearest payload: the matched city plus its
// distance from the query point. city.City has no json tags, so its four
// fields marshal capitalized; embedding it keeps that serialization exactly
// as before and only appends distance_km (rounded to 2 decimal places).
// Population is set only for population-ranked requests: the nil pointer is
// omitted from the JSON, so distance-ranked bodies stay byte-identical to the
// pre-ranking API (the embedded City.Population itself stays json:"-"). When
// present it carries the winning city's actual Population int32.
type nearestCityResponse struct {
	city.City
	DistanceKm float64 `json:"distance_km"`
	Population *int32  `json:"Population,omitempty"`
}

func SetupRoutes(app *fiber.App, mainFinder *finder.Finder) {
	// Liveness probe: registered before the data routes and never touches the
	// finders, so it stays cheap and answers even when data loading is slow
	// or the indexes are degraded.
	app.Get("/healthz", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

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

		nearest, distanceKm, err := mainFinder.FindNearestCity(lat, lon, rank)
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
		return c.JSON(response)
	})

	app.Get("/coordinates", func(c *fiber.Ctx) error {
		name := strings.TrimSpace(c.Query("name"))
		if name == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Name is required")
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
