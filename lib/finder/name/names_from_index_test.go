package name

import (
	"slices"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"

	"github.com/stretchr/testify/assert"
)

// namesFromIndexViaMap is the map-based implementation namesFromIndex used to
// have, kept as the reference the slice/sort/compact version must match.
func namesFromIndexViaMap(nf *Finder) []string {
	nameSet := make(map[string]struct{})
	for _, t := range nf.countries {
		for _, name := range t.names {
			nameSet[name] = struct{}{}
		}
	}
	for _, countryOverflow := range nf.overflow {
		for name := range countryOverflow {
			nameSet[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestNamesFromIndexMatchesMapReference compares the two on a fixture where
// the same name is indexed in several countries (and again in the overflow),
// the case the dedup exists for.
func TestNamesFromIndexMatchesMapReference(t *testing.T) {
	at := func(name, country string) city.SpatialCity {
		return city.SpatialCity{City: city.City{Name: name, Country: country, Latitude: 1, Longitude: 1}}
	}
	finder := BuildIndex([]city.SpatialCity{
		at("Springfield", "US"), at("Springfield", "AU"), at("Springfield", "GB"),
		at("Paris", "FR"), at("Paris", "US"),
		at("Berlin", "DE"),
		at("Zürich", "CH"), at("Åre", "SE"),
	})
	// Post-build additions land in the overflow: one repeats a table name in
	// a new country, one is brand new, one repeats an overflow name.
	finder.AddCity(at("Berlin", "US"))
	finder.AddCity(at("Zeta", "ZZ"))
	finder.AddCity(at("Zeta", "ZY"))

	got := finder.namesFromIndex()
	want := namesFromIndexViaMap(finder)
	assert.Equal(t, want, got)
	assert.True(t, slices.IsSorted(got))
	assert.Equal(t, []string{"Berlin", "Paris", "Springfield", "Zeta", "Zürich", "Åre"}, got)

	assert.Empty(t, NewNameFinder().namesFromIndex(), "an empty finder has no names")
}
