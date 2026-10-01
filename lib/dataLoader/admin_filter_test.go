package dataLoader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// featureClassRow builds a GeoNames allCountries-shaped line with the given
// feature class (field 6, 0-based) and feature code (field 7). The slice
// literal keeps the tested index (6) auditable; every other field is valid so
// a row survives the mandatory-field and coordinate checks unless stated
// otherwise by an argument (e.g. an unparsable latitude).
func featureClassRow(geonameID, name, lat, lon, featureClass, featureCode, country, admin1, population string) string {
	return strings.Join([]string{
		geonameID,        // 0: geonameid
		name,             // 1: name
		name,             // 2: asciiname
		"",               // 3: alternatenames
		lat,              // 4: latitude
		lon,              // 5: longitude
		featureClass,     // 6: feature class
		featureCode,      // 7: feature code
		country,          // 8: country code
		"",               // 9: cc2
		admin1,           // 10: admin1 code
		"",               // 11: admin2 code
		"",               // 12: admin3 code
		"",               // 13: admin4 code
		population,       // 14: population
		"2811",           // 15: elevation
		"2348",           // 16: dem
		"Europe/Andorra", // 17: timezone
		"2023-10-03",     // 18: modification date
	}, "\t")
}

// fixtureRows is the canonical mixed fixture: two P-class populated places,
// an A-class ADM1 row (state), and an A-class PCLI row (country). The
// exclude_admin_divisions knob must keep the first two and drop the last two.
var fixtureRows = []string{
	featureClassRow("3062241", "Springfield", "42.58765", "1.7418", "P", "PPL", "AD", "02", "16316"),
	featureClassRow("2994701", "Roc Meler", "42.58765", "1.7418", "P", "PPL", "AD", "02", "16316"),
	featureClassRow("5332921", "California", "38.5", "-120.5", "A", "ADM1", "US", "CA", "38965193"),
	featureClassRow("6252001", "United States", "39.76", "-98.5", "A", "PCLI", "US", "", "331002651"),
}

// writeFixture writes rows to a temp TSV and returns its path.
func writeFixture(t *testing.T, rows ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "allCountries_fixture.txt")
	content := ""
	for _, r := range rows {
		content += r + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// loadedNames returns the city names in load order for order-sensitive asserts.
func loadedNames(cities []city.SpatialCity) []string {
	names := make([]string, len(cities))
	for i, c := range cities {
		names[i] = c.City.Name
	}
	return names
}

// TestLoadGeoNames_DefaultLoadKeepsFeatureClassARows pins the default (knob
// off) as the historical behavior: every row — including both feature class A
// rows — loads, the legacy signatures keep their meaning, and the options form
// with a zero value or an explicit false returns exactly what LoadGeoNamesCSV
// returns from the same file. No admin-division skip summary may be logged.
func TestLoadGeoNames_DefaultLoadKeepsFeatureClassARows(t *testing.T) {
	path := writeFixture(t, fixtureRows...)

	buf, restore := captureLoaderLogs(t)
	defer restore()

	legacy, err := LoadGeoNamesCSV(path)
	require.NoError(t, err)
	require.Len(t, legacy, 4, "default load keeps P-class and A-class rows alike")
	assert.Equal(t, []string{"Springfield", "Roc Meler", "California", "United States"}, loadedNames(legacy))
	assert.Equal(t, "CA", legacy[2].Admin1Code, "the ADM1 row's admin1 code must still decode")
	assert.Equal(t, int32(38965193), legacy[2].City.Population, "the ADM1 row's population must still decode")

	zeroOpts, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{})
	require.NoError(t, err)
	assert.Exactly(t, legacy, zeroOpts, "zero-value LoadOptions must be byte-identical to the legacy path")

	explicitFalse, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{ExcludeAdminDivisions: false})
	require.NoError(t, err)
	assert.Exactly(t, legacy, explicitFalse, "ExcludeAdminDivisions=false must be byte-identical to the legacy path")

	assert.NotContains(t, buf.String(), "admin division rows",
		"no admin-division skip summary may be logged when the knob is off")
}

