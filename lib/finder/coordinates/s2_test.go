package coordinates

import (
	"fmt"
	"os"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/stretchr/testify/assert"
)

var testCities = []city.SpatialCity{
	{City: city.City{Name: "San Francisco", Latitude: 37.7749, Longitude: -122.4194}},
	{City: city.City{Name: "New York", Latitude: 40.7128, Longitude: -74.0060}},
	{City: city.City{Name: "London", Latitude: 51.5074, Longitude: -0.1278}},
}

func TestBuildIndex(t *testing.T) {
	cfg := &config.S2{}
	finder, err := BuildIndex(testCities, cfg)

	assert.NoError(t, err)
	assert.NotNil(t, finder)
	assert.NotNil(t, finder.Index)
	assert.Len(t, finder.Cities, 3)
	assert.Equal(t, "San Francisco", finder.Cities[0].Name)
}

func TestNearestPlace(t *testing.T) {
	cfg := &config.S2{}
	finder, _ := BuildIndex(testCities, cfg)

	// Test case 1: Find city closest to SF
	sfLat, sfLon := 37.7750, -122.4190
	nearest, dist, err := finder.NearestPlace(sfLat, sfLon)
	assert.NoError(t, err)
	assert.NotNil(t, nearest)
	assert.Equal(t, "San Francisco", nearest.Name)
	assert.InDelta(t, 0.04, dist, 0.1) // Looser delta for distance

	// Test case 2: Find city closest to NYC
	nycLat, nycLon := 40.7128, -74.0060
	nearest, dist, err = finder.NearestPlace(nycLat, nycLon)
	assert.NoError(t, err)
	assert.NotNil(t, nearest)
	assert.Equal(t, "New York", nearest.Name)
	assert.InDelta(t, 0.0, dist, 0.1)

	// Test case 3: A point in the middle of the Atlantic
	midAtlanticLat, midAtlanticLon := 30.0, -40.0
	nearest, _, err = finder.NearestPlace(midAtlanticLat, midAtlanticLon)
	assert.NoError(t, err)
	assert.NotNil(t, nearest)
	assert.Equal(t, "New York", nearest.Name)
}

func TestSerialization(t *testing.T) {
	// Create a temporary file for the index
	tmpfile, err := os.CreateTemp("", "s2index_test.*.gob")
	assert.NoError(t, err)
	defer func() {
		_ = os.Remove(tmpfile.Name())
	}()

	// Build the initial finder and serialize it
	cfg := &config.S2{}
	finder, _ := BuildIndex(testCities, cfg)
	err = finder.SerializeIndex(tmpfile.Name())
	assert.NoError(t, err)

	// Deserialize the finder
	deserializedFinder, err := DeserializeIndex(tmpfile.Name())
	assert.NoError(t, err)
	assert.NotNil(t, deserializedFinder)
	assert.Len(t, deserializedFinder.Cities, 3)

	// Test that the deserialized finder works correctly
	sfLat, sfLon := 37.7750, -122.4190
	nearest, _, err := deserializedFinder.NearestPlace(sfLat, sfLon)
	assert.NoError(t, err)
	assert.NotNil(t, nearest)
	assert.Equal(t, "San Francisco", nearest.Name)
}

func TestEmptyCities(t *testing.T) {
	cfg := &config.S2{}
	finder, err := BuildIndex([]city.SpatialCity{}, cfg)
	assert.NoError(t, err)
	assert.NotNil(t, finder)

	_, _, err = finder.NearestPlace(0, 0)
	assert.Error(t, err)
	assert.Equal(t, "no city found", err.Error())
}

func TestSingleCity(t *testing.T) {
	cfg := &config.S2{}
	singleCityList := []city.SpatialCity{
		{City: city.City{Name: "Honolulu", Latitude: 21.3069, Longitude: -157.8583}},
	}
	finder, err := BuildIndex(singleCityList, cfg)
	assert.NoError(t, err)

	nearest, _, err := finder.NearestPlace(21.3, -157.8)
	assert.NoError(t, err)
	assert.NotNil(t, nearest)
	assert.Equal(t, "Honolulu", nearest.Name)
}

