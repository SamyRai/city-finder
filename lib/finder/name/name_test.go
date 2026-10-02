package name

import (
	"fmt"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"os"
	"reflect"
	"testing"
)

func TestFinder_SerializeDeserialize(t *testing.T) {
	// Create a new finder and add some data
	finder := NewNameFinder()
	finder.AddCity(city.SpatialCity{
		City: city.City{
			Name:    "Test City",
			Country: "TC",
		},
	})

	// Serialize the finder to a temporary file
	tmpfile, err := os.CreateTemp("", "test_name_finder_*.gob")
	assert.NoError(t, err)
	defer func() {
		_ = os.Remove(tmpfile.Name())
	}()

	err = finder.SerializeIndex(tmpfile.Name())
	assert.NoError(t, err)

	// Deserialize the finder from the temporary file
	deserializedFinder, err := DeserializeIndex(tmpfile.Name())
	assert.NoError(t, err)

	// Compare the original and deserialized finders: the per-country
	// name -> city-pointers view must survive the round trip unchanged
	// (the flat tables replaced the old InvertedIndex as the storage).
	assert.True(t, reflect.DeepEqual(refsSnapshot(finder), refsSnapshot(deserializedFinder)),
		"round trip must restore every (country, name, city) reference")

	// v2 does not serialize the fuzzy structure: the deserialized finder
	// starts without one and lazily rebuilds it on the first fuzzy lookup.
	deserializedFinder.mutex.RLock()
	ngrams := deserializedFinder.ngrams
	deserializedFinder.mutex.RUnlock()
	assert.Nil(t, ngrams, "no fuzzy structure may survive serialization in v2")
	deserializedFinder.WarmFuzzy()                          // trigger the background rebuild...
	waitFuzzyBuilt(t, deserializedFinder)                   // ...and wait it out before the fuzzy assertion
	got := deserializedFinder.CityByName("Test Citt", "TC") // distance-1 typo: resolves via the rebuilt index
	if assert.NotNil(t, got, "fuzzy lookup must work via lazy rebuild after deserialize") {
		assert.Equal(t, "Test City", got.Name)
	}
}

// TestAddCityDoesNotWriteCallerAltNames pins that AddCity never appends into
// the caller's AltNames backing array: with spare capacity, a plain
// append(AltNames, Name) would overwrite the element past len.
func TestAddCityDoesNotWriteCallerAltNames(t *testing.T) {
	backing := make([]string, 1, 2)
	backing[0] = "Alt"
	spare := backing[:2]
	spare[1] = "caller-owned"

	finder := NewNameFinder()
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Primary", Country: "TC"}, AltNames: backing})

	assert.Equal(t, "caller-owned", spare[1], "AddCity must not write past the caller's AltNames length")
	assert.NotNil(t, finder.CityByName("Primary", "TC"))
	assert.NotNil(t, finder.CityByName("Alt", "TC"))
}

// TestBuildIndexHomonymOrderIsLoadOrder pins that, for a name shared by many
// cities, the concurrent build resolves to the FIRST city in input order — on
// every build. The concurrent loader once merged worker results in completion
// order, so the winner varied between identical builds.
func TestBuildIndexHomonymOrderIsLoadOrder(t *testing.T) {
	const n = 200_000 // well above the sequential/concurrent cutoff
	cities := make([]city.SpatialCity, n)
	for i := range cities {
		cities[i] = city.SpatialCity{City: city.City{Name: fmt.Sprintf("Town%d", i%50), Country: "TC", Latitude: float64(i)}}
	}
	for run := 0; run < 4; run++ {
		f := BuildIndex(cities)
		for k := 0; k < 50; k++ {
			c := f.CityByName(fmt.Sprintf("Town%d", k), "TC")
			if assert.NotNil(t, c) {
				assert.Equal(t, float64(k), c.Latitude, "run %d: Town%d must resolve to its first occurrence", run, k)
			}
		}
	}
}
