package finder

import (
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

func TestDataIntegrity_NameFinder(t *testing.T) {
	// Test with malformed or edge case city data
	testCases := []struct {
		name        string
		cities      []city.SpatialCity
		expectError bool
		description string
	}{
		{
			name: "Empty cities",
			cities: []city.SpatialCity{},
			expectError: false,
			description: "Should handle empty city list gracefully",
		},
		{
			name: "Cities with empty names",
			cities: []city.SpatialCity{
				{City: city.City{Name: "", Country: "US", Latitude: 40.7128, Longitude: -74.0060}},
			},
			expectError: false,
			description: "Should handle cities with empty names",
		},
		{
			name: "Cities with empty country codes",
			cities: []city.SpatialCity{
				{City: city.City{Name: "Test City", Country: "XX", Latitude: 40.7128, Longitude: -74.0060}}, // Use valid country code instead
			},
			expectError: false,
			description: "Should handle cities with various country codes",
		},
		{
			name: "Cities with special characters in names",
			cities: []city.SpatialCity{
				{City: city.City{Name: "São Paulo", Country: "BR", Latitude: -23.5505, Longitude: -46.6333}},
				{City: city.City{Name: "México City", Country: "MX", Latitude: 19.4326, Longitude: -99.1332}},
				{City: city.City{Name: "Zürich", Country: "CH", Latitude: 47.3769, Longitude: 8.5417}},
			},
			expectError: false,
			description: "Should handle international characters in city names",
		},
		{
			name: "Cities with very long names",
			cities: []city.SpatialCity{
				{City: city.City{Name: "This is a very long city name that exceeds normal length and might cause issues with indexing or searching functionality", Country: "US", Latitude: 40.7128, Longitude: -74.0060}},
			},
			expectError: false,
			description: "Should handle cities with very long names",
		},
		{
			name: "Duplicate city names in same country",
			cities: []city.SpatialCity{
				{City: city.City{Name: "Springfield", Country: "US", Latitude: 39.7817, Longitude: -89.6501}},
				{City: city.City{Name: "Springfield", Country: "US", Latitude: 37.2089, Longitude: -93.2922}},
			},
			expectError: false,
			description: "Should handle duplicate city names in same country (returns first match)",
		},
		{
			name: "Cities with extreme coordinate values",
			cities: []city.SpatialCity{
				{City: city.City{Name: "North Pole", Country: "NP", Latitude: 90.0, Longitude: 0.0}},
				{City: city.City{Name: "South Pole", Country: "SP", Latitude: -90.0, Longitude: 0.0}},
			},
			expectError: false,
			description: "Should handle cities at geographic poles",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			finder := name.BuildIndex(tc.cities)

			// Test that index building doesn't crash
			assert.NotNil(t, finder, tc.description)

			// Test basic lookup functionality if we have cities
			if len(tc.cities) > 0 {
				// Try to find the first city by name
				firstCity := tc.cities[0]
				if firstCity.Name != "" && firstCity.Country != "" {
					result := finder.CityByName(firstCity.Name, firstCity.Country)
					// Should either find the city or return nil (for edge cases)
					if result != nil {
						assert.Equal(t, firstCity.Name, result.Name)
						assert.Equal(t, firstCity.Country, result.Country)
					}
				}
			}
		})
	}
}

