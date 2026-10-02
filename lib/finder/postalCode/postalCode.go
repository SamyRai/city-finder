package postalCode

import (
	"sync"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// Finder answers postal code lookups: one sorted countryTable per country.
type Finder struct {
	countries map[string]*countryTable
	legacy    bool // loaded from a v3 file (see LegacyFormat)
	mutex     sync.RWMutex
}

// NewPostalCodeFinder creates an empty Finder.
func NewPostalCodeFinder() *Finder {
	return &Finder{countries: make(map[string]*countryTable)}
}

// AddPostalCode adds (or replaces) one postal code entry.
func (pcf *Finder) AddPostalCode(e dataLoader.PostalCodeEntry) {
	pcf.mutex.Lock()
	defer pcf.mutex.Unlock()
	t := pcf.countries[e.CountryCode]
	if t == nil {
		t = &countryTable{}
		pcf.countries[e.CountryCode] = t
	}
	t.put(e.PostalCode, entryOf(e))
}

// BuildIndex creates a postal code index from the loader's country -> code ->
// entry map. Only the served fields are kept (see entry).
func BuildIndex(postalCodes map[string]map[string]dataLoader.PostalCodeEntry) *Finder {
	finder := NewPostalCodeFinder()
	for country, byCode := range postalCodes {
		finder.countries[country] = buildCountryTable(byCode)
	}
	return finder
}

// Len returns the number of indexed postal codes across all countries.
func (pcf *Finder) Len() int {
	pcf.mutex.RLock()
	defer pcf.mutex.RUnlock()
	n := 0
	for _, t := range pcf.countries {
		n += len(t.codes)
	}
	return n
}

// CityByPostalCode finds the nearest city by postal code and country code
func (pcf *Finder) CityByPostalCode(postalCode, countryCode string) *city.City {
	pcf.mutex.RLock()
	defer pcf.mutex.RUnlock()

	t := pcf.countries[countryCode]
	if t == nil {
		return nil
	}
	i, found := t.find(postalCode)
	if !found {
		return nil
	}
	e := t.entries[i]
	return &city.City{
		Latitude:  e.Latitude,
		Longitude: e.Longitude,
		Name:      e.PlaceName,
		Country:   countryCode,
	}
}
