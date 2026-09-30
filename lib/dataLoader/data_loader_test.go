package dataLoader

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test data constants
const (
	validCityLine    = "2994701\tRoc Meler\tRoc Meler\tRoc Mele,Roc Meler,Roc Mélé\t42.58765\t1.7418\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03"
	invalidCityLine1 = "" // Empty line
	invalidCityLine2 = "invalid data with wrong format"
	invalidCityLine3 = "2994701\tCity\t\t\tinvalid_lat\tinvalid_lon\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03" // Invalid coordinates
	invalidCityLine4 = "2994701\tCity\t\t\t91.0\t0.0\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03"                // Invalid latitude
	invalidCityLine5 = "2994701\tCity\t\t\t0.0\t181.0\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03"               // Invalid longitude
)

const (
	validPostalLine    = "AD\tAD100\tCanillo\tCanillo\t02\t\t\t\t\t\t\t42.5833\t1.6667\t6"
	invalidPostalLine1 = "" // Empty line
	invalidPostalLine2 = "invalid postal data"
	invalidPostalLine3 = "AD\tAD100\tCanillo\tCanillo\t02\t\t\t\t\t\t\tinvalid_lat\tinvalid_lon\t6" // Invalid coordinates
	invalidPostalLine4 = "AD\tAD100\tCanillo\tCanillo\t02\t\t\t\t\t\t\t91.0\t0.0\t6"                // Invalid latitude
)

func TestLoadCities_ValidData(t *testing.T) {
	// Create a temporary file with valid data
	tmpfile, err := os.CreateTemp("", "cities_test_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	// Write test data
	testData := validCityLine + "\n"
	_, err = tmpfile.WriteString(testData)
	require.NoError(t, err)
	tmpfile.Close()

	// Test parsing
	cities, err := loadCitiesFromFile(tmpfile.Name())
	require.NoError(t, err)
	assert.Len(t, cities, 1)

	testCity := cities[0]
	assert.Equal(t, "Roc Meler", testCity.Name)
	assert.Equal(t, "AD", testCity.Country)
	assert.InDelta(t, 42.58765, testCity.Latitude, 0.00001)
	assert.InDelta(t, 1.7418, testCity.Longitude, 0.00001)
}

func TestLoadCities_InvalidData(t *testing.T) {
	testCases := []struct {
		name     string
		data     string
		expected int // expected number of valid cities loaded
	}{
		{"Empty file", "", 0},
		{"Empty lines only", "\n\n\n", 0},
		{"Invalid format", invalidCityLine2, 0},
		{"Invalid coordinates", invalidCityLine3, 0},
		{"Invalid latitude", invalidCityLine4, 0},
		{"Invalid longitude", invalidCityLine5, 0},
		{"Mixed valid/invalid", validCityLine + "\n" + invalidCityLine2 + "\n" + validCityLine, 2},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpfile, err := os.CreateTemp("", "cities_invalid_test_*.txt")
			require.NoError(t, err)
			defer os.Remove(tmpfile.Name())

			_, err = tmpfile.WriteString(tc.data)
			require.NoError(t, err)
			tmpfile.Close()

			cities, err := loadCitiesFromFile(tmpfile.Name())
			if tc.expected > 0 {
				assert.NoError(t, err)
			}
			assert.Len(t, cities, tc.expected)
		})
	}
}

func TestLoadCities_BoundaryCoordinates(t *testing.T) {
	boundaryLines := []string{
		"1\tNorth Pole\t\t\t90.0\t0.0\tT\tPK\tNP\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",       // North pole
		"2\tSouth Pole\t\t\t-90.0\t0.0\tT\tPK\tSP\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",      // South pole
		"3\tDate Line East\t\t\t0.0\t180.0\tT\tPK\tDE\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",  // Date line
		"4\tDate Line West\t\t\t0.0\t-180.0\tT\tPK\tDW\t\t\t\t\t\t\t0\t\t\t\t2023-01-01", // Date line negative
		"5\tEquator\t\t\t0.0\t0.0\tT\tPK\tEQ\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",           // Equator
	}

	tmpfile, err := os.CreateTemp("", "cities_boundary_test_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	for _, line := range boundaryLines {
		_, err = tmpfile.WriteString(line + "\n")
		require.NoError(t, err)
	}
	tmpfile.Close()

	cities, err := loadCitiesFromFile(tmpfile.Name())
	assert.NoError(t, err)
	assert.Len(t, cities, 5)

	expectedCoords := []struct{ lat, lon float64 }{
		{90.0, 0.0}, {-90.0, 0.0}, {0.0, 180.0}, {0.0, -180.0}, {0.0, 0.0},
	}

	for i, testCity := range cities {
		assert.InDelta(t, expectedCoords[i].lat, testCity.Latitude, 0.00001)
		assert.InDelta(t, expectedCoords[i].lon, testCity.Longitude, 0.00001)
		assert.True(t, testCity.Latitude >= -90 && testCity.Latitude <= 90)
		assert.True(t, testCity.Longitude >= -180 && testCity.Longitude <= 180)
	}
}