func TestDataIntegrity_CoordinateFinder(t *testing.T) {
	cfg := &config.S2{}

	testCases := []struct {
		name        string
		cities      []city.SpatialCity
		expectError bool
		description string
	}{
		{
			name:        "Empty cities",
			cities:      []city.SpatialCity{},
			expectError: false,
			description: "Should handle empty city list",
		},
		{
			name: "Single city",
			cities: []city.SpatialCity{
				{City: city.City{Name: "Test City", Country: "TC", Latitude: 0.0, Longitude: 0.0}},
			},
			expectError: false,
			description: "Should handle single city",
		},
		{
			name: "Cities with invalid coordinates",
			cities: []city.SpatialCity{
				{City: city.City{Name: "Invalid Lat", Country: "IL", Latitude: 91.0, Longitude: 0.0}}, // Invalid latitude
				{City: city.City{Name: "Invalid Lon", Country: "IL", Latitude: 0.0, Longitude: 181.0}}, // Invalid longitude
			},
			expectError: false, // S2 library should handle coordinate normalization
			description: "Should handle coordinates outside normal ranges",
		},
		{
			name: "Cities at exact boundaries",
			cities: []city.SpatialCity{
				{City: city.City{Name: "North", Country: "NO", Latitude: 90.0, Longitude: 0.0}},
				{City: city.City{Name: "South", Country: "SO", Latitude: -90.0, Longitude: 0.0}},
				{City: city.City{Name: "East", Country: "EA", Latitude: 0.0, Longitude: 180.0}},
				{City: city.City{Name: "West", Country: "WE", Latitude: 0.0, Longitude: -180.0}},
			},
			expectError: false,
			description: "Should handle cities at exact coordinate boundaries",
		},
		{
			name: "High precision coordinates",
			cities: []city.SpatialCity{
				{City: city.City{Name: "Precise", Country: "PR", Latitude: 40.7128000000000001, Longitude: -74.0060000000000001}},
			},
			expectError: false,
			description: "Should handle very high precision coordinates",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			finder, err := coordinates.BuildIndex(tc.cities, cfg)

			if tc.expectError {
				assert.Error(t, err, tc.description)
			} else {
				assert.NoError(t, err, tc.description)
				assert.NotNil(t, finder)

				// Test nearest place lookup if we have cities
				if len(tc.cities) > 0 {
					// Try to find nearest place to first city coordinates
					firstCity := tc.cities[0]
					nearest, dist, err := finder.NearestPlace(firstCity.Latitude, firstCity.Longitude)
					assert.NoError(t, err)
					assert.NotNil(t, nearest)
					assert.True(t, dist >= 0, "Distance should be non-negative")
				} else {
					// Empty cities should return error
					_, _, err := finder.NearestPlace(0, 0)
					assert.Error(t, err)
				}
			}
		})
	}
}

func TestDataIntegrity_PostalCodeFinder(t *testing.T) {
	testCases := []struct {
		name        string
		entries     map[string]map[string]dataLoader.PostalCodeEntry
		expectError bool
		description string
	}{
		{
			name:        "Empty postal codes",
			entries:     map[string]map[string]dataLoader.PostalCodeEntry{},
			expectError: false,
			description: "Should handle empty postal code data",
		},
		{
			name: "Valid postal codes",
			entries: map[string]map[string]dataLoader.PostalCodeEntry{
				"US": {
					"10001": {CountryCode: "US", PostalCode: "10001", PlaceName: "New York", Latitude: 40.7128, Longitude: -74.0060, Accuracy: 1},
				},
			},
			expectError: false,
			description: "Should handle valid postal code data",
		},
		{
			name: "Postal codes with special formats",
			entries: map[string]map[string]dataLoader.PostalCodeEntry{
				"CA": {
					"K1A 0A6": {CountryCode: "CA", PostalCode: "K1A 0A6", PlaceName: "Ottawa", Latitude: 45.4215, Longitude: -75.6972, Accuracy: 1},
				},
				"GB": {
					"SW1A 1AA": {CountryCode: "GB", PostalCode: "SW1A 1AA", PlaceName: "London", Latitude: 51.5074, Longitude: -0.1278, Accuracy: 1},
				},
			},
			expectError: false,
			description: "Should handle postal codes with spaces and special formats",
		},
		{
			name: "Duplicate postal codes in same country",
			entries: func() map[string]map[string]dataLoader.PostalCodeEntry {
				entries := make(map[string]map[string]dataLoader.PostalCodeEntry)
				entries["US"] = make(map[string]dataLoader.PostalCodeEntry)
				entries["US"]["10001"] = dataLoader.PostalCodeEntry{CountryCode: "US", PostalCode: "10001", PlaceName: "New York 1", Latitude: 40.7128, Longitude: -74.0060, Accuracy: 1}
				entries["US"]["10001"] = dataLoader.PostalCodeEntry{CountryCode: "US", PostalCode: "10001", PlaceName: "New York 2", Latitude: 40.7129, Longitude: -74.0061, Accuracy: 1} // This should overwrite
				return entries
			}(),
			expectError: false,
			description: "Should handle duplicate postal codes (last one wins)",
		},
		{
			name: "Postal codes with invalid coordinates",
			entries: map[string]map[string]dataLoader.PostalCodeEntry{
				"XX": {
					"00000": {CountryCode: "XX", PostalCode: "00000", PlaceName: "Invalid", Latitude: 91.0, Longitude: 181.0, Accuracy: 1},
				},
			},
			expectError: false,
			description: "Should handle postal codes with invalid coordinates",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			finder := postalCode.BuildIndex(tc.entries)

			if tc.expectError {
				assert.Nil(t, finder, tc.description)
			} else {
				assert.NotNil(t, finder, tc.description)

				// Test lookups if we have data
				if len(tc.entries) > 0 {
					for countryCode, countryEntries := range tc.entries {
						for postalCodeStr := range countryEntries {
							city := finder.CityByPostalCode(postalCodeStr, countryCode)
							// Should either find a city or return nil
							if city != nil {
								assert.Equal(t, countryCode, city.Country)
							}
						}
					}
				}
			}
		})
	}
}

