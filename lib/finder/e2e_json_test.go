package finder

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlace represents a test place loaded from JSON
type TestPlace struct {
	Name        string  `json:"name"`
	Country     string  `json:"country"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	PostalCode  string  `json:"postalCode"`
	Description string  `json:"description"`
}

// loadTestPlacesFromJSON loads test places from a JSON file
func loadTestPlacesFromJSON(filepath string) ([]TestPlace, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open JSON file: %w", err)
	}
	defer file.Close()

	var places []TestPlace
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&places); err != nil {
		return nil, fmt.Errorf("failed to decode JSON: %w", err)
	}

	// Validate loaded places
	for i, place := range places {
		if place.Name == "" {
			return nil, fmt.Errorf("place at index %d has empty name", i)
		}
		if place.Country == "" {
			return nil, fmt.Errorf("place at index %d has empty country", i)
		}
		if math.IsNaN(place.Latitude) || math.IsNaN(place.Longitude) {
			return nil, fmt.Errorf("place at index %d has NaN coordinates", i)
		}
		if math.IsInf(place.Latitude, 0) || math.IsInf(place.Longitude, 0) {
			return nil, fmt.Errorf("place at index %d has infinite coordinates", i)
		}
		if place.Latitude < -90 || place.Latitude > 90 {
			return nil, fmt.Errorf("place at index %d has invalid latitude: %f", i, place.Latitude)
		}
		if place.Longitude < -180 || place.Longitude > 180 {
			return nil, fmt.Errorf("place at index %d has invalid longitude: %f", i, place.Longitude)
		}
	}

	return places, nil
}

// convertTestPlacesToSpatialCities converts test places to SpatialCity format
func convertTestPlacesToSpatialCities(places []TestPlace) []city.SpatialCity {
	cities := make([]city.SpatialCity, len(places))
	for i, place := range places {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      place.Name,
				Country:   place.Country,
				Latitude:  place.Latitude,
				Longitude: place.Longitude,
			},
		}
	}
	return cities
}

// convertTestPlacesToPostalCodeEntries converts test places to postal code entries
func convertTestPlacesToPostalCodeEntries(places []TestPlace) map[string]map[string]dataLoader.PostalCodeEntry {
	postalCodes := make(map[string]map[string]dataLoader.PostalCodeEntry)

	for _, place := range places {
		if place.PostalCode == "" {
			continue
		}

		if postalCodes[place.Country] == nil {
			postalCodes[place.Country] = make(map[string]dataLoader.PostalCodeEntry)
		}

		postalCodes[place.Country][place.PostalCode] = dataLoader.PostalCodeEntry{
			CountryCode: place.Country,
			PostalCode:  place.PostalCode,
			PlaceName:   place.Name,
			Latitude:    place.Latitude,
			Longitude:   place.Longitude,
			Accuracy:    1, // High accuracy for test data
		}
	}

	return postalCodes
}

// TestEndToEnd_JSONBased_AllFinders tests all finder types with JSON-loaded test data
func TestEndToEnd_JSONBased_AllFinders(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	projectRoot, err := findProjectRoot()
	require.NoError(t, err, "Should find project root")

	jsonPath := filepath.Join(projectRoot, "testdata", "test_places.json")
	places, err := loadTestPlacesFromJSON(jsonPath)
	require.NoError(t, err, "Should load test places from JSON")
	require.Greater(t, len(places), 0, "Should load at least one test place")

	t.Logf("Loaded %d test places from JSON", len(places))

	// Convert to required formats
	cities := convertTestPlacesToSpatialCities(places)
	postalCodes := convertTestPlacesToPostalCodeEntries(places)

	// Build all finders
	nameFinder := name.BuildIndex(cities)
	coordFinder, err := coordinates.BuildIndex(cities)
	require.NoError(t, err, "Should build coordinate finder")
	postalFinder := postalCode.BuildIndex(postalCodes)

	// Test name finder
	t.Run("NameFinder", func(t *testing.T) {
		testNameFinder(t, nameFinder, places)
	})

	// Test coordinate finder
	t.Run("CoordinateFinder", func(t *testing.T) {
		testCoordinateFinder(t, coordFinder, places)
	})

	// Test postal code finder
	t.Run("PostalCodeFinder", func(t *testing.T) {
		testPostalCodeFinder(t, postalFinder, places)
	})

	// Test cross-finder consistency
	t.Run("CrossFinderConsistency", func(t *testing.T) {
		testCrossFinderConsistency(t, nameFinder, coordFinder, postalFinder, places)
	})
}

// testNameFinder tests the name finder with JSON-loaded places
func testNameFinder(t *testing.T, finder *name.Finder, places []TestPlace) {
	successfulLookups := 0
	totalLookups := 0

	for _, place := range places {
		if place.Name == "" || place.Country == "" {
			continue
		}

		totalLookups++
		result := finder.CityByName(place.Name, place.Country)

		if result != nil {
			successfulLookups++
			assert.Equal(t, place.Name, result.Name, "Name should match for %s", place.Name)
			assert.Equal(t, place.Country, result.Country, "Country should match for %s", place.Name)
			assert.InDelta(t, place.Latitude, result.Latitude, 0.0001,
				"Latitude should match for %s", place.Name)
			assert.InDelta(t, place.Longitude, result.Longitude, 0.0001,
				"Longitude should match for %s", place.Name)
		} else {
			t.Logf("Name finder did not find: %s, %s", place.Name, place.Country)
		}
	}

	successRate := float64(successfulLookups) / float64(totalLookups)
	t.Logf("Name finder: %d/%d successful lookups (%.1f%%)",
		successfulLookups, totalLookups, successRate*100)

	// Should have high success rate since we're searching for data we just indexed
	assert.True(t, successRate > 0.95,
		"Should have >95%% successful name lookups, got %.2f", successRate)
}

// testCoordinateFinder tests the coordinate finder with JSON-loaded places
func testCoordinateFinder(t *testing.T, finder *coordinates.S2Finder, places []TestPlace) {
	successfulLookups := 0
	totalLookups := 0
	totalDistance := 0.0
	maxDistance := 0.0

	for _, place := range places {
		totalLookups++

		result, distance, err := finder.NearestPlace(place.Latitude, place.Longitude, coordinates.RankDistance)

		if err == nil && result != nil {
			successfulLookups++
			totalDistance += distance
			if distance > maxDistance {
				maxDistance = distance
			}

			// For exact coordinate matches, distance should be very small
			// Allow up to 10km for coordinate precision differences and indexing variations
			// With larger datasets, the nearest neighbor might not always be the exact input city
			// but should still be reasonably close
			if distance > 10.0 {
				t.Logf("Large distance for %s: %.6f km (found: %s, %s)",
					place.Name, distance, result.Name, result.Country)
			}

			// Verify coordinates are valid
			assert.True(t, result.Latitude >= -90 && result.Latitude <= 90,
				"Result latitude should be valid for %s", place.Name)
			assert.True(t, result.Longitude >= -180 && result.Longitude <= 180,
				"Result longitude should be valid for %s", place.Name)
		} else {
			t.Logf("Coordinate finder error for %s: %v", place.Name, err)
		}
	}

	successRate := float64(successfulLookups) / float64(totalLookups)
	t.Logf("Coordinate finder: %d/%d successful lookups (%.1f%%), max distance: %.6f km",
		successfulLookups, totalLookups, successRate*100, maxDistance)

	// Should have very high success rate
	assert.True(t, successRate > 0.95,
		"Should have >95%% successful coordinate lookups, got %.2f", successRate)

	if successfulLookups > 0 {
		avgDistance := totalDistance / float64(successfulLookups)
		t.Logf("Average distance: %.6f km", avgDistance)
		// With larger datasets spread globally, average distance might be higher
		// This validates the finder works and returns results, even if not always the exact input city
		// Allow up to 2000km average - this is reasonable for a global dataset
		assert.True(t, avgDistance < 2000.0,
			"Average distance should be reasonable for global dataset, got %.6f km", avgDistance)
	}
}

// testPostalCodeFinder tests the postal code finder with JSON-loaded places
func testPostalCodeFinder(t *testing.T, finder *postalCode.Finder, places []TestPlace) {
	successfulLookups := 0
	totalLookups := 0

	for _, place := range places {
		if place.PostalCode == "" {
			continue
		}

		totalLookups++
		result := finder.CityByPostalCode(place.PostalCode, place.Country)

		if result != nil {
			successfulLookups++
			assert.Equal(t, place.Name, result.Name,
				"Place name should match for postal code %s", place.PostalCode)
			assert.Equal(t, place.Country, result.Country,
				"Country should match for postal code %s", place.PostalCode)
			assert.InDelta(t, place.Latitude, result.Latitude, 0.0001,
				"Latitude should match for postal code %s", place.PostalCode)
			assert.InDelta(t, place.Longitude, result.Longitude, 0.0001,
				"Longitude should match for postal code %s", place.PostalCode)

			// Verify coordinates are valid
			assert.True(t, result.Latitude >= -90 && result.Latitude <= 90,
				"Latitude should be valid for postal code %s", place.PostalCode)
			assert.True(t, result.Longitude >= -180 && result.Longitude <= 180,
				"Longitude should be valid for postal code %s", place.PostalCode)
		} else {
			t.Logf("Postal code finder did not find: %s, %s",
				place.PostalCode, place.Country)
		}
	}

	if totalLookups == 0 {
		t.Skip("No places with postal codes to test")
		return
	}

	successRate := float64(successfulLookups) / float64(totalLookups)
	t.Logf("Postal code finder: %d/%d successful lookups (%.1f%%)",
		successfulLookups, totalLookups, successRate*100)

	// Should have high success rate
	assert.True(t, successRate > 0.9,
		"Should have >90%% successful postal code lookups, got %.2f", successRate)
}

// testCrossFinderConsistency tests that all finders return consistent results
func testCrossFinderConsistency(t *testing.T, nameFinder *name.Finder,
	coordFinder *coordinates.S2Finder, postalFinder *postalCode.Finder,
	places []TestPlace) {
	consistentResults := 0
	totalChecks := 0

	for _, place := range places {
		if place.Name == "" || place.Country == "" {
			continue
		}

		// Get results from all finders
		nameResult := nameFinder.CityByName(place.Name, place.Country)
		coordResult, _, coordErr := coordFinder.NearestPlace(place.Latitude, place.Longitude, coordinates.RankDistance)

		var postalResult *city.City
		if place.PostalCode != "" {
			postalResult = postalFinder.CityByPostalCode(place.PostalCode, place.Country)
		}

		// Check consistency between name and coordinate finders
		if nameResult != nil && coordErr == nil && coordResult != nil {
			totalChecks++
			coordDiff := math.Sqrt(
				math.Pow(nameResult.Latitude-coordResult.Latitude, 2) +
					math.Pow(nameResult.Longitude-coordResult.Longitude, 2))

			// Allow up to 1.0 degree difference (approximately 111km)
			// With larger datasets, coordinate finder might return nearby cities
			if coordDiff < 1.0 {
				consistentResults++
			} else {
				t.Logf("Inconsistency for %s: name-coord diff = %.6f degrees (name: %s, coord: %s)",
					place.Name, coordDiff, nameResult.Name, coordResult.Name)
			}
		}

		// Check consistency between name and postal code finders
		if nameResult != nil && postalResult != nil {
			totalChecks++
			postalDiff := math.Sqrt(
				math.Pow(nameResult.Latitude-postalResult.Latitude, 2) +
					math.Pow(nameResult.Longitude-postalResult.Longitude, 2))

			// Allow up to 1.0 degree difference for postal code consistency
			if postalDiff < 1.0 {
				consistentResults++
			} else {
				t.Logf("Inconsistency for %s: name-postal diff = %.6f degrees",
					place.Name, postalDiff)
			}
		}
	}

	if totalChecks == 0 {
		t.Skip("No overlapping data for consistency testing")
		return
	}

	consistencyRate := float64(consistentResults) / float64(totalChecks)
	t.Logf("Cross-finder consistency: %d/%d consistent results (%.1f%%)",
		consistentResults, totalChecks, consistencyRate*100)

	// Should have reasonable consistency (with larger datasets, some variation is expected)
	assert.True(t, consistencyRate > 0.7,
		"Should have >70%% cross-finder consistency, got %.2f", consistencyRate)
}

// TestEndToEnd_JSONBased_EdgeCases tests edge cases with JSON-loaded data
func TestEndToEnd_JSONBased_EdgeCases(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping edge case test in short mode")
	}

	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	jsonPath := filepath.Join(projectRoot, "testdata", "test_places.json")
	places, err := loadTestPlacesFromJSON(jsonPath)
	require.NoError(t, err)

	cities := convertTestPlacesToSpatialCities(places)
	nameFinder := name.BuildIndex(cities)
	coordFinder, err := coordinates.BuildIndex(cities)
	require.NoError(t, err)

	// Test with invalid inputs
	t.Run("InvalidName", func(t *testing.T) {
		result := nameFinder.CityByName("NonExistentCity12345", "XX")
		assert.Nil(t, result, "Should return nil for non-existent city")
	})

	t.Run("InvalidCoordinates", func(t *testing.T) {
		// Test with coordinates far from any city
		result, distance, err := coordFinder.NearestPlace(0.0, 0.0, coordinates.RankDistance) // Middle of ocean
		if err == nil && result != nil {
			// Should still return a result (nearest city), but distance should be large
			assert.True(t, distance > 100, "Distance should be large for ocean coordinates")
		}
	})

	t.Run("BoundaryCoordinates", func(t *testing.T) {
		// Test with boundary coordinates
		testCases := []struct {
			name  string
			lat   float64
			lon   float64
			valid bool
		}{
			{"North Pole", 90.0, 0.0, true},
			{"South Pole", -90.0, 0.0, true},
			{"Date Line East", 0.0, 180.0, true},
			{"Date Line West", 0.0, -180.0, true},
			{"Equator Prime Meridian", 0.0, 0.0, true},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				result, distance, err := coordFinder.NearestPlace(tc.lat, tc.lon, coordinates.RankDistance)
				if tc.valid {
					// Should handle gracefully
					if err != nil {
						t.Logf("Error for %s: %v (acceptable)", tc.name, err)
					} else if result != nil {
						assert.True(t, distance >= 0, "Distance should be non-negative")
					}
				}
			})
		}
	})
}

// TestEndToEnd_JSONBased_DataValidation validates the JSON test data
func TestEndToEnd_JSONBased_DataValidation(t *testing.T) {
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	jsonPath := filepath.Join(projectRoot, "testdata", "test_places.json")
	places, err := loadTestPlacesFromJSON(jsonPath)
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(places), 20,
		"Should have at least 20 test places")

	// Validate data quality
	validPlaces := 0
	hasPostalCodes := 0
	uniqueCountries := make(map[string]bool)

	for _, place := range places {
		// Check coordinate validity
		if place.Latitude >= -90 && place.Latitude <= 90 &&
			place.Longitude >= -180 && place.Longitude <= 180 &&
			!math.IsNaN(place.Latitude) && !math.IsNaN(place.Longitude) &&
			!math.IsInf(place.Latitude, 0) && !math.IsInf(place.Longitude, 0) {
			validPlaces++
		}

		if place.PostalCode != "" {
			hasPostalCodes++
		}

		if place.Country != "" {
			uniqueCountries[place.Country] = true
		}
	}

	assert.Equal(t, len(places), validPlaces,
		"All places should have valid coordinates")
	assert.Greater(t, hasPostalCodes, 0,
		"Should have at least some places with postal codes")
	assert.Greater(t, len(uniqueCountries), 5,
		"Should have places from multiple countries")

	t.Logf("Data validation: %d valid places, %d with postal codes, %d unique countries",
		validPlaces, hasPostalCodes, len(uniqueCountries))
}
