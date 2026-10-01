package finder

import (
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

// Finder struct embeds all individual finders
type Finder struct {
	S2Finder         *coordinates.S2Finder
	NameFinder       *name.Finder
	PostalCodeFinder *postalCode.Finder
}

// FindCityByPostalCode wraps the PostalCodeFinder method
func (f *Finder) FindCityByPostalCode(postalCode, countryCode string) *city.City {
	return f.PostalCodeFinder.CityByPostalCode(postalCode, countryCode)
}

// FindCityByName wraps the NameFinder method
func (f *Finder) FindCityByName(name, countryCode string) *city.City {
	return f.NameFinder.CityByName(name, countryCode)
}

// WarmFuzzy wraps the NameFinder method: it starts the fuzzy (n-gram) index
// build in the background without blocking, so the initializer can call it
// right after init and the first user typo query skips the degraded
// exact-only window. Idempotent; no-op when the index is already built,
// building, or disabled. See name.Finder.WarmFuzzy for cost notes — the
// finished structure adds ~1.2 GiB resident at production scale.
func (f *Finder) WarmFuzzy() {
	if f.NameFinder == nil {
		return
	}
	f.NameFinder.WarmFuzzy()
}

// FuzzyBuildState wraps the NameFinder method: it reports the fuzzy index
// build state (see name.Finder.FuzzyBuildState) — 0 = not built,
// 1 = building, 2 = built, 3 = disabled. A finder without a name index
// reports 0.
func (f *Finder) FuzzyBuildState() int32 {
	if f.NameFinder == nil {
		return 0
	}
	return f.NameFinder.FuzzyBuildState()
}

// FindNearestCity wraps the S2Finder method
func (f *Finder) FindNearestCity(lat, lon float64, rank coordinates.Rank) (*city.City, float64, error) {
	c, dist, err := f.S2Finder.NearestPlace(lat, lon, rank)
	if err != nil {
		return nil, 0, err
	}
	return c, dist, nil
}
