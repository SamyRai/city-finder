package routes

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/gofiber/fiber/v2"
)

// coordinatesHandler serves GET /coordinates: exact (then fuzzy) city lookup
// by name within a country.
func coordinatesHandler(mainFinder *finder.Finder) fiber.Handler {
	return func(c *fiber.Ctx) error {
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
	}
}

// autocompleteHandler serves GET /autocomplete: indexed names by prefix
// within a country.
func autocompleteHandler(mainFinder *finder.Finder) fiber.Handler {
	// autocompleteMatch is one /autocomplete entry: the indexed name plus
	// the first city it resolves to (homonyms collapse to their first city
	// in load order, mirroring the exact-lookup phase).
	type autocompleteMatch struct {
		Name string     `json:"name"`
		City *city.City `json:"city"`
	}

	return func(c *fiber.Ctx) error {
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
	}
}

// postalCodeHandler serves GET /postalCode: postal code lookup within a
// country.
func postalCodeHandler(mainFinder *finder.Finder) fiber.Handler {
	return func(c *fiber.Ctx) error {
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
	}
}