func TestCoordinatePrecision(t *testing.T) {
	cfg := &config.S2{}
	cities := []city.SpatialCity{
		{City: city.City{Name: "Precise Location", Latitude: 40.71280000000001, Longitude: -74.00600000000001}},
		{City: city.City{Name: "Nearby Location", Latitude: 40.7128, Longitude: -74.0060}},
	}
	finder, err := BuildIndex(cities, cfg)
	assert.NoError(t, err)

	// Test high precision coordinates
	nearest, dist, err := finder.NearestPlace(40.71280000000001, -74.00600000000001)
	assert.NoError(t, err)
	assert.NotNil(t, nearest)
	assert.Equal(t, "Precise Location", nearest.Name)
	assert.InDelta(t, 0.0, dist, 1e-10) // Very high precision for exact match

	// Test slight offset still finds correct city
	nearest, dist, err = finder.NearestPlace(40.7128000001, -74.0060000001)
	assert.NoError(t, err)
	assert.NotNil(t, nearest)
	assert.Equal(t, "Precise Location", nearest.Name)
	assert.True(t, dist < 0.01, "Distance should be very small for nearby coordinates")
}

func TestBoundaryConditions(t *testing.T) {
	cfg := &config.S2{}

	tests := []struct {
		name      string
		lat       float64
		lon       float64
		expectErr bool
	}{
		{"Valid coordinates", 40.7128, -74.0060, false},
		{"North Pole", 90.0, 0.0, false},
		{"South Pole", -90.0, 0.0, false},
		{"Prime Meridian", 0.0, 0.0, false},
		{"International Date Line", 0.0, 180.0, false},
		{"International Date Line negative", 0.0, -180.0, false},
		{"Equator", 0.0, 45.0, false},
		{"High latitude", 89.9999, 179.9999, false},
		{"Low latitude", -89.9999, -179.9999, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a city at the test coordinates
			cities := []city.SpatialCity{
				{City: city.City{Name: tt.name, Latitude: tt.lat, Longitude: tt.lon}},
			}
			finder, err := BuildIndex(cities, cfg)
			assert.NoError(t, err)

			// Try to find the city
			nearest, _, err := finder.NearestPlace(tt.lat, tt.lon)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, nearest)
				assert.Equal(t, tt.name, nearest.Name)
			}
		})
	}
}

func TestExtremeCoordinates(t *testing.T) {
	cfg := &config.S2{}

	// Test with cities at extreme coordinates
	extremeCities := []city.SpatialCity{
		{City: city.City{Name: "North Pole", Latitude: 90.0, Longitude: 0.0}},
		{City: city.City{Name: "South Pole", Latitude: -90.0, Longitude: 0.0}},
		{City: city.City{Name: "Date Line East", Latitude: 0.0, Longitude: 180.0}},
		{City: city.City{Name: "Date Line West", Latitude: 0.0, Longitude: -180.0}},
		{City: city.City{Name: "Max Precision", Latitude: 40.7128000000000001, Longitude: -74.0060000000000001}},
	}

	finder, err := BuildIndex(extremeCities, cfg)
	assert.NoError(t, err)

	// Test finding each extreme city
	for _, city := range extremeCities {
		nearest, dist, err := finder.NearestPlace(city.Latitude, city.Longitude)
		assert.NoError(t, err)
		assert.NotNil(t, nearest)
		assert.Equal(t, city.Name, nearest.Name)
		assert.InDelta(t, 0.0, dist, 1e-10)
	}
}

func TestDistanceAccuracy(t *testing.T) {
	cfg := &config.S2{}

	// Create cities with known distances
	nyc := city.SpatialCity{City: city.City{Name: "New York", Latitude: 40.7128, Longitude: -74.0060}}
	// Philadelphia is approximately 150km southwest of NYC
	phl := city.SpatialCity{City: city.City{Name: "Philadelphia", Latitude: 39.9526, Longitude: -75.1652}}

	finder, err := BuildIndex([]city.SpatialCity{nyc, phl}, cfg)
	assert.NoError(t, err)

	// Test distance from a point near Philadelphia to Philadelphia (should be close to 0)
	nearest, dist, err := finder.NearestPlace(39.9526, -75.1652)
	assert.NoError(t, err)
	assert.Equal(t, "Philadelphia", nearest.Name)
	assert.InDelta(t, 0.0, dist, 1e-6)

	// Test distance from a point between NYC and Philadelphia
	// This should find one of the cities, but not necessarily the expected distance
	// since the algorithm finds the closest city, not the distance to a specific city
	midPointLat := (40.7128 + 39.9526) / 2
	midPointLon := (-74.0060 + -75.1652) / 2
	nearest, dist, err = finder.NearestPlace(midPointLat, midPointLon)
	assert.NoError(t, err)
	assert.NotNil(t, nearest)
	assert.True(t, dist >= 0, "Distance should be non-negative")
	assert.True(t, dist < 200, "Distance should be reasonable for cities on the US East Coast")

	// Test exact match returns 0 distance
	nearest, dist, err = finder.NearestPlace(40.7128, -74.0060)
	assert.NoError(t, err)
	assert.Equal(t, "New York", nearest.Name)
	assert.InDelta(t, 0.0, dist, 1e-6)
}