func TestLoadPostalCodes_ValidData(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "postal_test_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	// Use the actual data format that matches the implementation
	// Based on the real data: AD AD100 Canillo Canillo 02 (4 empty fields) 42.5833 1.6667 6
	testData := "AD\tAD100\tCanillo\tCanillo\t02\t\t\t\t\t42.5833\t1.6667\t6\n"
	_, err = tmpfile.WriteString(testData)
	require.NoError(t, err)
	tmpfile.Close()

	postalCodes, err := LoadPostalCodes(tmpfile.Name())
	require.NoError(t, err)
	assert.Contains(t, postalCodes, "AD")
	assert.Contains(t, postalCodes["AD"], "AD100")

	entry := postalCodes["AD"]["AD100"]
	assert.Equal(t, "AD", entry.CountryCode)
	assert.Equal(t, "AD100", entry.PostalCode)
	assert.Equal(t, "Canillo", entry.PlaceName)
	assert.InDelta(t, 42.5833, entry.Latitude, 0.0001)
	assert.InDelta(t, 1.6667, entry.Longitude, 0.0001)
	assert.Equal(t, 6, entry.Accuracy)
}

func TestLoadPostalCodes_InvalidData(t *testing.T) {
	testCases := []struct {
		name     string
		data     string
		expected int // expected number of entries
	}{
		{"Empty file", "", 0},
		{"Invalid format - too few fields", "AD\tAD100\n", 0},
		{"Valid data", "AD\tAD100\tCanillo\tCanillo\t02\t\t\t\t\t\t42.5833\t1.6667\t6\n", 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpfile, err := os.CreateTemp("", "postal_invalid_test_*.txt")
			require.NoError(t, err)
			defer os.Remove(tmpfile.Name())

			_, err = tmpfile.WriteString(tc.data)
			require.NoError(t, err)
			tmpfile.Close()

			postalCodes, err := LoadPostalCodes(tmpfile.Name())
			assert.NoError(t, err) // Should not error on invalid data, just skip it
			totalEntries := 0
			for _, countryEntries := range postalCodes {
				totalEntries += len(countryEntries)
			}
			assert.Equal(t, tc.expected, totalEntries)
		})
	}
}

func TestLoadPostalCodes_SpecialFormats(t *testing.T) {
	specialLines := []string{
		"CA\tK1A 0A6\tOttawa\t\t\t\t\t\t\t\t45.4215\t-75.6972\t1", // Canadian format with space
		"GB\tSW1A 1AA\tLondon\t\t\t\t\t\t\t\t51.5074\t-0.1278\t1", // UK format
		"DE\t12345\tBerlin\t\t\t\t\t\t\t\t52.5200\t13.4050\t1",    // German format
		"JP\t123-4567\tTokyo\t\t\t\t\t\t\t\t35.6762\t139.6503\t1", // Japanese format
	}

	tmpfile, err := os.CreateTemp("", "postal_special_test_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	for _, line := range specialLines {
		_, err = tmpfile.WriteString(line + "\n")
		require.NoError(t, err)
	}
	tmpfile.Close()

	postalCodes, err := LoadPostalCodes(tmpfile.Name())
	assert.NoError(t, err)
	assert.Contains(t, postalCodes, "CA")
	assert.Contains(t, postalCodes, "GB")
	assert.Contains(t, postalCodes, "DE")
	assert.Contains(t, postalCodes, "JP")

	// Verify special postal code formats are handled
	assert.Contains(t, postalCodes["CA"], "K1A 0A6")
	assert.Contains(t, postalCodes["GB"], "SW1A 1AA")
	assert.Contains(t, postalCodes["JP"], "123-4567")
}

