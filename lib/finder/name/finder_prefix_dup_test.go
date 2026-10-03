package name

import (
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrefixNamesListsATableNameOnce: a post-build homonym of a table name
// (AddCity) is not listed a second time, and the listed city is the one
// CityByName resolves the name to.
func TestPrefixNamesListsATableNameOnce(t *testing.T) {
	finder := BuildIndex([]city.SpatialCity{
		{City: city.City{Name: "Springfield", Country: "US", Latitude: 39.8, Longitude: -89.6}},
		{City: city.City{Name: "Springdale", Country: "US", Latitude: 36.2, Longitude: -94.1}},
	})
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Springfield", Country: "US", Latitude: 42.1, Longitude: -72.6}})
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Springvale", Country: "US", Latitude: 43.5, Longitude: -70.8}})

	got := finder.PrefixNames("US", "Spring", 10)
	names := make([]string, len(got))
	for i, m := range got {
		names[i] = m.Name
	}
	assert.Equal(t, []string{"Springdale", "Springfield", "Springvale"}, names)

	resolved := finder.CityByName("Springfield", "US")
	require.NotNil(t, resolved)
	assert.Same(t, resolved, got[1].City, "the listed city is the one exact lookup returns")
}
