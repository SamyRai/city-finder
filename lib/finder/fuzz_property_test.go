package finder

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fuzz test for coordinate finder - tests various coordinate inputs
func FuzzCoordinateFinder(f *testing.F) {
	// Add seed corpus with interesting coordinate values
	seedCoords := []struct{ lat, lon float64 }{
		{0, 0},                  // Origin
		{90, 0},                 // North pole
		{-90, 0},                // South pole
		{0, 180},                // International date line
		{0, -180},               // International date line negative
		{45, 90},                // Mid latitude
		{1e-10, 1e-10},          // Very small coordinates
		{89.999999, 179.999999}, // Very close to boundaries
		{math.Pi, math.E},       // Irrational numbers
	}

	for _, coord := range seedCoords {
		f.Add(coord.lat, coord.lon)
	}

	// Create test cities
	testCities := []city.SpatialCity{
		{City: city.City{Name: "TestCity1", Country: "TC", Latitude: 40.7128, Longitude: -74.0060}},
		{City: city.City{Name: "TestCity2", Country: "TC", Latitude: 51.5074, Longitude: -0.1278}},
		{City: city.City{Name: "TestCity3", Country: "TC", Latitude: -33.8688, Longitude: 151.2093}},
	}

	cfg := &config.S2{}
	finder, err := coordinates.BuildIndex(testCities, cfg)
	require.NoError(f, err)

	f.Fuzz(func(t *testing.T, lat, lon float64) {
		// Test that the function doesn't crash with any coordinate input
		result, distance, err := finder.NearestPlace(lat, lon)

		// Basic invariants that should always hold
		if err == nil {
			// If no error, we should get a result
			assert.NotNil(t, result, "Should return a city when no error")

			// Distance should be non-negative
			assert.True(t, distance >= 0, "Distance should be non-negative, got %f", distance)

			// Result should be one of our test cities
			validCities := []string{"TestCity1", "TestCity2", "TestCity3"}
			found := false
			for _, cityName := range validCities {
				if result.Name == cityName {
					found = true
					break
				}
			}
			assert.True(t, found, "Result should be one of the test cities, got %s", result.Name)
		} else {
			// If error, result should be nil
			assert.Nil(t, result, "Should not return a city when error occurs")
		}

		// Distance should never be NaN or Inf
		assert.False(t, math.IsNaN(distance), "Distance should not be NaN")
		assert.False(t, math.IsInf(distance, 0), "Distance should not be Inf")
	})
}

// Fuzz test for name finder - tests various name and country code inputs
func FuzzNameFinder(f *testing.F) {
	// Add seed corpus with interesting strings
	seedInputs := []struct{ name, country string }{
		{"New York", "US"},
		{"London", "GB"},
		{"Tokyo", "JP"},
		{"", ""},                // Empty strings
		{"", "US"},              // Empty name
		{"New York", ""},        // Empty country
		{"São Paulo", "BR"},     // Unicode characters
		{"München", "DE"},       // More unicode
		{string(rune(0)), "XX"}, // Null character
		{"Very Long City Name That Exceeds Normal Length And Might Cause Issues With Internal Data Structures", "XX"},
		{"City with spaces and special chars !@#$%^&*()", "XX"},
	}

	for _, input := range seedInputs {
		f.Add(input.name, input.country)
	}

	// Create test cities
	testCities := []city.SpatialCity{
		{City: city.City{Name: "New York", Country: "US", Latitude: 40.7128, Longitude: -74.0060}},
		{City: city.City{Name: "London", Country: "GB", Latitude: 51.5074, Longitude: -0.1278}},
		{City: city.City{Name: "São Paulo", Country: "BR", Latitude: -23.5505, Longitude: -46.6333}},
	}

	finder := name.BuildIndex(testCities)

	f.Fuzz(func(t *testing.T, name, country string) {
		// Test that the function doesn't crash with any input
		result := finder.CityByName(name, country)

		// If we get a result, it should be one of our test cities
		if result != nil {
			validCities := []string{"New York", "London", "São Paulo"}
			found := false
			for _, cityName := range validCities {
				if result.Name == cityName {
					found = true
					// Verify country matches
					expectedCountry := map[string]string{
						"New York":  "US",
						"London":    "GB",
						"São Paulo": "BR",
					}[cityName]
					assert.Equal(t, expectedCountry, result.Country, "Country should match for %s", cityName)
					break
				}
			}
			assert.True(t, found, "Result should be one of the test cities, got %s", result.Name)
		}
	})
}

