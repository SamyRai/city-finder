package dataLoader

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// badCoordinateCases are the values the HTTP layer already rejects on input:
// non-finite and out-of-range. strconv.ParseFloat accepts every one of them,
// so only the shared validator keeps them out of the indexes.
var badCoordinateCases = []struct{ name, lat, lon string }{
	{"NaN lat", "NaN", "10"},
	{"NaN lon", "10", "NaN"},
	{"+Inf lat", "+Inf", "10"},
	{"-Inf lon", "10", "-Inf"},
	{"lat 90.0001", "90.0001", "10"},
	{"lat -91", "-91", "10"},
	{"lat 999", "999", "10"},
	{"lon 180.0001", "10", "180.0001"},
	{"lon -181", "10", "-181"},
	{"lon 999", "10", "999"},
}

func TestLoadGeoNames_RejectsNonFiniteAndOutOfRangeCoordinates(t *testing.T) {
	for _, tc := range badCoordinateCases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFixture(t,
				featureClassRow("1", "Bad", tc.lat, tc.lon, "P", "PPL", "AD", "02", "10"),
				featureClassRow("2", "Good", "42.5", "1.7", "P", "PPL", "AD", "02", "10"),
			)
			cities, err := LoadGeoNamesCSV(path)
			require.NoError(t, err)
			require.Len(t, cities, 1)
			assert.Equal(t, "Good", cities[0].City.Name)
		})
	}
}

func TestLoadGeoNames_AcceptsBoundaryCoordinates(t *testing.T) {
	path := writeFixture(t,
		featureClassRow("1", "NE", "90", "180", "P", "PPL", "AD", "02", "10"),
		featureClassRow("2", "SW", "-90", "-180", "P", "PPL", "AD", "02", "10"),
		featureClassRow("3", "Null", "0", "0", "P", "PPL", "AD", "02", "10"),
	)
	cities, err := LoadGeoNamesCSV(path)
	require.NoError(t, err)
	assert.Len(t, cities, 3)
}

// TestLoadGeoNames_NegativePopulationRowIsRejected pins the policy: GeoNames
// populations are non-negative integers, so a negative value marks a corrupt
// row. Rejecting it (rather than clamping to 0) keeps a damaged row out of
// name and population-rank answers instead of laundering it into a valid one.
func TestLoadGeoNames_NegativePopulationRowIsRejected(t *testing.T) {
	path := writeFixture(t,
		featureClassRow("1", "Neg", "42.5", "1.7", "P", "PPL", "AD", "02", "-7"),
		featureClassRow("2", "Zero", "42.5", "1.7", "P", "PPL", "AD", "02", "0"),
		featureClassRow("3", "Empty", "42.5", "1.7", "P", "PPL", "AD", "02", ""),
	)
	cities, err := LoadGeoNamesCSV(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"Zero", "Empty"}, loadedNames(cities))
}

func TestLoadGeoNames_SkipSummaryIsOneLinePerFile(t *testing.T) {
	var rows []string
	for i := 0; i < 20; i++ {
		rows = append(rows, featureClassRow(fmt.Sprint(i), "SECRETNAME", "NaN", "10", "P", "PPL", "AD", "02", "1"))
	}
	rows = append(rows,
		featureClassRow("30", "Neg", "10", "10", "P", "PPL", "AD", "02", "-1"),
		featureClassRow("31", "Good", "10", "10", "P", "PPL", "AD", "02", "1"),
	)
	buf, restore := captureLoaderLogs(t)
	defer restore()

	cities, err := LoadGeoNamesCSV(writeFixture(t, rows...))
	require.NoError(t, err)
	require.Len(t, cities, 1)

	var skipLines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "skipped") {
			skipLines = append(skipLines, l)
		}
	}
	require.Len(t, skipLines, 1, "logs: %q", buf.String())
	assert.Contains(t, skipLines[0], "21 malformed city rows")
	assert.Contains(t, skipLines[0], "20 non-finite or out-of-range coordinates (lines 1, 2, 3, 4, 5, ...)",
		"samples are capped")
	assert.Contains(t, skipLines[0], "1 negative population (line 21)")
	assert.NotContains(t, buf.String(), "SECRETNAME", "rejected rows must not be logged in full")
}

func TestValidCoordinate(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	for _, tc := range []struct {
		lat, lon float64
		want     bool
	}{
		{0, 0, true}, {90, 180, true}, {-90, -180, true},
		{90.0000001, 0, false}, {-90.0000001, 0, false},
		{0, 180.0000001, false}, {0, -180.0000001, false},
		{nan, 0, false}, {0, nan, false},
		{inf, 0, false}, {0, -inf, false},
	} {
		assert.Equal(t, tc.want, validCoordinate(tc.lat, tc.lon), "lat=%v lon=%v", tc.lat, tc.lon)
	}
}

func TestLoadPostalCodes_RejectsNonFiniteAndOutOfRangeCoordinates(t *testing.T) {
	for _, tc := range badCoordinateCases {
		t.Run(tc.name, func(t *testing.T) {
			content := postalRow("XX", "1", "Bad", "", "", "", "", "", "", tc.lat, tc.lon, "1") + validUSPostalRow()
			codes, err := LoadPostalCodes(writePostalFixture(t, content))
			require.NoError(t, err)
			assert.NotContains(t, codes, "XX")
			assert.Contains(t, codes["US"], "10005")
		})
	}
}
