package finder

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadRealCityData loads a subset of real city data for integration testing
func loadRealCityData(filepath string, maxRecords int) ([]city.SpatialCity, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cities []city.SpatialCity
	scanner := bufio.NewScanner(file)

	for scanner.Scan() && len(cities) < maxRecords {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 9 {
			continue // Skip malformed lines
		}

		// Parse coordinates
		lat, err := strconv.ParseFloat(fields[4], 64)
		if err != nil {
			continue // Skip lines with invalid latitude
		}

		lon, err := strconv.ParseFloat(fields[5], 64)
		if err != nil {
			continue // Skip lines with invalid longitude
		}

		// Skip records with invalid coordinates
		if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
			continue
		}

		// Basic coordinate validation
		if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			continue
		}

		spatialCity := city.SpatialCity{
			City: city.City{
				Name:      fields[1],
				Country:   fields[8],
				Latitude:  lat,
				Longitude: lon,
			},
		}

		cities = append(cities, spatialCity)
	}

	return cities, scanner.Err()
}

// loadRealPostalData loads a subset of real postal code data for integration testing
func loadRealPostalData(filepath string, maxRecords int) (map[string]map[string]dataLoader.PostalCodeEntry, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.ReuseRecord = true

	postalCodes := make(map[string]map[string]dataLoader.PostalCodeEntry)
	recordCount := 0

	for {
		record, err := reader.Read()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			continue // Skip malformed records
		}

		if len(record) < 12 || recordCount >= maxRecords {
			continue
		}

		// Parse coordinates
		lat, err := strconv.ParseFloat(record[9], 64)
		if err != nil {
			continue
		}

		lon, err := strconv.ParseFloat(record[10], 64)
		if err != nil {
			continue
		}

		accuracy, err := strconv.Atoi(record[11])
		if err != nil {
			continue
		}

		// Skip invalid coordinates
		if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
			continue
		}

		if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			continue
		}

		countryCode := record[0]
		postalCode := record[1]

		if postalCodes[countryCode] == nil {
			postalCodes[countryCode] = make(map[string]dataLoader.PostalCodeEntry)
		}

		postalCodes[countryCode][postalCode] = dataLoader.PostalCodeEntry{
			CountryCode: countryCode,
			PostalCode:  postalCode,
			PlaceName:   record[2],
			Latitude:    lat,
			Longitude:   lon,
			Accuracy:    accuracy,
		}

		recordCount++
	}

	return postalCodes, nil
}

func TestRealDataIntegration_NameFinder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real data integration test in short mode")
	}

	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	// Load real city data (limit to 1000 records for faster testing)
	cities, err := loadRealCityData(filepath.Join(projectRoot, "testdata", "allCountries.txt"), 1000)
	require.NoError(t, err)
	assert.True(t, len(cities) > 0, "Should load at least some real city data")

	// Build name finder
	finder := name.BuildIndex(cities)

	// Test lookups with real data
	successfulLookups := 0
	totalLookups := 0

	for _, testCity := range cities {
		if testCity.Name == "" || testCity.Country == "" {
			continue
		}

		totalLookups++
		result := finder.CityByName(testCity.Name, testCity.Country)

		if result != nil {
			successfulLookups++
			// Verify data integrity - focus on name/country matching for real data
			// Real data may have multiple locations for the same city name, so coordinate precision varies
			assert.Equal(t, testCity.Name, result.Name, "Name should match")
			assert.Equal(t, testCity.Country, result.Country, "Country should match")
			// Coordinates may vary due to multiple locations or data quality - just ensure they're reasonable
			assert.True(t, result.Latitude >= -90 && result.Latitude <= 90, "Latitude should be valid")
			assert.True(t, result.Longitude >= -180 && result.Longitude <= 180, "Longitude should be valid")
		}
	}

	// Should have reasonable success rate with real data
	successRate := float64(successfulLookups) / float64(totalLookups)
	assert.True(t, successRate > 0.8, "Should have >80%% successful lookups with real data, got %.2f", successRate)

	t.Logf("Real data name finder test: %d/%d successful lookups (%.1f%%)",
		successfulLookups, totalLookups, successRate*100)
}

