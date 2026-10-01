package finder

import (
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder/name"
)

// TestFinderWarmFuzzyPassThrough pins the thin wrapper: Finder.WarmFuzzy
// delegates to the name finder's warm-up, so the initializer can start the
// fuzzy build right after init and the first user typo query skips the
// degraded exact-only window. A typo resolves as soon as the background
// build lands.
func TestFinderWarmFuzzyPassThrough(t *testing.T) {
	cities := []city.SpatialCity{
		{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}},
		{City: city.City{Name: "London", Country: "GB", Latitude: 51.50, Longitude: -0.12}},
	}
	f := &Finder{NameFinder: name.BuildIndex(cities)}

	f.WarmFuzzy() // must delegate; no-op-safe on any state

	deadline := time.Now().Add(10 * time.Second)
	for {
		got := f.FindCityByName("Pars", "FR")
		if got != nil {
			if got.Name != "Paris" {
				t.Fatalf("typo resolved to %q, want Paris", got.Name)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("typo lookup never resolved: WarmFuzzy did not build the fuzzy index")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
