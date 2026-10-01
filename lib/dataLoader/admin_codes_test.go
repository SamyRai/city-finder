package dataLoader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminLine builds a GeoNames allCountries-shaped line with the given field 10
// (admin1 code) and field 11 (admin2 code), both 0-indexed and tab-separated.
// Every other field is valid so the row survives the mandatory-field and
// coordinate checks.
func adminLine(field10, field11 string) string {
	return "5391959\tSan Francisco\tSan Francisco\tSF\t37.7749\t-122.4194\tP\tPPLC\tUS\tCA\t" +
		field10 + "\t" + field11 + "\t\t\t808437\t\t\t14\tAmerica/Los_Angeles\t2023-10-03"
}

// loadAdminFixture writes lines to a temp file and loads them through the
// production path, LoadGeoNamesCSVWithLimit.
func loadAdminFixture(t *testing.T, lines ...string) []city.SpatialCity {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin_test.txt")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cities, err := LoadGeoNamesCSVWithLimit(path, 0)
	require.NoError(t, err)
	return cities
}

func TestLoadGeoNames_ParsesAdmin1AndAdmin2Codes(t *testing.T) {
	cities := loadAdminFixture(t, adminLine("06", "075"))

	require.Len(t, cities, 1)
	assert.Equal(t, "06", cities[0].Admin1Code, "field 10 is the admin1 code")
	assert.Equal(t, "075", cities[0].Admin2Code, "field 11 is the admin2 code")
	assert.Equal(t, "US", cities[0].City.Country)
	assert.Equal(t, int32(808437), cities[0].City.Population, "the admin fields must not disturb population parsing")
}

func TestLoadGeoNames_EmptyAdminFieldsLoadEmptyAndKeepRow(t *testing.T) {
	// 57.7% of dump rows carry no admin2 code (and 2.1% no admin1): those
	// rows must survive with empty build-only codes, which the S2 build
	// records as id -1.
	cities := loadAdminFixture(t, adminLine("", ""))

	require.Len(t, cities, 1, "rows without admin codes must never be dropped")
	assert.Equal(t, "", cities[0].Admin1Code)
	assert.Equal(t, "", cities[0].Admin2Code)
}

func TestLoadGeoNames_Admin2WithoutAdmin1(t *testing.T) {
	// GeoNames occasionally has a populated admin2 under an empty admin1
	// (data quality noise). Both fields are independent strings; nothing
	// derives one from the other.
	cities := loadAdminFixture(t, adminLine("", "075"))
	require.Len(t, cities, 1)
	assert.Equal(t, "", cities[0].Admin1Code)
	assert.Equal(t, "075", cities[0].Admin2Code)
}

func TestLoadGeoNames_AdminCodesNotOnCity(t *testing.T) {
	// The wire/data contract guard: admin codes live on SpatialCity (build
	// path only). city.City — the struct the name index serializes and the
	// HTTP API embeds — must not grow the fields. This compiles only while
	// that stays true; the json-shape half of the pin lives in the routes
	// tests, which byte-pin the default /nearest response.
	cities := loadAdminFixture(t, adminLine("06", "075"))
	require.Len(t, cities, 1)
	assert.Equal(t, city.City{
		Latitude:   37.7749,
		Longitude:  -122.4194,
		Population: 808437,
		Name:       "San Francisco",
		Country:    "US",
	}, cities[0].City)
}

// --- admin1CodesASCII.txt loader ---

func writeAdmin1NamesFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin1CodesASCII.txt")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadAdmin1Names(t *testing.T) {
	path := writeAdmin1NamesFile(t,
		"US.CA\tCalifornia\tCalifornia\t5332921\n"+
			"AD.02\tCanillo\tCanillo\t3042004\n"+
			"ES.51\tComunidad de Madrid\tComunidad de Madrid\t3117735\n")

	names, err := LoadAdmin1Names(path)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"US.CA": "California",
		"AD.02": "Canillo",
		"ES.51": "Comunidad de Madrid",
	}, names)
}

func TestLoadAdmin1Names_SkipsMalformedLines(t *testing.T) {
	// A truncated tail (e.g. an interrupted download) or a stray blank line
	// must be skipped, not fatal: the names table is optional enhancement
	// data on top of an independently complete code table.
	path := writeAdmin1NamesFile(t,
		"US.CA\tCalifornia\tCalifornia\t5332921\n"+
			"\n"+
			"no-tabs-at-all\n"+
			"US.NV\tNevada\tNevada\t5509842\n")

	names, err := LoadAdmin1Names(path)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"US.CA": "California",
		"US.NV": "Nevada",
	}, names)
}

func TestLoadAdmin1Names_MissingFileIsError(t *testing.T) {
	// The error lets the initializer distinguish "configured but unreadable"
	// from "not configured"; only the latter is the silent codes-only mode.
	_, err := LoadAdmin1Names(filepath.Join(t.TempDir(), "absent.txt"))
	require.Error(t, err)
}