// Fuzz test for postal code finder
func FuzzPostalCodeFinder(f *testing.F) {
	// Add seed corpus
	seedInputs := []struct{ postalCode, country string }{
		{"10001", "US"},
		{"SW1A", "GB"},
		{"12345", "DE"},
		{"", ""},                // Empty strings
		{"", "US"},              // Empty postal code
		{"10001", ""},           // Empty country
		{"K1A 0A6", "CA"},       // Canadian format
		{"123-4567", "JP"},      // Japanese format
		{"ABC123", "XX"},        // Alphanumeric
		{string(rune(0)), "XX"}, // Null character
		{"Very Long Postal Code That Might Cause Issues", "XX"},
	}

	for _, input := range seedInputs {
		f.Add(input.postalCode, input.country)
	}

	// Create test postal codes
	testPostalCodes := map[string]map[string]dataLoader.PostalCodeEntry{
		"US": {
			"10001": {CountryCode: "US", PostalCode: "10001", PlaceName: "New York", Latitude: 40.7128, Longitude: -74.0060, Accuracy: 1},
			"90210": {CountryCode: "US", PostalCode: "90210", PlaceName: "Beverly Hills", Latitude: 34.0901, Longitude: -118.4065, Accuracy: 1},
		},
		"GB": {
			"SW1A": {CountryCode: "GB", PostalCode: "SW1A", PlaceName: "London", Latitude: 51.5074, Longitude: -0.1278, Accuracy: 1},
		},
	}

	finder := postalCode.BuildIndex(testPostalCodes)

	f.Fuzz(func(t *testing.T, postalCode, country string) {
		// Test that the function doesn't crash with any input
		result := finder.CityByPostalCode(postalCode, country)

		// If we get a result, it should be valid
		if result != nil {
			// Should have a name
			assert.NotEmpty(t, result.Name, "Result should have a name")

			// Should have valid country
			assert.NotEmpty(t, result.Country, "Result should have a country")

			// Coordinates should be valid
			assert.True(t, result.Latitude >= -90 && result.Latitude <= 90, "Latitude should be valid, got %f", result.Latitude)
			assert.True(t, result.Longitude >= -180 && result.Longitude <= 180, "Longitude should be valid, got %f", result.Longitude)
			assert.False(t, math.IsNaN(result.Latitude), "Latitude should not be NaN")
			assert.False(t, math.IsNaN(result.Longitude), "Longitude should not be NaN")
			assert.False(t, math.IsInf(result.Latitude, 0), "Latitude should not be Inf")
			assert.False(t, math.IsInf(result.Longitude, 0), "Longitude should not be Inf")
		}
	})
}

// Property-based tests for coordinate detection reliability
func TestProperty_CoordinateDetection_RoundTrip(t *testing.T) {
	// Property: If I search for coordinates of a known city, I should find that city or a very close one
	testCities := []city.SpatialCity{
		{City: city.City{Name: "CityA", Country: "XX", Latitude: 40.0, Longitude: -74.0}},
		{City: city.City{Name: "CityB", Country: "XX", Latitude: 41.0, Longitude: -73.0}},
		{City: city.City{Name: "CityC", Country: "XX", Latitude: 39.0, Longitude: -75.0}},
	}

	cfg := &config.S2{}
	finder, err := coordinates.BuildIndex(testCities, cfg)
	require.NoError(t, err)

	for _, testCity := range testCities {
		result, distance, err := finder.NearestPlace(testCity.Latitude, testCity.Longitude)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, distance < 1.0, "Should find the exact city or very close one, distance: %f", distance)
	}
}

