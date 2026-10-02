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

// flatFixtureCities is the exact-lookup fixture: homonyms within one
// country (distinguished by Population, insertion-ordered), the same name
// across countries, and one city referenced under an alternate name.
func flatFixtureCities() []city.SpatialCity {
	return []city.SpatialCity{
		{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35, Population: 100}, AltNames: []string{"Lutèce"}},
		{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.20, Longitude: 2.10, Population: 200}},   // homonym, second insert
		{City: city.City{Name: "Paris", Country: "US", Latitude: 39.84, Longitude: -88.70, Population: 300}}, // multi-country
		{City: city.City{Name: "London", Country: "GB", Latitude: 51.50, Longitude: -0.12, Population: 400}},
	}
}

// TestExactLookupFlatTable pins the exact phase over the sorted CSR tables:
// hits, misses, homonym first-inserted-wins order, per-country resolution,
// and pointer sharing between a primary name and its alternate.
func TestExactLookupFlatTable(t *testing.T) {
	finder := BuildIndex(flatFixtureCities())

	// Hit.
	paris := finder.CityByName("Paris", "FR")
	require.NotNil(t, paris, "exact hit must resolve")
	assert.Equal(t, int32(100), paris.Population,
		"homonyms must resolve to the first-inserted city (load order preserved by the CSR id order)")

	// The alternate name resolves to the SAME pointer (cross-name sharing
	// through the distinct-city table).
	lutèce := finder.CityByName("Lutèce", "FR")
	require.NotNil(t, lutèce)
	assert.Same(t, paris, lutèce, "a city's primary and alternate names must share one pointer")

	// The same name under another country resolves to that country's city.
	parisUS := finder.CityByName("Paris", "US")
	require.NotNil(t, parisUS)
	assert.Equal(t, int32(300), parisUS.Population, "per-country resolution must not leak FR's Paris into US")

	// Misses.
	assert.Nil(t, finder.CityByName("Atlantis", "FR"), "unknown name must miss")
	assert.Nil(t, finder.CityByName("Paris", "XX"), "unknown country must miss")
	assert.Nil(t, finder.CityByName("Paris", "GB"), "name known under other countries must miss in GB")
}

// TestAddCityVisibleThroughSortedAndOverflowPaths proves the post-build add
// contract: overflow names are visible to exact lookups, a homonym added
// after the build loses to the build-time winner (sorted table probes
// first), a brand-new country created only by AddCity works, and the sorted
// (build-time) names keep resolving alongside the overflow entries.
func TestAddCityVisibleThroughSortedAndOverflowPaths(t *testing.T) {
	finder := BuildIndex(flatFixtureCities())

	// Overflow: new name, existing country.
	finder.AddCity(city.SpatialCity{
		City:     city.City{Name: "Lyon", Country: "FR", Latitude: 45.76, Longitude: 4.84, Population: 500},
		AltNames: []string{"Lugdunum"},
	})
	lyon := finder.CityByName("Lyon", "FR")
	require.NotNil(t, lyon, "an AddCity name must be visible through the overflow probe")
	assert.Equal(t, int32(500), lyon.Population)
	lugdunum := finder.CityByName("Lugdunum", "FR")
	require.NotNil(t, lugdunum)
	assert.Same(t, lyon, lugdunum, "the added city's names must share one pointer")

	// Sorted entries keep resolving (probe order: table first, overflow
	// second — both must answer).
	assert.NotNil(t, finder.CityByName("Paris", "FR"))
	assert.NotNil(t, finder.CityByName("London", "GB"))

	// Overflow homonym: the added Paris must NOT displace the build-time
	// winner.
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Paris", Country: "FR", Population: 999}})
	assert.Equal(t, int32(100), finder.CityByName("Paris", "FR").Population,
		"a post-build homonym must lose to the build-time first-inserted winner")

	// Overflow-only country: AddCity created "ZZ" from nothing.
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Zurich", Country: "ZZ", Latitude: 47.37, Longitude: 8.54}})
	assert.NotNil(t, finder.CityByName("Zurich", "ZZ"), "a country created only by AddCity must resolve")
	assert.Nil(t, finder.CityByName("Zurich", "FR"), "the overflow country must stay per-country")
}

