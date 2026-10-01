package name

import (
	"encoding/gob"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedCityFixture builds a dataset whose cities are each referenced under
// several names (primary + alternates), and one city that is manually indexed
// under two different countries — the shape the v2 id table must dedupe by
// pointer identity across both names and countries.
func sharedCityFixture() (*Finder, []*city.City) {
	paris := &city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35, Population: 2_161_000}
	london := &city.City{Name: "London", Country: "GB", Latitude: 51.50, Longitude: -0.12, Population: 8_982_000}

	f := NewNameFinder()
	// Cross-name sharing within one country.
	f.InvertedIndex["FR"] = map[string][]*city.City{
		"Paris":  {paris},
		"Lutèce": {paris},
		"Paname": {paris},
		"Orly":   {}, // empty ref list: preserved as a key with no ids
	}
	// Cross-country sharing: the SAME pointer under two countries. Real
	// builders never produce this, but the format must not corrupt it —
	// identity, not equality, defines a distinct city.
	f.InvertedIndex["FR"]["Paris"] = append(f.InvertedIndex["FR"]["Paris"], london)
	f.InvertedIndex["GB"] = map[string][]*city.City{
		"London":    {london},
		"Londres":   {london},
		"Big Smoke": {london, paris},
	}
	return f, []*city.City{paris, london}
}

// indexStats captures the quantities the count-validation gate compares.
type indexStats struct {
	distinctCities int
	totalRefs      int
	perCountryKeys map[string]int
}

func statsOf(f *Finder) indexStats {
	seen := make(map[*city.City]struct{})
	perCountry := make(map[string]int)
	total := 0
	for country, countryMap := range f.InvertedIndex {
		perCountry[country] = len(countryMap)
		for _, cityList := range countryMap {
			total += len(cityList)
			for _, c := range cityList {
				seen[c] = struct{}{}
			}
		}
	}
	return indexStats{distinctCities: len(seen), totalRefs: total, perCountryKeys: perCountry}
}

func serializeToTemp(t *testing.T, f *Finder) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "name_v2.gob")
	require.NoError(t, f.SerializeIndex(path))
	return path
}

// TestSerializeV2RoundTripDeepEqual proves the core v2 contract: serialize →
// deserialize yields an index deep-equal to the original, with the empty-ref
// key and the cross-country ref order preserved.
func TestSerializeV2RoundTripDeepEqual(t *testing.T) {
	original, _ := sharedCityFixture()
	path := serializeToTemp(t, original)

	restored, err := DeserializeIndex(path)
	require.NoError(t, err)

	assert.True(t, reflect.DeepEqual(original.InvertedIndex, restored.InvertedIndex),
		"v2 round trip must restore the inverted index exactly")
	assert.NotNil(t, restored.CityByName("Paris", "FR"))
	assert.NotNil(t, restored.CityByName("Londres", "GB"))
}

// TestSerializeV2PointerSharing pins the semantics the v1 format could not
// carry: after deserialize, the *city.City reachable under two different
// names is the SAME pointer — mutate through one, observe through the other.
func TestSerializeV2PointerSharing(t *testing.T) {
	original, _ := sharedCityFixture()
	path := serializeToTemp(t, original)

	restored, err := DeserializeIndex(path)
	require.NoError(t, err)

	// Cross-name sharing within a country.
	paris := restored.InvertedIndex["FR"]["Paris"][0]
	lutèce := restored.InvertedIndex["FR"]["Lutèce"][0]
	assert.Same(t, paris, lutèce, "one city under two names must deserialize to one pointer")

	// Cross-country sharing: the London entry inside FR must be the same
	// pointer as London inside GB.
	londonFR := restored.InvertedIndex["FR"]["Paris"][1]
	londonGB := restored.InvertedIndex["GB"]["London"][0]
	assert.Same(t, londonFR, londonGB, "one city under two countries must deserialize to one pointer")

	// Mutation through one reference is observable from every other.
	paris.Population = 42
	assert.Equal(t, int32(42), lutèce.Population, "mutating via one name must be visible via the other")
	assert.Equal(t, int32(42), restored.InvertedIndex["GB"]["Big Smoke"][1].Population,
		"mutating via one country must be visible via another")

	// The empty ref list must deserialize as a present-but-empty key, not as
	// a nil lookup.
	refs, exists := restored.InvertedIndex["FR"]["Orly"]
	assert.True(t, exists, "a name with zero refs must survive as a key")
	assert.Empty(t, refs)
}

// TestSerializeV2CountValidation is design gate 3: distinct-city count, total
// reference count, and per-country key counts are all preserved across the
// round trip.
func TestSerializeV2CountValidation(t *testing.T) {
	original, _ := sharedCityFixture()
	want := statsOf(original)
	require.Equal(t, 2, want.distinctCities, "fixture must hold exactly two distinct pointers")
	require.Equal(t, 8, want.totalRefs)

	path := serializeToTemp(t, original)
	restored, err := DeserializeIndex(path)
	require.NoError(t, err)

	got := statsOf(restored)
	assert.Equal(t, want.distinctCities, got.distinctCities, "distinct city count must be preserved")
	assert.Equal(t, want.totalRefs, got.totalRefs, "total reference count must be preserved")
	assert.Equal(t, want.perCountryKeys, got.perCountryKeys, "per-country key counts must be preserved")
}

