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

// FindNearestCity wraps the S2Finder method
func (f *Finder) FindNearestCity(lat, lon float64, rank coordinates.Rank) (*city.City, float64, error) {
	c, dist, err := f.S2Finder.NearestPlace(lat, lon, rank)
	if err != nil {
		return nil, 0, err
	}
	return c, dist, nil
}