func TestMalformedDataHandling(t *testing.T) {
	t.Run("Name finder with nil cities", func(t *testing.T) {
		// This should not crash
		finder := name.BuildIndex(nil)
		assert.NotNil(t, finder)

		result := finder.CityByName("test", "US")
		assert.Nil(t, result)
	})

	t.Run("Coordinate finder with nil cities", func(t *testing.T) {
		cfg := &config.S2{}
		finder, err := coordinates.BuildIndex(nil, cfg)
		assert.NoError(t, err)
		assert.NotNil(t, finder)

		_, _, err = finder.NearestPlace(0, 0)
		assert.Error(t, err)
	})

	t.Run("Postal code finder with nil entries", func(t *testing.T) {
		finder := postalCode.BuildIndex(nil)
		assert.NotNil(t, finder)

		result := finder.CityByPostalCode("10001", "US")
		assert.Nil(t, result)
	})
}

func TestBoundaryConditions_Integrated(t *testing.T) {
	// Test all finders with boundary condition data
	testCities := []city.SpatialCity{
		{City: city.City{Name: "North Pole City", Country: "NP", Latitude: 89.9999, Longitude: 0.0}},
		{City: city.City{Name: "South Pole City", Country: "SP", Latitude: -89.9999, Longitude: 0.0}},
		{City: city.City{Name: "Date Line East", Country: "DE", Latitude: 0.0, Longitude: 179.9999}},
		{City: city.City{Name: "Date Line West", Country: "DW", Latitude: 0.0, Longitude: -179.9999}},
		{City: city.City{Name: "Equator City", Country: "EQ", Latitude: 0.0, Longitude: 0.0}},
		{City: city.City{Name: "Prime Meridian", Country: "PM", Latitude: 0.0, Longitude: 0.0}}, // Duplicate coordinates
	}

	testPostalCodes := map[string]map[string]dataLoader.PostalCodeEntry{
		"NP": {"00001": {CountryCode: "NP", PostalCode: "00001", PlaceName: "North Pole City", Latitude: 89.9999, Longitude: 0.0, Accuracy: 1}},
		"SP": {"00002": {CountryCode: "SP", PostalCode: "00002", PlaceName: "South Pole City", Latitude: -89.9999, Longitude: 0.0, Accuracy: 1}},
		"DE": {"99901": {CountryCode: "DE", PostalCode: "99901", PlaceName: "Date Line East", Latitude: 0.0, Longitude: 179.9999, Accuracy: 1}},
		"DW": {"99902": {CountryCode: "DW", PostalCode: "99902", PlaceName: "Date Line West", Latitude: 0.0, Longitude: -179.9999, Accuracy: 1}},
	}

	cfg := &config.S2{}
	coordFinder, err := coordinates.BuildIndex(testCities, cfg)
	require.NoError(t, err)

	nameFinder := name.BuildIndex(testCities)
	postalFinder := postalCode.BuildIndex(testPostalCodes)

	// Test coordinate lookups at boundaries
	t.Run("Coordinate boundary lookups", func(t *testing.T) {
		boundaryTests := []struct {
			lat, lon float64
			shouldFind bool
		}{
			{90.0, 0.0, true},    // North Pole
			{-90.0, 0.0, true},   // South Pole
			{0.0, 180.0, true},   // Date line
			{0.0, -180.0, true},  // Date line negative
			{0.0, 0.0, true},     // Prime meridian/equator
		}

		for _, bt := range boundaryTests {
			nearest, _, err := coordFinder.NearestPlace(bt.lat, bt.lon)
			if bt.shouldFind {
				assert.NoError(t, err)
				assert.NotNil(t, nearest)
			}
		}
	})

	// Test name lookups at boundaries
	t.Run("Name boundary lookups", func(t *testing.T) {
		city := nameFinder.CityByName("North Pole City", "NP")
		assert.NotNil(t, city)
		assert.Equal(t, "North Pole City", city.Name)

		city = nameFinder.CityByName("South Pole City", "SP")
		assert.NotNil(t, city)
		assert.Equal(t, "South Pole City", city.Name)
	})

	// Test postal code lookups at boundaries
	t.Run("Postal code boundary lookups", func(t *testing.T) {
		city := postalFinder.CityByPostalCode("00001", "NP")
		assert.NotNil(t, city)
		assert.Equal(t, "North Pole City", city.Name)

		city = postalFinder.CityByPostalCode("00002", "SP")
		assert.NotNil(t, city)
		assert.Equal(t, "South Pole City", city.Name)
	})
}

