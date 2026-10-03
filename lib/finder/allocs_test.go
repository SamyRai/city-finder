package finder_test

import (
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

// TestLookupAllocationCeilings pins the per-lookup allocation counts of the
// hot paths, so an allocation regression fails here instead of waiting for
// someone to read benchmark output. Raise a ceiling only with a reason.
func TestLookupAllocationCeilings(t *testing.T) {
	cities := []city.SpatialCity{
		{City: city.City{Name: "Berlin", Country: "DE", Latitude: 52.52, Longitude: 13.405, Population: 5}},
		{City: city.City{Name: "Bern", Country: "CH", Latitude: 46.948, Longitude: 7.447, Population: 3}},
	}
	nameIndex := name.BuildIndex(cities)
	s2Index, err := coordinates.BuildIndex(cities)
	if err != nil {
		t.Fatal(err)
	}
	postal := postalCode.BuildIndex(map[string]map[string]dataLoader.PostalCodeEntry{
		"DE": {"10115": {Latitude: 52.53, Longitude: 13.38, PlaceName: "Berlin"}},
	})

	for _, tc := range []struct {
		name    string
		ceiling float64
		pooled  bool // uses a sync.Pool, which the race detector randomly drains
		run     func()
	}{
		{"exact name lookup", 0, false, func() { nameIndex.CityByName("Berlin", "DE") }},
		{"prefix lookup", 1, false, func() { nameIndex.PrefixNames("DE", "Ber", 10) }},                       // the result slice
		{"postal lookup", 1, false, func() { postal.CityByPostalCode("10115", "DE") }},                       // the returned City
		{"nearest by distance", 13, true, func() { s2Index.NearestPlace(50, 10, coordinates.RankDistance) }}, // geo query internals
		{"nearest with admin", 13, true, func() { s2Index.NearestPlaceWithAdmin(50, 10, coordinates.RankDistance) }},
	} {
		if tc.pooled && raceEnabled {
			continue
		}
		if got := testing.AllocsPerRun(200, tc.run); got > tc.ceiling {
			t.Errorf("%s: %.1f allocs per lookup, ceiling %.0f", tc.name, got, tc.ceiling)
		}
	}
}