// TestLoadGeoNames_ExcludeAdminDivisionsDropsExactlyClassA pins the enabled
// behavior on the mixed fixture: exactly the two A-class rows (ADM1 state and
// PCLI country) are dropped, the two P-class rows survive in order, and the
// skip count is summarized in ONE log line, postal-loader style.
func TestLoadGeoNames_ExcludeAdminDivisionsDropsExactlyClassA(t *testing.T) {
	path := writeFixture(t, fixtureRows...)

	buf, restore := captureLoaderLogs(t)
	defer restore()

	cities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{ExcludeAdminDivisions: true})
	require.NoError(t, err)

	require.Len(t, cities, 2, "exactly the two feature class A rows must be dropped")
	assert.Equal(t, []string{"Springfield", "Roc Meler"}, loadedNames(cities))

	logs := buf.String()
	require.Equal(t, 1, strings.Count(logs, "admin division rows"),
		"the skip summary must be logged exactly once, got: %q", logs)
	assert.Contains(t, logs, "skipped 2 admin division rows (feature class A)")
	assert.Contains(t, logs, "Loaded 2 cities", "the end-of-load summary reflects the post-filter count")
}

// TestLoadGeoNames_ExcludeAdminDivisionsStillsSkipsMalformedRows pins that
// enabling the knob changes nothing about the pre-existing malformed-row
// policy: short rows and rows with unparsable coordinates are still skipped
// (and not counted as admin divisions), under both knob settings.
func TestLoadGeoNames_ExcludeAdminDivisionsStillsSkipsMalformedRows(t *testing.T) {
	rows := []string{
		"1\tToo few fields", // short row: dropped by the mandatory-field check
		featureClassRow("2", "Bad Lat", "not-a-number", "1.7418", "P", "PPL", "AD", "02", "1000"), // unparsable latitude
		featureClassRow("3062241", "Springfield", "42.58765", "1.7418", "P", "PPL", "AD", "02", "16316"),
		featureClassRow("5332921", "California", "38.5", "-120.5", "A", "ADM1", "US", "CA", "38965193"),
	}
	path := writeFixture(t, rows...)

	t.Run("enabled: malformed rows skipped, A row excluded, P row kept", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		cities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{ExcludeAdminDivisions: true})
		require.NoError(t, err)
		require.Len(t, cities, 1, "only the valid P-class row survives")
		assert.Equal(t, "Springfield", cities[0].City.Name)
		assert.Contains(t, buf.String(), "skipped 1 admin division rows (feature class A)",
			"only the class-A row counts as an admin skip, not the malformed rows")
	})

	t.Run("default: malformed rows skipped, A row still loaded", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		cities, err := LoadGeoNamesCSV(path)
		require.NoError(t, err)
		require.Len(t, cities, 2, "the A-class row loads alongside the valid P-class row")
		assert.Equal(t, []string{"Springfield", "California"}, loadedNames(cities))
		assert.NotContains(t, buf.String(), "admin division rows")
		assert.Contains(t, buf.String(), "Error parsing lat",
			"the unparsable-latitude row is still reported by the pre-existing per-row error path")
	})
}

// TestLoadGeoNames_ClassACheckPrecedesFieldValidation pins the documented
// ordering: the feature-class check runs before the mandatory-field and
// coordinate checks, so a class-A row with a broken latitude is counted as an
// excluded admin division — never reaching (or logging) the lat parse error.
// With the knob off the same row follows the historical path: parse error,
// row dropped.
func TestLoadGeoNames_ClassACheckPrecedesFieldValidation(t *testing.T) {
	rows := []string{
		featureClassRow("6252001", "United States", "not-a-number", "-98.5", "A", "PCLI", "US", "", "331002651"),
		featureClassRow("3062241", "Springfield", "42.58765", "1.7418", "P", "PPL", "AD", "02", "16316"),
	}
	path := writeFixture(t, rows...)

	t.Run("enabled: counted as admin skip, no lat error logged", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		cities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{ExcludeAdminDivisions: true})
		require.NoError(t, err)
		require.Len(t, cities, 1)
		assert.Equal(t, "Springfield", cities[0].City.Name)
		assert.Contains(t, buf.String(), "skipped 1 admin division rows (feature class A)")
		assert.NotContains(t, buf.String(), "Error parsing lat",
			"a class-A row must not reach coordinate parsing and must not log a per-row error")
	})

	t.Run("default: historical parse-error path", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		cities, err := LoadGeoNamesCSV(path)
		require.NoError(t, err)
		require.Len(t, cities, 1)
		assert.Equal(t, "Springfield", cities[0].City.Name)
		assert.Contains(t, buf.String(), "Error parsing lat")
		assert.NotContains(t, buf.String(), "admin division rows")
	})
}