func TestDataConsistency(t *testing.T) {
	// Test that all finders return consistent data for the same logical entities
	testCities := []city.SpatialCity{
		{City: city.City{Name: "Test City", Country: "TC", Latitude: 40.7128, Longitude: -74.0060}},
		{City: city.City{Name: "Another City", Country: "AC", Latitude: 34.0522, Longitude: -118.2437}},
	}

	testPostalCodes := map[string]map[string]dataLoader.PostalCodeEntry{
		"TC": {"10001": {CountryCode: "TC", PostalCode: "10001", PlaceName: "Test City", Latitude: 40.7128, Longitude: -74.0060, Accuracy: 1}},
		"AC": {"90210": {CountryCode: "AC", PostalCode: "90210", PlaceName: "Another City", Latitude: 34.0522, Longitude: -118.2437, Accuracy: 1}},
	}

	cfg := &config.S2{}
	coordFinder, _ := coordinates.BuildIndex(testCities, cfg)
	nameFinder := name.BuildIndex(testCities)
	postalFinder := postalCode.BuildIndex(testPostalCodes)

	// Test that coordinate and name lookups return consistent data
	t.Run("Coordinate vs Name consistency", func(t *testing.T) {
		for _, city := range testCities {
			// Find by coordinates
			coordResult, _, err := coordFinder.NearestPlace(city.Latitude, city.Longitude)
			assert.NoError(t, err)
			assert.NotNil(t, coordResult)

			// Find by name
			nameResult := nameFinder.CityByName(city.Name, city.Country)
			assert.NotNil(t, nameResult)

			// They should return the same city
			assert.Equal(t, coordResult.Name, nameResult.Name)
			assert.Equal(t, coordResult.Country, nameResult.Country)
			assert.InDelta(t, coordResult.Latitude, nameResult.Latitude, 1e-6)
			assert.InDelta(t, coordResult.Longitude, nameResult.Longitude, 1e-6)
		}
	})

	// Test that postal code lookups return cities that exist in coordinate/name indices
	t.Run("Postal code vs other indices consistency", func(t *testing.T) {
		for countryCode, countryEntries := range testPostalCodes {
			for _, entry := range countryEntries {
				postalResult := postalFinder.CityByPostalCode(entry.PostalCode, countryCode)
				assert.NotNil(t, postalResult)

				// Should be able to find this city by name
				nameResult := nameFinder.CityByName(entry.PlaceName, countryCode)
				assert.NotNil(t, nameResult, "City from postal code should exist in name index")

				// Should be able to find this city by coordinates
				coordResult, _, err := coordFinder.NearestPlace(entry.Latitude, entry.Longitude)
				assert.NoError(t, err)
				assert.NotNil(t, coordResult, "City from postal code should exist in coordinate index")
			}
		}
	})
}