func TestCoordinateNormalization(t *testing.T) {
	cfg := &config.S2{}

	// Test that coordinates are handled correctly (no built-in normalization in S2 finder)
	// This tests the robustness of the S2 library with edge coordinates
	cities := []city.SpatialCity{
		{City: city.City{Name: "Test City", Latitude: 40.7128, Longitude: -74.0060}},
	}

	finder, err := BuildIndex(cities, cfg)
	assert.NoError(t, err)

	// Test coordinates that might cause issues if not normalized properly
	testCoords := []struct {
		lat, lon   float64
		shouldFind bool
	}{
		{40.7128, -74.0060, true},         // Exact match
		{40.7128, -74.0060 + 360, true},   // Longitude + 360 (should work if normalized)
		{40.7128, -74.0060 - 360, true},   // Longitude - 360 (should work if normalized)
		{40.7128 + 0.001, -74.0060, true}, // Slightly off latitude
		{40.7128, -74.0060 + 0.001, true}, // Slightly off longitude
	}

	for _, tc := range testCoords {
		nearest, _, err := finder.NearestPlace(tc.lat, tc.lon)
		if tc.shouldFind {
			assert.NoError(t, err)
			assert.NotNil(t, nearest)
			assert.Equal(t, "Test City", nearest.Name)
		}
	}
}

func TestHighDensityArea(t *testing.T) {
	cfg := &config.S2{}

	// Create many cities in a small area to test high-density scenarios
	var cities []city.SpatialCity
	baseLat, baseLon := 40.7128, -74.0060

	for i := 0; i < 100; i++ {
		// Create cities within ~1km of each other
		lat := baseLat + float64(i%10)*0.001
		lon := baseLon + float64(i/10)*0.001
		cities = append(cities, city.SpatialCity{
			City: city.City{
				Name:      fmt.Sprintf("City%d", i),
				Latitude:  lat,
				Longitude: lon,
			},
		})
	}

	finder, err := BuildIndex(cities, cfg)
	assert.NoError(t, err)

	// Test finding cities in high-density area
	for i := 0; i < 10; i++ {
		lat := baseLat + float64(i%10)*0.001
		lon := baseLon + float64(i/10)*0.001
		nearest, dist, err := finder.NearestPlace(lat, lon)
		assert.NoError(t, err)
		assert.NotNil(t, nearest)
		assert.True(t, dist < 0.1, "Distance should be very small in high-density area, got %f", dist)
	}
}

func TestLargeCoordinateOffsets(t *testing.T) {
	cfg := &config.S2{}

	// Test with cities that have large coordinate differences
	cities := []city.SpatialCity{
		{City: city.City{Name: "Equator", Latitude: 0.0, Longitude: 0.0}},
		{City: city.City{Name: "North", Latitude: 85.0, Longitude: 0.0}},
		{City: city.City{Name: "South", Latitude: -85.0, Longitude: 0.0}},
		{City: city.City{Name: "East", Latitude: 0.0, Longitude: 175.0}},
		{City: city.City{Name: "West", Latitude: 0.0, Longitude: -175.0}},
	}

	finder, err := BuildIndex(cities, cfg)
	assert.NoError(t, err)

	// Test finding each city
	testCases := []struct {
		lat, lon float64
		expected string
	}{
		{0.0, 0.0, "Equator"},
		{85.0, 0.0, "North"},
		{-85.0, 0.0, "South"},
		{0.0, 175.0, "East"},
		{0.0, -175.0, "West"},
	}

	for _, tc := range testCases {
		nearest, dist, err := finder.NearestPlace(tc.lat, tc.lon)
		assert.NoError(t, err)
		assert.NotNil(t, nearest)
		assert.Equal(t, tc.expected, nearest.Name)
		assert.InDelta(t, 0.0, dist, 1e-6)
	}
}