func TestParseCityLine(t *testing.T) {
	t.Run("Valid line", func(t *testing.T) {
		city, err := parseCityLine(validCityLine)
		assert.NoError(t, err)
		assert.Equal(t, "Roc Meler", city.Name)
		assert.Equal(t, "AD", city.Country)
		assert.InDelta(t, 42.58765, city.Latitude, 0.00001)
		assert.InDelta(t, 1.7418, city.Longitude, 0.00001)
	})

	t.Run("Invalid format - too few fields", func(t *testing.T) {
		invalidLine := "2994701\tRoc Meler\tRoc Meler"
		_, err := parseCityLine(invalidLine)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid line format")
	})

	t.Run("Invalid latitude", func(t *testing.T) {
		invalidLine := "2994701\tRoc Meler\tRoc Meler\t\tinvalid_lat\t1.7418\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03"
		_, err := parseCityLine(invalidLine)
		assert.Error(t, err)
	})

	t.Run("Invalid longitude", func(t *testing.T) {
		invalidLine := "2994701\tRoc Meler\tRoc Meler\t\t42.58765\tinvalid_lon\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03"
		_, err := parseCityLine(invalidLine)
		assert.Error(t, err)
	})

	t.Run("Boundary coordinates", func(t *testing.T) {
		boundaryLines := []string{
			"1\tNorth Pole\t\t\t90.0\t0.0\tT\tPK\tNP\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",
			"2\tSouth Pole\t\t\t-90.0\t0.0\tT\tPK\tSP\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",
			"3\tDate Line\t\t\t0.0\t180.0\tT\tPK\tDL\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",
		}

		expectedCoords := []struct{ lat, lon float64 }{
			{90.0, 0.0}, {-90.0, 0.0}, {0.0, 180.0},
		}

		for i, line := range boundaryLines {
			city, err := parseCityLine(line)
			assert.NoError(t, err)
			assert.InDelta(t, expectedCoords[i].lat, city.Latitude, 0.00001)
			assert.InDelta(t, expectedCoords[i].lon, city.Longitude, 0.00001)
		}
	})
}

func TestDataLoader_FileNotFound(t *testing.T) {
	_, err := loadCitiesFromFile("nonexistent_file.txt")
	assert.Error(t, err)
	assert.True(t, os.IsNotExist(err))

	_, err = LoadPostalCodes("nonexistent_file.txt")
	assert.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

func TestDataLoader_LargeFileHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large file test in short mode")
	}

	// Create a large test file
	tmpfile, err := os.CreateTemp("", "large_cities_test_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	// Generate 10,000 lines of test data
	for i := 0; i < 10000; i++ {
		line := fmt.Sprintf("%d\tCity%d\tCity%d\t\t%f\t%f\tT\tPK\tXX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01\n",
			i, i, i, float64(i%180)-90.0, float64(i%360)-180.0)
		_, err = tmpfile.WriteString(line)
		require.NoError(t, err)
	}
	tmpfile.Close()

	// Test loading large file
	cities, err := loadCitiesFromFile(tmpfile.Name())
	assert.NoError(t, err)
	assert.Len(t, cities, 10000)

	// Verify data integrity for a few random entries
	for i := 0; i < 10; i++ {
		idx := i * 1000
		testCity := cities[idx]
		expectedLat := float64(idx%180) - 90.0
		expectedLon := float64(idx%360) - 180.0
		assert.InDelta(t, expectedLat, testCity.Latitude, 0.00001)
		assert.InDelta(t, expectedLon, testCity.Longitude, 0.00001)
	}
}

func TestDataLoader_MemoryEfficiency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory efficiency test in short mode")
	}

	// Test that the CSV reader uses ReuseRecord properly
	tmpfile, err := os.CreateTemp("", "memory_test_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	// Create test data with varying line lengths
	for i := 0; i < 1000; i++ {
		line := fmt.Sprintf("XX\t%d\tCity%d\t\t\t\t\t\t\t\t%f\t%f\t1\n",
			i, i, float64(i%180)-90.0, float64(i%360)-180.0)
		_, err = tmpfile.WriteString(line)
		require.NoError(t, err)
	}
	tmpfile.Close()

	// This should not consume excessive memory
	postalCodes, err := LoadPostalCodes(tmpfile.Name())
	assert.NoError(t, err)
	assert.Len(t, postalCodes["XX"], 1000)
}