// TestSerializeV2FileDoesNotContainRuntimeState verifies the dropped trailer:
// a v2 stream must decode header + payload and then END. Any trailing value
// (e.g. a serialized BK-tree, as v1 wrote) means the file was not written by
// this format.
func TestSerializeV2FileDoesNotContainRuntimeState(t *testing.T) {
	original, _ := sharedCityFixture()
	original.isBKTreeBuilt = true
	original.allNames = []string{"Paris", "London"}
	path := serializeToTemp(t, original)

	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()

	decoder := gob.NewDecoder(file)
	var header indexHeader
	require.NoError(t, decoder.Decode(&header))
	var payload nameIndexPayloadV2
	require.NoError(t, decoder.Decode(&payload))

	// Decode into the type v1 wrote next (bool isBKTreeBuilt): a clean v2
	// stream must be exhausted here, so the expected error is EOF — not a
	// type mismatch from leftover runtime state.
	var leftover bool
	err = decoder.Decode(&leftover)
	assert.ErrorIs(t, err, io.EOF,
		"the v2 stream must end after the payload: no BK-tree, no isBKTreeBuilt, no allNames")
}

// TestDeserializeV2HeaderCountMismatch rejects a payload whose country count
// disagrees with the header (the count-validation gate at file level).
func TestDeserializeV2HeaderCountMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad_count.gob")
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(&indexHeader{Magic: nameIndexMagic, Version: nameIndexVersion, Count: 7}))
	require.NoError(t, enc.Encode(&nameIndexPayloadV2{
		Cities: []city.City{{Name: "Paris", Country: "FR"}},
		Refs:   map[string]map[string][]int32{"FR": {"Paris": {0}}},
	}))
	require.NoError(t, f.Close())

	_, err = DeserializeIndex(path)
	assert.ErrorIs(t, err, ErrCorruptIndex, "a country-count mismatch is corruption")
	assert.Contains(t, err.Error(), "recorded 7")
}

// TestDeserializeV2RefOutsideCityTable rejects an id that points past the
// distinct-city table — the rehydration bounds check must return an error,
// not panic, on corrupt input.
func TestDeserializeV2RefOutsideCityTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad_ref.gob")
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(&indexHeader{Magic: nameIndexMagic, Version: nameIndexVersion, Count: 1}))
	require.NoError(t, enc.Encode(&nameIndexPayloadV2{
		Cities: []city.City{{Name: "Paris", Country: "FR"}},
		Refs:   map[string]map[string][]int32{"FR": {"Paris": {0, 99}}},
	}))
	require.NoError(t, f.Close())

	_, err = DeserializeIndex(path)
	assert.ErrorIs(t, err, ErrCorruptIndex, "an out-of-range city id is corruption")
	assert.Contains(t, err.Error(), "outside")
}

// TestAddCityAfterDeserializeV2 proves the post-deserialize mutation path:
// AddCity appends fresh pointers (not table ids), old and new lookups work,
// and re-serializing produces a valid v2 file whose id table was rebuilt
// cleanly over old shared pointers plus the new one.
func TestAddCityAfterDeserializeV2(t *testing.T) {
	original, _ := sharedCityFixture()
	path := serializeToTemp(t, original)

	restored, err := DeserializeIndex(path)
	require.NoError(t, err)

	// The added city is reachable under its primary name and an alt name.
	restored.AddCity(city.SpatialCity{
		City:     city.City{Name: "Berlin", Country: "DE", Latitude: 52.52, Longitude: 13.40, Population: 3_664_088},
		AltNames: []string{"Berlín"},
	})

	berlin := restored.CityByName("Berlin", "DE")
	require.NotNil(t, berlin, "the added city must be findable by exact name")
	assert.Equal(t, int32(3_664_088), berlin.Population)
	assert.NotNil(t, restored.CityByName("Berlín", "DE"), "the added alt name must be findable")
	assert.NotNil(t, restored.CityByName("Paris", "FR"), "pre-existing entries must keep resolving")

	// The added refs are new pointers: distinct from every table-rehydrated
	// pointer, and the two names see the same new pointer.
	de := restored.InvertedIndex["DE"]
	assert.Same(t, de["Berlin"][0], de["Berlín"][0], "AddCity's primary and alt names must share one pointer")

	// Re-serialize: the id table is rebuilt from scratch each time, so the
	// mix of rehydrated and added pointers must serialize and deserialize
	// cleanly, with pointer sharing intact on both sides.
	path2 := serializeToTemp(t, restored)
	again, err := DeserializeIndex(path2)
	require.NoError(t, err)

	assert.NotNil(t, again.CityByName("Berlin", "DE"), "the added city must survive a second round trip")
	assert.NotNil(t, again.CityByName("Lutèce", "FR"), "the original cities must survive a second round trip")

	want, got := statsOf(restored), statsOf(again)
	assert.Equal(t, want.distinctCities, got.distinctCities, "distinct city count across re-serialization")
	assert.Equal(t, want.totalRefs, got.totalRefs, "total refs across re-serialization")
	assert.Equal(t, want.perCountryKeys, got.perCountryKeys, "per-country keys across re-serialization")

	assert.Same(t, again.InvertedIndex["FR"]["Paris"][0], again.InvertedIndex["FR"]["Paname"][0],
		"pointer sharing must survive the second round trip")
	assert.Same(t, again.InvertedIndex["DE"]["Berlin"][0], again.InvertedIndex["DE"]["Berlín"][0],
		"the added city's sharing must survive the second round trip")
}