func TestRealDataIntegration_CoordinateFinder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real data integration test in short mode")
	}

	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	// Load real city data (limit to 500 records for coordinate finder)
	cities, err := loadRealCityData(filepath.Join(projectRoot, "testdata", "allCountries.txt"), 500)
	require.NoError(t, err)
	assert.True(t, len(cities) > 0, "Should load at least some real city data")

	// Build coordinate finder
	cfg := &config.S2{}
	finder, err := coordinates.BuildIndex(cities, cfg)
	require.NoError(t, err)

	// Test coordinate lookups with real data
	successfulLookups := 0
	totalLookups := 0
	totalDistance := 0.0

	for _, testCity := range cities {
		totalLookups++

		// Search for the city using its own coordinates (should find itself or very close)
		result, distance, err := finder.NearestPlace(testCity.Latitude, testCity.Longitude, coordinates.RankDistance)

		if err == nil && result != nil {
			successfulLookups++
			totalDistance += distance

			// For exact coordinate matches, distance should be very small
			assert.True(t, distance < 1.0, "Distance should be small for real data lookup, got %.6f km", distance)

			// Verify coordinates are reasonable
			assert.True(t, result.Latitude >= -90 && result.Latitude <= 90, "Result latitude should be valid")
			assert.True(t, result.Longitude >= -180 && result.Longitude <= 180, "Result longitude should be valid")
		}
	}

	// Should have good success rate
	successRate := float64(successfulLookups) / float64(totalLookups)
	assert.True(t, successRate > 0.95, "Should have >95%% successful coordinate lookups, got %.2f", successRate)

	if successfulLookups > 0 {
		avgDistance := totalDistance / float64(successfulLookups)
		t.Logf("Real data coordinate finder: %d/%d successful lookups (%.1f%%), avg distance: %.6f km",
			successfulLookups, totalLookups, successRate*100, avgDistance)
	}
}

func TestRealDataIntegration_PostalCodeFinder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real data integration test in short mode")
	}

	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	// Load real postal code data (limit to 500 records)
	postalCodes, err := loadRealPostalData(filepath.Join(projectRoot, "testdata", "zipCodes.txt"), 500)
	require.NoError(t, err)
	assert.True(t, len(postalCodes) > 0, "Should load at least some real postal code data")

	// Build postal code finder
	finder := postalCode.BuildIndex(postalCodes)

	// Test postal code lookups with real data
	successfulLookups := 0
	totalLookups := 0

	for countryCode, countryEntries := range postalCodes {
		for postalCodeStr, expectedEntry := range countryEntries {
			totalLookups++

			result := finder.CityByPostalCode(postalCodeStr, countryCode)

			if result != nil {
				successfulLookups++

				// Verify data integrity
				assert.Equal(t, expectedEntry.PlaceName, result.Name, "Place name should match")
				assert.Equal(t, countryCode, result.Country, "Country should match")
				assert.InDelta(t, expectedEntry.Latitude, result.Latitude, 0.0001, "Latitude should match")
				assert.InDelta(t, expectedEntry.Longitude, result.Longitude, 0.0001, "Longitude should match")

				// Verify coordinates are valid
				assert.True(t, result.Latitude >= -90 && result.Latitude <= 90, "Latitude should be valid")
				assert.True(t, result.Longitude >= -180 && result.Longitude <= 180, "Longitude should be valid")
			}
		}
	}

	// Should have good success rate
	successRate := float64(successfulLookups) / float64(totalLookups)
	assert.True(t, successRate > 0.9, "Should have >90%% successful postal code lookups, got %.2f", successRate)

	t.Logf("Real data postal code finder: %d/%d successful lookups (%.1f%%)",
		successfulLookups, totalLookups, successRate*100)
}

func TestRealDataIntegration_CrossFinderConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real data integration test in short mode")
	}

	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	// Load real data
	cities, err := loadRealCityData(filepath.Join(projectRoot, "testdata", "allCountries.txt"), 200)
	require.NoError(t, err)

	postalCodes, err := loadRealPostalData(filepath.Join(projectRoot, "testdata", "zipCodes.txt"), 200)
	require.NoError(t, err)

	// Build all finders
	nameFinder := name.BuildIndex(cities)
	cfg := &config.S2{}
	coordFinder, err := coordinates.BuildIndex(cities, cfg)
	require.NoError(t, err)
	postalFinder := postalCode.BuildIndex(postalCodes)

	// Test cross-finder consistency with overlapping data
	consistencyChecks := 0
	consistentResults := 0

	// Check cities that appear in both city data and postal codes
	for _, testCity := range cities {
		if testCity.Name == "" || testCity.Country == "" {
			continue
		}

		// Try to find this city in postal codes
		for postalCountry, postalEntries := range postalCodes {
			if postalCountry != testCity.Country {
				continue
			}

			for _, postalEntry := range postalEntries {
				if strings.EqualFold(postalEntry.PlaceName, testCity.Name) {
					consistencyChecks++

					// Check that all finders return consistent coordinate information
					nameResult := nameFinder.CityByName(testCity.Name, testCity.Country)
					coordResult, _, coordErr := coordFinder.NearestPlace(testCity.Latitude, testCity.Longitude, coordinates.RankDistance)
					postalResult := postalFinder.CityByPostalCode(postalEntry.PostalCode, postalCountry)

					if nameResult != nil && coordErr == nil && coordResult != nil && postalResult != nil {
						// Check coordinate consistency (within reasonable bounds)
						coordDiff := math.Sqrt(
							math.Pow(nameResult.Latitude-postalResult.Latitude, 2) +
								math.Pow(nameResult.Longitude-postalResult.Longitude, 2))

						if coordDiff < 1.0 { // Within 1 degree (reasonable for city-level data)
							consistentResults++
						}
					}
				}
			}
		}
	}

	if consistencyChecks > 0 {
		consistencyRate := float64(consistentResults) / float64(consistencyChecks)
		t.Logf("Cross-finder consistency: %d/%d consistent results (%.1f%%)",
			consistentResults, consistencyChecks, consistencyRate*100)

		// Should have reasonable consistency (allowing for data quality issues)
		assert.True(t, consistencyRate > 0.5, "Should have >50%% cross-finder consistency, got %.2f", consistencyRate)
	} else {
		t.Log("No overlapping data found for cross-finder consistency testing")
	}
}

