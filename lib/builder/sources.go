package builder

import (
	"context"
	"fmt"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// PostalCodes is the postal dataset as the loader returns it: country code,
// then postal code, to its entry.
type PostalCodes = map[string]map[string]dataLoader.PostalCodeEntry

// Sources locates the raw datasets and carries the filters applied to the
// city rows. The filters apply only when an index is (re)built, so every
// caller threads them from the same config.
type Sources struct {
	CitiesFile string
	PostalFile string
	Options    dataLoader.LoadOptions
}

// LoadCities parses the GeoNames city dump.
func (s Sources) LoadCities() ([]city.SpatialCity, error) {
	return dataLoader.LoadGeoNamesCSVWithOptions(s.CitiesFile, s.Options)
}

// LoadPostal parses the postal code dump.
func (s Sources) LoadPostal() (PostalCodes, error) {
	return dataLoader.LoadPostalCodes(s.PostalFile)
}

// Load parses the city and postal datasets concurrently: they are
// independent files, and the postal parse (~1 s at production scale) hides
// behind the multi-second city parse. Both steps are drained before it
// returns, so a failure of one never leaves the other running. The city
// error is reported first when both fail, as it was when the parses ran in
// sequence. Any failure is fatal here; a caller that tolerates a missing
// postal dataset calls LoadCities and LoadPostal itself.
func (s Sources) Load(ctx context.Context) ([]city.SpatialCity, PostalCodes, error) {
	var (
		cities               []city.SpatialCity
		postalCodes          PostalCodes
		citiesErr, postalErr error
	)
	g := newGroup(ctx)
	g.Go(func(context.Context) error {
		cities, citiesErr = s.LoadCities()
		return citiesErr
	})
	g.Go(func(context.Context) error {
		postalCodes, postalErr = s.LoadPostal()
		return postalErr
	})
	groupErr := g.Wait()
	switch {
	case citiesErr != nil:
		return nil, nil, fmt.Errorf("failed to load GeoNames data from CSV: %v", citiesErr)
	case postalErr != nil:
		return nil, nil, fmt.Errorf("failed to load Postal Code data: %v", postalErr)
	case groupErr != nil:
		return nil, nil, groupErr // cancelled before a step started
	}
	return cities, postalCodes, nil
}
