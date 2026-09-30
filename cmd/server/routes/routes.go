// cmd/server/routes.go
package routes

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"github.com/SamyRai/cityFinder/lib/finder"
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

func SetupRoutes(app *fiber.App, mainFinder *finder.Finder) {
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

		city, _, err := mainFinder.FindNearestCity(lat, lon)
		if err != nil {
			log.Printf("Error finding city for lat=%f lon=%f: %v", lat, lon, err)
			return c.Status(fiber.StatusInternalServerError).SendString("internal server error")
		}
		if city == nil {
			return c.Status(fiber.StatusNotFound).SendString(fmt.Sprintf("City not found for lat: %f, lon: %f", lat, lon))
		}
		return c.JSON(city)
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