func TestProperty_CoordinateDetection_BoundaryConsistency(t *testing.T) {
	// Property: Coordinate searches near boundaries should be consistent
	boundaryCities := []city.SpatialCity{
		{City: city.City{Name: "North", Country: "XX", Latitude: 89.0, Longitude: 0.0}},
		{City: city.City{Name: "South", Country: "XX", Latitude: -89.0, Longitude: 0.0}},
		{City: city.City{Name: "East", Country: "XX", Latitude: 0.0, Longitude: 179.0}},
		{City: city.City{Name: "West", Country: "XX", Latitude: 0.0, Longitude: -179.0}},
	}

	cfg := &config.S2{}
	finder, err := coordinates.BuildIndex(boundaryCities, cfg)
	require.NoError(t, err)

	// Test slight perturbations near boundaries
	testCases := []struct {
		name     string
		lat, lon float64
		expected string
	}{
		{"Near North", 88.9, 0.0, "North"},
		{"Near South", -88.9, 0.0, "South"},
		{"Near East", 0.0, 178.9, "East"},
		{"Near West", 0.0, -178.9, "West"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, distance, err := finder.NearestPlace(tc.lat, tc.lon)
			assert.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, tc.expected, result.Name)
			assert.True(t, distance < 50.0, "Should be reasonably close to boundary city, distance: %f km", distance)
		})
	}
}

func TestProperty_NameLookup_Consistency(t *testing.T) {
	// Property: Same name/country lookup should always return same result
	testCities := []city.SpatialCity{
		{City: city.City{Name: "TestCity", Country: "XX", Latitude: 40.0, Longitude: -74.0}},
		{City: city.City{Name: "OtherCity", Country: "XX", Latitude: 41.0, Longitude: -73.0}},
	}

	finder := name.BuildIndex(testCities)

	// Multiple lookups should be consistent
	for i := 0; i < 100; i++ {
		result1 := finder.CityByName("TestCity", "XX")
		result2 := finder.CityByName("TestCity", "XX")

		if result1 != nil && result2 != nil {
			assert.Equal(t, result1.Name, result2.Name)
			assert.Equal(t, result1.Country, result2.Country)
			assert.Equal(t, result1.Latitude, result2.Latitude)
			assert.Equal(t, result1.Longitude, result2.Longitude)
		} else {
			assert.Equal(t, result1, result2, "Both should be nil or both should be non-nil")
		}
	}
}

func TestProperty_PostalCodeLookup_Consistency(t *testing.T) {
	// Property: Same postal code/country lookup should always return same result
	testPostalCodes := map[string]map[string]dataLoader.PostalCodeEntry{
		"XX": {
			"12345": {CountryCode: "XX", PostalCode: "12345", PlaceName: "TestCity", Latitude: 40.0, Longitude: -74.0, Accuracy: 1},
		},
	}

	finder := postalCode.BuildIndex(testPostalCodes)

	// Multiple lookups should be consistent
	for i := 0; i < 100; i++ {
		result1 := finder.CityByPostalCode("12345", "XX")
		result2 := finder.CityByPostalCode("12345", "XX")

		if result1 != nil && result2 != nil {
			assert.Equal(t, result1.Name, result2.Name)
			assert.Equal(t, result1.Country, result2.Country)
			assert.Equal(t, result1.Latitude, result2.Latitude)
			assert.Equal(t, result1.Longitude, result2.Longitude)
		} else {
			assert.Equal(t, result1, result2, "Both should be nil or both should be non-nil")
		}
	}
}