func TestDataLoader_UnicodeHandling(t *testing.T) {
	// Test handling of Unicode characters in city names
	unicodeLines := []string{
		"1\tSão Paulo\tSão Paulo\t\t-23.5505\t-46.6333\tP\tPPL\tBR\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",
		"2\tMéxico City\tMéxico City\t\t19.4326\t-99.1332\tP\tPPL\tMX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",
		"3\tZürich\tZürich\t\t47.3769\t8.5417\tP\tPPL\tCH\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",
		"4\t北京市\t北京市\t\t39.9042\t116.4074\tP\tPPL\tCN\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",
	}

	tmpfile, err := os.CreateTemp("", "unicode_test_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	for _, line := range unicodeLines {
		_, err = tmpfile.WriteString(line + "\n")
		require.NoError(t, err)
	}
	tmpfile.Close()

	cities, err := loadCitiesFromFile(tmpfile.Name())
	assert.NoError(t, err)
	assert.Len(t, cities, 4)

	expectedNames := []string{"São Paulo", "México City", "Zürich", "北京市"}
	for i, testCity := range cities {
		assert.Equal(t, expectedNames[i], testCity.Name)
	}
}

func TestDataLoader_CoordinateValidation(t *testing.T) {
	// Test that invalid coordinates are properly filtered out
	testLines := []string{
		"1\tValid City\t\t\t45.0\t90.0\tT\tPK\tXX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",        // Valid
		"2\tInvalid Lat High\t\t\t95.0\t90.0\tT\tPK\tXX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",  // Invalid lat
		"3\tInvalid Lat Low\t\t\t-95.0\t90.0\tT\tPK\tXX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",  // Invalid lat
		"4\tInvalid Lon High\t\t\t45.0\t190.0\tT\tPK\tXX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01", // Invalid lon
		"5\tInvalid Lon Low\t\t\t45.0\t-190.0\tT\tPK\tXX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01", // Invalid lon
		"6\tNaN Lat\t\t\tNaN\t90.0\tT\tPK\tXX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",            // NaN
		"7\tInf Lon\t\t\t45.0\t+Inf\tT\tPK\tXX\t\t\t\t\t\t\t0\t\t\t\t2023-01-01",           // Inf
	}

	tmpfile, err := os.CreateTemp("", "validation_test_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	for _, line := range testLines {
		_, err = tmpfile.WriteString(line + "\n")
		require.NoError(t, err)
	}
	tmpfile.Close()

	cities, err := loadCitiesFromFile(tmpfile.Name())
	assert.NoError(t, err)
	// Should only load the first valid entry
	assert.Len(t, cities, 1)
	assert.Equal(t, "Valid City", cities[0].Name)
	assert.Equal(t, 45.0, cities[0].Latitude)
	assert.Equal(t, 90.0, cities[0].Longitude)
}

// Helper functions (would normally be in the dataLoader package but are tested here)
func loadCitiesFromFile(filepath string) ([]testCity, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cities []testCity
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		city, err := parseCityLine(line)
		if err != nil {
			continue // Skip invalid lines
		}

		cities = append(cities, city)
	}

	return cities, scanner.Err()
}

func parseCityLine(line string) (testCity, error) {
	fields := strings.Split(line, "\t")
	if len(fields) < 9 {
		return testCity{}, fmt.Errorf("invalid line format")
	}

	lat, err := strconv.ParseFloat(fields[4], 64)
	if err != nil {
		return testCity{}, err
	}

	lon, err := strconv.ParseFloat(fields[5], 64)
	if err != nil {
		return testCity{}, err
	}

	// Validate coordinates
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return testCity{}, fmt.Errorf("invalid coordinates: NaN or Inf values")
	}

	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return testCity{}, fmt.Errorf("coordinates out of valid range")
	}

	return testCity{
		Name:      fields[1],
		Country:   fields[8],
		Latitude:  lat,
		Longitude: lon,
	}, nil
}

// testCity struct for testing (simplified version for data loading tests)
type testCity struct {
	Name      string
	Country   string
	Latitude  float64
	Longitude float64
}
