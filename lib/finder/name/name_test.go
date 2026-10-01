package name

import (
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
