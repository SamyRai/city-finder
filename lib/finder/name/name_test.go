package name

import (
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"os"
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

	// Deserialize the finder from the file
	deserializedFinder, err := DeserializeIndex(tmpfile.Name())
	assert.NoError(t, err)

	// Compare the original and deserialized finders
	assert.Equal(t, finder.InvertedIndex, deserializedFinder.InvertedIndex)

	// v2 does not serialize the BK-tree: the deserialized finder starts with
	// an empty tree and rebuilds it lazily on the first fuzzy lookup.
	assert.NotNil(t, deserializedFinder.BKTree)
	assert.Nil(t, deserializedFinder.BKTree.Root, "the BK-tree must not survive serialization in v2")
	got := deserializedFinder.CityByName("Test Citt", "TC") // distance-1 typo: forces the lazy rebuild
	if assert.NotNil(t, got, "fuzzy lookup must work via lazy rebuild after deserialize") {
		assert.Equal(t, "Test City", got.Name)
	}
}