func TestRealData_EdgeCasesAndDataQuality(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real data edge case test in short mode")
	}

	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	// Load larger dataset to catch edge cases
	cities, err := loadRealCityData(filepath.Join(projectRoot, "testdata", "allCountries.txt"), 5000)
	require.NoError(t, err)

	// Analyze data quality and edge cases
	validCoords := 0
	extremeCoords := 0
	duplicateNames := make(map[string]map[string]int) // country -> name -> count

	for _, city := range cities {
		// Check coordinate validity
		if city.Latitude >= -90 && city.Latitude <= 90 &&
			city.Longitude >= -180 && city.Longitude <= 180 &&
			!math.IsNaN(city.Latitude) && !math.IsNaN(city.Longitude) &&
			!math.IsInf(city.Latitude, 0) && !math.IsInf(city.Longitude, 0) {
			validCoords++
		}

		// Check for extreme coordinates (poles, date line)
		if math.Abs(city.Latitude) > 80 || math.Abs(city.Longitude) > 170 {
			extremeCoords++
		}

		// Track duplicate names
		if duplicateNames[city.Country] == nil {
			duplicateNames[city.Country] = make(map[string]int)
		}
		duplicateNames[city.Country][city.Name]++
	}

	// Verify data quality
	validityRate := float64(validCoords) / float64(len(cities))
	assert.True(t, validityRate > 0.99, "Should have >99%% valid coordinates, got %.2f", validityRate)

	// Should find some extreme coordinates in real data (if available)
	// Note: Test data subset may not include extreme coordinates, so this is informational
	if extremeCoords == 0 {
		t.Log("No extreme coordinates found in test data subset - this is acceptable for limited test data")
	} else {
		t.Logf("Found %d extreme coordinates in test data", extremeCoords)
	}

	// Check for duplicate handling
	totalDuplicates := 0
	for _, nameCounts := range duplicateNames {
		for _, count := range nameCounts {
			if count > 1 {
				totalDuplicates++
			}
		}
	}

	t.Logf("Real data quality: %d/%d valid coordinates (%.1f%%), %d extreme coords, %d duplicate names",
		validCoords, len(cities), validityRate*100, extremeCoords, totalDuplicates)

	// Build finder and test with edge case data
	finder := name.BuildIndex(cities)

	// Test that duplicate names are handled (should return one of them)
	duplicateFound := false
	for country, nameCounts := range duplicateNames {
		for cityName, count := range nameCounts {
			if count > 1 {
				result := finder.CityByName(cityName, country)
				if result != nil {
					duplicateFound = true
					assert.Equal(t, cityName, result.Name)
					assert.Equal(t, country, result.Country)
					break
				}
			}
		}
		if duplicateFound {
			break
		}
	}

	if duplicateFound {
		t.Log("Successfully handled duplicate city names")
	}
}

func TestRealData_PerformanceValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real data performance test in short mode")
	}

	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	// Load moderate dataset for performance testing
	cities, err := loadRealCityData(filepath.Join(projectRoot, "testdata", "allCountries.txt"), 2000)
	require.NoError(t, err)

	// Test name finder performance
	nameFinder := name.BuildIndex(cities)

	// Benchmark lookups
	lookupCount := 10000
	startTime := time.Now()
	for i := 0; i < lookupCount; i++ {
		cityIndex := i % len(cities)
		testCity := cities[cityIndex]
		nameFinder.CityByName(testCity.Name, testCity.Country)
	}
	duration := time.Since(startTime)

	lookupsPerSecond := float64(lookupCount) / duration.Seconds()
	t.Logf("Real data performance: %.0f lookups/second", lookupsPerSecond)

	// Should be reasonably fast (adjust threshold based on system)
	assert.True(t, lookupsPerSecond > 1000, "Should handle >1000 lookups/second, got %.0f", lookupsPerSecond)
}

// findProjectRoot finds the project root directory
func findProjectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Walk up directories looking for go.mod
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd, nil
		}

		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}

	return "", fmt.Errorf("could not find project root")
}