// prefixFixture builds 60 sorted "AlphaNN" names in PC plus decoys, for the
// PrefixNames matrix.
func prefixFixtureCities() []city.SpatialCity {
	cities := make([]city.SpatialCity, 0, 70)
	for i := 0; i < 60; i++ {
		cities = append(cities, city.SpatialCity{
			City: city.City{Name: fmt.Sprintf("Alpha%02d", i), Country: "PC", Latitude: 1, Longitude: 1, Population: int32(i)},
		})
	}
	cities = append(cities,
		city.SpatialCity{City: city.City{Name: "Beta", Country: "PC", Latitude: 1, Longitude: 1}},
		city.SpatialCity{City: city.City{Name: "Alphaville", Country: "OT", Latitude: 1, Longitude: 1}}, // same prefix, other country
	)
	return cities
}

func prefixNames(match []PrefixMatch) []string {
	out := make([]string, len(match))
	for i, m := range match {
		out[i] = m.Name
	}
	return out
}

// TestPrefixNames covers the autocomplete lookup over the sorted tables:
// basic range walk, limit handling (explicit, default, cap), unknown
// countries, overflow-added names, homonym winners, and probe order.
func TestPrefixNames(t *testing.T) {
	finder := BuildIndex(prefixFixtureCities())

	t.Run("basic sorted range", func(t *testing.T) {
		got := prefixNames(finder.PrefixNames("PC", "Alpha1", 10))
		assert.Equal(t, []string{"Alpha10", "Alpha11", "Alpha12", "Alpha13", "Alpha14", "Alpha15", "Alpha16", "Alpha17", "Alpha18", "Alpha19"}, got,
			"the sorted-table walk must return the contiguous prefix range in sorted order")
	})

	t.Run("explicit limit", func(t *testing.T) {
		got := prefixNames(finder.PrefixNames("PC", "Alpha1", 3))
		assert.Equal(t, []string{"Alpha10", "Alpha11", "Alpha12"}, got)
	})

	t.Run("input is capped at 50", func(t *testing.T) {
		got := finder.PrefixNames("PC", "Alpha", 1000)
		assert.Len(t, got, maxPrefixMatches, "any input over the cap must be clamped")
		assert.Equal(t, "Alpha00", got[0].Name)
	})

	t.Run("non-positive limit defaults to 10", func(t *testing.T) {
		for _, n := range []int{0, -5} {
			got := finder.PrefixNames("PC", "Alpha1", n)
			assert.Len(t, got, defaultPrefixMatches, "maxNames=%d must select the default", n)
		}
	})

	t.Run("unknown country returns nil", func(t *testing.T) {
		assert.Nil(t, finder.PrefixNames("XX", "Alpha", 10))
	})

	t.Run("no match in a known country", func(t *testing.T) {
		got := finder.PrefixNames("PC", "Zeta", 10)
		assert.Empty(t, got)
	})

	t.Run("prefix stays per-country", func(t *testing.T) {
		got := prefixNames(finder.PrefixNames("OT", "Alpha", 10))
		assert.Equal(t, []string{"Alphaville"}, got, "another country's Alpha names must not leak in")
	})

	t.Run("empty prefix yields the first names", func(t *testing.T) {
		got := prefixNames(finder.PrefixNames("PC", "", 10))
		assert.Equal(t, []string{"Alpha00", "Alpha01", "Alpha02", "Alpha03", "Alpha04", "Alpha05", "Alpha06", "Alpha07", "Alpha08", "Alpha09"}, got)
	})

	t.Run("overflow-added name comes after sorted matches", func(t *testing.T) {
		f := BuildIndex([]city.SpatialCity{
			{City: city.City{Name: "Alpine", Country: "AC", Latitude: 1, Longitude: 1}},
			{City: city.City{Name: "Alzette", Country: "AC", Latitude: 1, Longitude: 1}},
		})
		f.AddCity(city.SpatialCity{City: city.City{Name: "Albatros", Country: "AC", Latitude: 1, Longitude: 1}})
		// Sorted-table matches first (Alpine, Alzette), then the overflow
		// match (Albatros) — even though it would sort before them.
		got := prefixNames(f.PrefixNames("AC", "Al", 10))
		assert.Equal(t, []string{"Alpine", "Alzette", "Albatros"}, got)
	})

	t.Run("overflow-only country", func(t *testing.T) {
		f := NewNameFinder()
		f.AddCity(city.SpatialCity{City: city.City{Name: "Zurichsee", Country: "ZZ", Latitude: 1, Longitude: 1}})
		got := prefixNames(f.PrefixNames("ZZ", "Zurich", 10))
		assert.Equal(t, []string{"Zurichsee"}, got, "a country created only by AddCity is not unknown")
	})

	t.Run("homonym takes the first id", func(t *testing.T) {
		f := BuildIndex([]city.SpatialCity{
			{City: city.City{Name: "Prefixville", Country: "HM", Population: 1}},
			{City: city.City{Name: "Prefixville", Country: "HM", Population: 2}},
		})
		got := f.PrefixNames("HM", "Prefixville", 10)
		require.Len(t, got, 1)
		assert.Same(t, f.CityByName("Prefixville", "HM"), got[0].City,
			"PrefixNames must pair the name with the same first-inserted winner CityByName returns")
		assert.Equal(t, int32(1), got[0].City.Population)
	})
}

