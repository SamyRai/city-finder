package dataLoader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// populationLine builds a GeoNames allCountries-shaped line with the given
// field 14 (0-indexed, tab-separated). Every other field is valid so the row
// survives the mandatory-field and coordinate checks; only the population
// field varies.
func populationLine(field14 string) string {
	return "2994701\tTestville\tTestville\tTestvil\t42.58765\t1.7418\tT\tPK\tAD\tAD,FR\t02\t\t\t\t" +
		field14 + "\t2811\t2348\tEurope/Andorra\t2023-10-03"
}

// loadPopulationFixture writes lines to a temp file and loads it through the
// production path, LoadGeoNamesCSVWithLimit.
func loadPopulationFixture(t *testing.T, lines ...string) []city.SpatialCity {
	t.Helper()
	path := filepath.Join(t.TempDir(), "population_test.txt")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cities, err := LoadGeoNamesCSVWithLimit(path, 0)
	require.NoError(t, err)
	return cities
}

func TestLoadGeoNames_ParsesPopulation(t *testing.T) {
	cities := loadPopulationFixture(t, populationLine("16316"))

	require.Len(t, cities, 1)
	assert.Equal(t, int32(16316), cities[0].City.Population)
	assert.Equal(t, "Testville", cities[0].City.Name)
	assert.Equal(t, "AD", cities[0].City.Country)
}

func TestLoadGeoNames_PopulationZeroIsZero(t *testing.T) {
	cities := loadPopulationFixture(t, populationLine("0"))
	require.Len(t, cities, 1)
	assert.Equal(t, int32(0), cities[0].City.Population)
}

func TestLoadGeoNames_EmptyPopulationFieldLoadsZeroAndKeepsRow(t *testing.T) {
	cities := loadPopulationFixture(t, populationLine(""))

	require.Len(t, cities, 1, "a row with an empty population field must never be dropped")
	assert.Equal(t, int32(0), cities[0].City.Population)
}

func TestLoadGeoNames_UnparsablePopulationLoadsZeroAndKeepsRow(t *testing.T) {
	for _, bad := range []string{"not-a-number", "1.5", "-=+", "9999999999999999999999"} {
		cities := loadPopulationFixture(t, populationLine(bad))
		require.Len(t, cities, 1, "row with unparsable population %q must be kept", bad)
		assert.Equal(t, int32(0), cities[0].City.Population, "unparsable population %q must load as 0", bad)
	}
}

func TestLoadGeoNames_PopulationOverflowClampsToZero(t *testing.T) {
	// 2^31 does not fit int32; the row survives with population 0 rather than
	// wrapping to a negative count.
	cities := loadPopulationFixture(t, populationLine("2147483648"))
	require.Len(t, cities, 1)
	assert.Equal(t, int32(0), cities[0].City.Population)
}

// A negative population marks a corrupt row and is rejected, not loaded.
func TestLoadGeoNames_PopulationMaxInt32AndNegative(t *testing.T) {
	cities := loadPopulationFixture(t,
		populationLine("2147483647"),
		populationLine("-12345"),
	)
	require.Len(t, cities, 1)
	assert.Equal(t, int32(2147483647), cities[0].City.Population)
}

func TestLoadGeoNames_PopulationMixedWithSkippedRows(t *testing.T) {
	// A valid populated row, a row dropped by the mandatory-field check, and
	// an empty-population row: only the empty-population semantics matter.
	cities := loadPopulationFixture(t,
		populationLine("8000000"),
		"2994702\tNoCountry\t\t\t1.0\t2.0\tT\tPK\t\t\t\t\t\t\t42\t\t\t\t2023-10-03",
		populationLine(""),
	)
	require.Len(t, cities, 2)
	assert.Equal(t, int32(8000000), cities[0].City.Population)
	assert.Equal(t, int32(0), cities[1].City.Population)
}