func TestProperty_CoordinateClipping(t *testing.T) {
	// Property: Coordinate finder should handle out-of-bounds coordinates gracefully
	testCities := []city.SpatialCity{
		{City: city.City{Name: "TestCity", Country: "XX", Latitude: 40.0, Longitude: -74.0}},
	}

	cfg := &config.S2{}
	finder, err := coordinates.BuildIndex(testCities, cfg)
	require.NoError(t, err)

	// Test various out-of-bounds coordinates
	outOfBoundsCoords := []struct{ lat, lon float64 }{
		{91.0, 0.0},      // Invalid latitude (too high)
		{-91.0, 0.0},     // Invalid latitude (too low)
		{0.0, 181.0},     // Invalid longitude (too high)
		{0.0, -181.0},    // Invalid longitude (too low)
		{100.0, 200.0},   // Both invalid
		{-100.0, -200.0}, // Both invalid
	}

	for _, coord := range outOfBoundsCoords {
		result, distance, err := finder.NearestPlace(coord.lat, coord.lon)
		// Should either succeed or fail gracefully, but not crash
		if err == nil {
			assert.NotNil(t, result)
			assert.True(t, distance >= 0)
		}
		// Distance should never be NaN or Inf
		assert.False(t, math.IsNaN(distance))
		assert.False(t, math.IsInf(distance, 0))
	}
}

func TestProperty_FloatPrecisionEdgeCases(t *testing.T) {
	// Property: Handle floating point precision edge cases
	testCities := []city.SpatialCity{
		{City: city.City{Name: "City1", Country: "XX", Latitude: 40.71280000000001, Longitude: -74.00600000000001}},
		{City: city.City{Name: "City2", Country: "XX", Latitude: 40.7128, Longitude: -74.0060}},
	}

	cfg := &config.S2{}
	finder, err := coordinates.BuildIndex(testCities, cfg)
	require.NoError(t, err)

	// Test coordinates that differ by very small amounts
	testCoords := []struct{ lat, lon float64 }{
		{40.71280000000001, -74.00600000000001},   // Exact match for City1
		{40.7128, -74.0060},                       // Exact match for City2
		{40.71280000000002, -74.00600000000002},   // Very close to City1
		{40.712800000000005, -74.006000000000005}, // Even closer to City1
	}

	for _, coord := range testCoords {
		result, distance, err := finder.NearestPlace(coord.lat, coord.lon)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, distance >= 0)
		assert.True(t, distance < 1.0, "Should find very close city, distance: %f", distance)
	}
}

// Generate random but valid test data for property testing
func generateRandomValidCities(count int) []city.SpatialCity {
	cities := make([]city.SpatialCity, count)
	r := rand.New(rand.NewSource(42)) // Fixed seed for reproducible tests

	for i := 0; i < count; i++ {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      fmt.Sprintf("RandomCity%d", i),
				Country:   fmt.Sprintf("C%d", r.Intn(100)),
				Latitude:  r.Float64()*180.0 - 90.0,  // -90 to 90
				Longitude: r.Float64()*360.0 - 180.0, // -180 to 180
			},
		}
	}
	return cities
}

func TestProperty_RandomDataConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping random data property test in short mode")
	}

	// Generate random but valid test data
	cities := generateRandomValidCities(1000)

	// Build all finders
	nameFinder := name.BuildIndex(cities)
	cfg := &config.S2{}
	coordFinder, err := coordinates.BuildIndex(cities, cfg)
	require.NoError(t, err)

	// Test consistency: cities found by coordinate should be findable by name
	consistent := 0
	total := 0

	for _, testCity := range cities {
		total++

		// Find by coordinates
		coordResult, distance, err := coordFinder.NearestPlace(testCity.Latitude, testCity.Longitude)
		if err != nil || coordResult == nil || distance > 1.0 {
			continue // Skip if coordinate search doesn't work well
		}

		// Should be able to find the same city by name
		nameResult := nameFinder.CityByName(coordResult.Name, coordResult.Country)
		if nameResult != nil {
			consistent++
		}
	}

	consistencyRate := float64(consistent) / float64(total)
	assert.True(t, consistencyRate > 0.8, "Should have >80%% consistency between coordinate and name lookups, got %.2f", consistencyRate)

	t.Logf("Random data consistency: %d/%d (%.1f%%)", consistent, total, consistencyRate*100)
}
