// Package finder provides comprehensive test suites for all finder functionality
package finder

import (
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/stretchr/testify/suite"
)

// FinderTestSuite provides a comprehensive test suite for all finder functionality
type FinderTestSuite struct {
	suite.Suite
	helper *TestHelper
}

// SetupSuite is called once before all tests in the suite
func (suite *FinderTestSuite) SetupSuite() {
	suite.helper = NewTestHelper()
}

// SetupTest is called before each test method
func (suite *FinderTestSuite) SetupTest() {
	// Setup is done in each test method to avoid state issues
}

// TearDownSuite is called once after all tests in the suite
func (suite *FinderTestSuite) TearDownSuite() {
	// Cleanup if needed
}

// TestBasicFunctionality tests basic finder operations
func (suite *FinderTestSuite) TestBasicFunctionality() {
	// Create test data for this test
	cities := suite.helper.GenerateTestCities(10)
	nameFinder := name.BuildIndex(cities)
	coordFinder, err := coordinates.BuildIndex(cities)
	suite.NoError(err)

	// Test name finder
	result := nameFinder.CityByName(cities[0].Name, cities[0].Country)
	suite.NotNil(result)
	suite.Equal(cities[0].Name, result.Name)

	// Test coordinate finder
	nearest, distance, err := coordFinder.NearestPlace(cities[0].Latitude, cities[0].Longitude, coordinates.RankDistance)
	suite.NoError(err)
	suite.NotNil(nearest)
	suite.True(distance >= 0)
}

// TestEdgeCases tests various edge cases
func (suite *FinderTestSuite) TestEdgeCases() {
	// Test with boundary cities
	boundaryCities := suite.helper.GenerateBoundaryTestCities()
	boundaryFinder := name.BuildIndex(boundaryCities)

	for _, testCity := range boundaryCities {
		result := boundaryFinder.CityByName(testCity.Name, testCity.Country)
		suite.NotNil(result)
		suite.Equal(testCity.Name, result.Name)
	}

	// Test invalid inputs
	result := boundaryFinder.CityByName("", "")
	suite.Nil(result)
}

// TestConcurrency tests concurrent operations
func (suite *FinderTestSuite) TestConcurrency() {
	// Concurrency tests are implemented in separate test files
	suite.T().Skip("Concurrency tests implemented in separate test files")
}

// TestDataIntegrity tests data consistency
func (suite *FinderTestSuite) TestDataIntegrity() {
	// Create test data
	cities := suite.helper.GenerateTestCities(5)
	nameFinder := name.BuildIndex(cities)
	coordFinder, err := coordinates.BuildIndex(cities)
	suite.NoError(err)

	// Test that finders return consistent data
	for _, testCity := range cities {
		// Find by name
		nameResult := nameFinder.CityByName(testCity.Name, testCity.Country)
		suite.NotNil(nameResult)

		// Find by coordinates
		coordResult, _, err := coordFinder.NearestPlace(testCity.Latitude, testCity.Longitude, coordinates.RankDistance)
		suite.NoError(err)
		suite.NotNil(coordResult)

		// Results should be consistent
		suite.Equal(testCity.Name, nameResult.Name)
		suite.Equal(testCity.Country, nameResult.Country)
	}
}

// TestSuite runs the test suite
func TestFinderTestSuite(t *testing.T) {
	suite.Run(t, new(FinderTestSuite))
}

// PerformanceTestSuite provides performance regression tests
type PerformanceTestSuite struct {
	suite.Suite
	helper *BenchmarkHelper
}

// SetupSuite sets up the performance test suite
func (suite *PerformanceTestSuite) SetupSuite() {
	suite.helper = NewBenchmarkHelper()
}

// TestLookupPerformance tests lookup operation performance
func (suite *PerformanceTestSuite) TestLookupPerformance() {
	cities := suite.helper.GenerateTestCities(1000)
	finder := name.BuildIndex(cities)

	// Benchmark a series of lookups
	duration := suite.helper.TimeOperation("1000 name lookups", func() {
		for i := 0; i < 1000; i++ {
			city := cities[i%len(cities)]
			finder.CityByName(city.Name, city.Country)
		}
	})

	// Should complete reasonably quickly (adjust threshold based on system)
	suite.True(duration < 1*time.Second, "Lookups should complete in less than 1 second")
}

// TestSuite runs the performance test suite
func TestPerformanceTestSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance tests in short mode")
	}
	suite.Run(t, new(PerformanceTestSuite))
}
