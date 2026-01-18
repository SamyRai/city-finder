// Package finder provides test helpers for testing finder functionality
package finder

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

// TestHelper provides common test utilities for finder tests
type TestHelper struct {
	rng *rand.Rand
}

// NewTestHelper creates a new test helper with deterministic random seed
func NewTestHelper() *TestHelper {
	return &TestHelper{
		rng: rand.New(rand.NewSource(42)), // Fixed seed for reproducible tests
	}
}

// GenerateTestCities creates a slice of test cities with the specified count
func (h *TestHelper) GenerateTestCities(count int) []city.SpatialCity {
	cities := make([]city.SpatialCity, count)
	for i := 0; i < count; i++ {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      fmt.Sprintf("TestCity%d", i),
				Country:   fmt.Sprintf("C%d", h.rng.Intn(100)),
				Latitude:  h.rng.Float64()*180.0 - 90.0,  // -90 to 90
				Longitude: h.rng.Float64()*360.0 - 180.0, // -180 to 180
			},
		}
	}
	return cities
}

// GenerateBoundaryTestCities creates cities at geographical boundaries
func (h *TestHelper) GenerateBoundaryTestCities() []city.SpatialCity {
	return []city.SpatialCity{
		{City: city.City{Name: "North Pole", Country: "NP", Latitude: 90.0, Longitude: 0.0}},
		{City: city.City{Name: "South Pole", Country: "SP", Latitude: -90.0, Longitude: 0.0}},
		{City: city.City{Name: "Date Line East", Country: "DE", Latitude: 0.0, Longitude: 180.0}},
		{City: city.City{Name: "Date Line West", Country: "DW", Latitude: 0.0, Longitude: -180.0}},
		{City: city.City{Name: "Equator", Country: "EQ", Latitude: 0.0, Longitude: 0.0}},
		{City: city.City{Name: "Prime Meridian", Country: "PM", Latitude: 0.0, Longitude: 0.0}},
	}
}

// GenerateTestPostalCodes creates test postal code data
func (h *TestHelper) GenerateTestPostalCodes(count int) map[string]map[string]dataLoader.PostalCodeEntry {
	postalCodes := make(map[string]map[string]dataLoader.PostalCodeEntry)

	for i := 0; i < count; i++ {
		countryCode := fmt.Sprintf("C%d", h.rng.Intn(100))
		postalCode := fmt.Sprintf("%05d", h.rng.Intn(100000))

		if postalCodes[countryCode] == nil {
			postalCodes[countryCode] = make(map[string]dataLoader.PostalCodeEntry)
		}

		postalCodes[countryCode][postalCode] = dataLoader.PostalCodeEntry{
			CountryCode: countryCode,
			PostalCode:  postalCode,
			PlaceName:   fmt.Sprintf("TestCity%d", i),
			Latitude:    h.rng.Float64()*180.0 - 90.0,
			Longitude:   h.rng.Float64()*360.0 - 180.0,
			Accuracy:    h.rng.Intn(10),
		}
	}

	return postalCodes
}

// CreateTestFinders creates and initializes all finder types with test data
func (h *TestHelper) CreateTestFinders(cityCount, postalCount int) (*name.Finder, *coordinates.S2Finder, *postalCode.Finder, error) {
	cities := h.GenerateTestCities(cityCount)
	postalCodes := h.GenerateTestPostalCodes(postalCount)

	// Create name finder
	nameFinder := name.BuildIndex(cities)

	// Create coordinate finder
	cfg := &config.S2{}
	coordFinder, err := coordinates.BuildIndex(cities, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create coordinate finder: %w", err)
	}

	// Create postal code finder
	postalFinder := postalCode.BuildIndex(postalCodes)

	return nameFinder, coordFinder, postalFinder, nil
}

// BenchmarkHelper provides utilities for benchmarking
type BenchmarkHelper struct {
	TestHelper
}

// NewBenchmarkHelper creates a new benchmark helper
func NewBenchmarkHelper() *BenchmarkHelper {
	return &BenchmarkHelper{
		TestHelper: *NewTestHelper(),
	}
}

// TimeOperation times a function execution
func (b *BenchmarkHelper) TimeOperation(name string, fn func()) time.Duration {
	start := time.Now()
	fn()
	elapsed := time.Since(start)
	fmt.Printf("%s took %v\n", name, elapsed)
	return elapsed
}

// CreateTempFile creates a temporary file with the given content
func CreateTempFile(content string) (string, func(), error) {
	tmpfile, err := os.CreateTemp("", "test_*.txt")
	if err != nil {
		return "", nil, err
	}

	if _, err := tmpfile.WriteString(content); err != nil {
		tmpfile.Close()
		os.Remove(tmpfile.Name())
		return "", nil, err
	}
	tmpfile.Close()

	cleanup := func() {
		os.Remove(tmpfile.Name())
	}

	return tmpfile.Name(), cleanup, nil
}

// FindProjectRoot finds the project root directory
func FindProjectRoot() (string, error) {
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

// TestData represents test data for integration tests
type TestData struct {
	Cities      []city.SpatialCity
	PostalCodes map[string]map[string]dataLoader.PostalCodeEntry
}

// LoadTestData loads test data from files
func LoadTestData(cityFile, postalFile string, maxRecords int) (*TestData, error) {
	data := &TestData{}

	if cityFile != "" {
		cities, err := loadCitiesFromFile(cityFile, maxRecords)
		if err != nil {
			return nil, fmt.Errorf("failed to load cities: %w", err)
		}
		data.Cities = cities
	}

	if postalFile != "" {
		postalCodes, err := loadPostalCodesFromFile(postalFile, maxRecords)
		if err != nil {
			return nil, fmt.Errorf("failed to load postal codes: %w", err)
		}
		data.PostalCodes = postalCodes
	}

	return data, nil
}

// Helper functions for loading test data (simplified versions)
func loadCitiesFromFile(filepath string, maxRecords int) ([]city.SpatialCity, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cities []city.SpatialCity
	// Simplified implementation - in real usage would parse TSV format
	_ = maxRecords // Would use this to limit records
	return cities, nil
}

func loadPostalCodesFromFile(filepath string, maxRecords int) (map[string]map[string]dataLoader.PostalCodeEntry, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	postalCodes := make(map[string]map[string]dataLoader.PostalCodeEntry)
	// Simplified implementation - in real usage would parse TSV format
	_ = maxRecords // Would use this to limit records
	return postalCodes, nil
}