// TestDeserializeLegacyV2Fixture proves on-disk compatibility the other way
// around from the round-trip tests: the committed fixture was serialized by
// the pre-flatten build (sprint/v1.2 @ efa68f6), and the flat-table reader
// must decode it and serve every lookup with the original semantics.
func TestDeserializeLegacyV2Fixture(t *testing.T) {
	path := filepath.Join("testdata", "legacy_v2_index.gob")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("fixture %s missing (regenerate with the pre-flatten build)", path)
	}

	header, _ := readV2File(t, path)
	assert.Equal(t, nameIndexMagic, header.Magic)
	assert.Equal(t, nameIndexVersionV2, header.Version, "the fixture must be a v2 file")

	restored, err := DeserializeIndex(path)
	require.NoError(t, err, "a v2 file written by the pre-flatten build must deserialize cleanly")

	// Homonym order survived: first-inserted Paris (Population 100) wins.
	paris := restored.CityByName("Paris", "FR")
	require.NotNil(t, paris)
	assert.Equal(t, int32(100), paris.Population)

	// Cross-name sharing survived: the alternate name is the same pointer.
	lutèce := restored.CityByName("Lutèce", "FR")
	require.NotNil(t, lutèce)
	assert.Same(t, paris, lutèce)

	// Multi-country resolution survived.
	parisUS := restored.CityByName("Paris", "US")
	require.NotNil(t, parisUS)
	assert.Equal(t, int32(300), parisUS.Population)

	assert.NotNil(t, restored.CityByName("London", "GB"))
	assert.Nil(t, restored.CityByName("Atlantis", "FR"))

	// The legacy fixture also feeds PrefixNames: the decoded tables are
	// binary-searchable like natively built ones.
	got := prefixNames(restored.PrefixNames("FR", "Par", 10))
	assert.Equal(t, []string{"Paris"}, got)

	// Re-serializing the legacy-loaded index through the flat builder must
	// stay a valid v2 file with identical reference content.
	path2 := serializeToTemp(t, restored)
	again, err := DeserializeIndex(path2)
	require.NoError(t, err)
	assert.True(t, assert.ObjectsAreEqual(refsSnapshot(restored), refsSnapshot(again)),
		"legacy → flat → disk → flat must not change any reference")
}

// TestOverflowSurvivesRoundTrip proves overflow entries serialize: an
// AddCity-added name is visible after a serialize/deserialize cycle, with
// its pointer sharing intact and the count gates unchanged.
func TestOverflowSurvivesRoundTrip(t *testing.T) {
	finder := BuildIndex(flatFixtureCities())
	finder.AddCity(city.SpatialCity{
		City:     city.City{Name: "Berlin", Country: "DE", Population: 3_664_088},
		AltNames: []string{"Berlín"},
	})

	path := serializeToTemp(t, finder)
	restored, err := DeserializeIndex(path)
	require.NoError(t, err)

	berlin := restored.CityByName("Berlin", "DE")
	require.NotNil(t, berlin, "the overflow-added city must survive serialization")
	assert.Equal(t, int32(3_664_088), berlin.Population)
	berlín := restored.CityByName("Berlín", "DE")
	require.NotNil(t, berlín)
	assert.Same(t, berlin, berlín, "the added city's pointer sharing must survive")

	assert.Equal(t, statsOf(finder), statsOf(restored), "every count gate must survive the overflow round trip")
}
