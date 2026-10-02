package name

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shareFixture(n int) []city.SpatialCity {
	cities := make([]city.SpatialCity, n)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City:     city.City{Name: fmt.Sprintf("Place %d", i%(n/3+1)), Country: []string{"FR", "DE", "US"}[i%3], Latitude: float64(i) / 100, Longitude: float64(-i) / 100, Population: int32(i)},
			AltNames: []string{fmt.Sprintf("Alt %d", i)},
		}
	}
	return cities
}

// rowTable is the S2-side table: the input's City values in row order.
func rowTable(cities []city.SpatialCity) []city.City {
	t := make([]city.City, len(cities))
	for i := range cities {
		t[i] = cities[i].City
	}
	return t
}

func TestShareCitiesResolvesThroughTheSharedTable(t *testing.T) {
	cities := shareFixture(30_000) // concurrent build path
	f := BuildIndex(cities)
	before := f.CityByName("Alt 123", "FR")
	require.NotNil(t, before)

	table := rowTable(cities)
	require.NoError(t, f.ShareCities(table))
	assert.True(t, f.CitiesShared())

	after := f.CityByName("Alt 123", "FR")
	assert.Equal(t, *before, *after, "sharing must not change an answer")
	assert.Same(t, &table[123], after, "lookups resolve into the shared table itself")
}

func TestShareCitiesRefusesADifferentTable(t *testing.T) {
	cities := shareFixture(3_000)
	f := BuildIndex(cities)
	want := *f.CityByName("Alt 7", "DE")

	table := rowTable(cities)
	table[7].Population++ // one field of one city differs
	assert.ErrorIs(t, f.ShareCities(table), ErrCityTableMismatch)
	assert.ErrorIs(t, f.ShareCities(table[:10]), ErrCityTableMismatch, "length mismatch")
	assert.False(t, f.CitiesShared())
	assert.Equal(t, want, *f.CityByName("Alt 7", "DE"), "a refused share changes nothing")
}

func TestSharedIndexSerializesWithoutCitiesAndReattaches(t *testing.T) {
	cities := shareFixture(20_000)
	dir := t.TempDir()

	standalone := BuildIndex(cities)
	embeddedPath := filepath.Join(dir, "embedded.gob")
	require.NoError(t, standalone.SerializeIndex(embeddedPath))

	shared := BuildIndex(cities)
	table := rowTable(cities)
	require.NoError(t, shared.ShareCities(table))
	externalPath := filepath.Join(dir, "external.gob")
	require.NoError(t, shared.SerializeIndex(externalPath))

	embeddedInfo, _ := os.Stat(embeddedPath)
	externalInfo, _ := os.Stat(externalPath)
	assert.Less(t, externalInfo.Size(), embeddedInfo.Size(), "the external variant must not carry the city table")

	// The embedded file is self-contained.
	fromEmbedded, err := DeserializeIndex(embeddedPath)
	require.NoError(t, err)
	assert.Equal(t, *standalone.CityByName("Place 5", "US"), *fromEmbedded.CityByName("Place 5", "US"))

	// The external file loads detached: no answer until the table attaches.
	detached, err := DeserializeIndex(externalPath)
	require.NoError(t, err)
	assert.Nil(t, detached.CityByName("Place 5", "US"), "a detached index must not resolve ids")
	assert.Error(t, detached.SerializeIndex(filepath.Join(dir, "x.gob")), "a detached index cannot be re-serialized")

	wrong := rowTable(cities)
	wrong[0].Name = "Other"
	assert.ErrorIs(t, detached.ShareCities(wrong), ErrCityTableMismatch, "fingerprint must catch a different table")
	require.NoError(t, detached.ShareCities(rowTable(cities)))

	for i := 0; i < len(cities); i += 997 {
		for _, q := range append([]string{cities[i].Name}, cities[i].AltNames...) {
			assert.Equal(t, standalone.CityByName(q, cities[i].Country), detached.CityByName(q, cities[i].Country), q)
		}
	}
}

func TestAddCityAfterShareUsesExtras(t *testing.T) {
	cities := shareFixture(3_000)
	f := BuildIndex(cities)
	table := rowTable(cities)
	require.NoError(t, f.ShareCities(table))
	f.AddCity(city.SpatialCity{City: city.City{Name: "Newtown", Country: "FR", Latitude: 1}})
	c := f.CityByName("Newtown", "FR")
	require.NotNil(t, c)
	assert.Equal(t, 1.0, c.Latitude)
	assert.Len(t, table, 3_000, "AddCity must never grow the shared table")

	path := filepath.Join(t.TempDir(), "n.gob")
	require.NoError(t, f.SerializeIndex(path))
	g, err := DeserializeIndex(path)
	require.NoError(t, err)
	require.NoError(t, g.ShareCities(table))
	assert.Equal(t, *c, *g.CityByName("Newtown", "FR"), "post-build cities survive the round trip")
}

// TestShareCitiesRemapsAPermutedTable covers the legacy-migration case: an
// index whose own table holds the same cities in a different order (v2 files
// number cities by first encounter) is remapped onto the shared table's rows,
// and every answer stays identical.
func TestShareCitiesRemapsAPermutedTable(t *testing.T) {
	cities := shareFixture(9_000)
	cities = append(cities, cities[42]) // an exact duplicate row: equal values pair arbitrarily, harmlessly
	f := BuildIndex(cities)
	type q struct{ name, country string }
	var queries []q
	for i := 0; i < len(cities); i += 7 {
		queries = append(queries, q{cities[i].Name, cities[i].Country}, q{cities[i].AltNames[0], cities[i].Country})
	}
	want := make([]city.City, len(queries))
	for i, x := range queries {
		want[i] = *f.CityByName(x.name, x.country)
	}

	shuffled := rowTable(cities)
	for i := range shuffled { // deterministic permutation
		j := (i * 7919) % len(shuffled)
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	require.NoError(t, f.ShareCities(shuffled))
	for i, x := range queries {
		got := f.CityByName(x.name, x.country)
		require.NotNil(t, got)
		assert.Equal(t, want[i], *got, x)
	}
	prefix := f.PrefixNames("FR", "Alt 1", 10)
	assert.NotEmpty(t, prefix)
	for _, m := range prefix {
		assert.Equal(t, m.Name, "Alt "+m.Name[len("Alt "):], "prefix pairs stay well-formed")
	}
}
