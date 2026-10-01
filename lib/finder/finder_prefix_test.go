package finder

import (
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/stretchr/testify/assert"
)

// TestFinderPrefixNamesPassThrough pins the thin wrapper: Finder.PrefixNames
// delegates to the name finder's autocomplete lookup (sorted matches, limit
// handling), and a finder without a name index returns nil instead of
// panicking — the same nil-guard contract WarmFuzzy and FuzzyBuildState
// carry.
func TestFinderPrefixNamesPassThrough(t *testing.T) {
	cities := []city.SpatialCity{
		{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}},
		{City: city.City{Name: "Pau", Country: "FR", Latitude: 43.30, Longitude: -0.37}},
		{City: city.City{Name: "Perpignan", Country: "FR", Latitude: 42.70, Longitude: 2.90}},
	}
	f := &Finder{NameFinder: name.BuildIndex(cities)}

	got := f.PrefixNames("FR", "Pe", 10)
	if assert.Len(t, got, 1, "the wrapper must delegate to the sorted-table prefix walk") {
		assert.Equal(t, "Perpignan", got[0].Name)
		assert.NotNil(t, got[0].City, "each match must pair its first-referenced city")
	}

	assert.Len(t, f.PrefixNames("FR", "P", 2), 2, "limit must flow through the wrapper")
	assert.Nil(t, f.PrefixNames("XX", "P", 10), "unknown country must return nil through the wrapper")

	var bare Finder // no name index at all
	assert.Nil(t, bare.PrefixNames("FR", "P", 10), "a finder without a NameFinder must nil-guard, not panic")
}
