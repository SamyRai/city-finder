package builder

import (
	"context"
	"errors"
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

// ErrNoCities reports a city load that succeeded but yielded no rows.
// Building indexes from it would succeed, serialize empty files, and make
// every later boot a "warm" start that serves nothing (every nearest query a
// 500), so every caller refuses and writes nothing. It is not a load failure:
// re-ensuring the datasets cannot fix it, so callers must not retry on it.
var ErrNoCities = errors.New("no cities loaded")

// LoadCities parses the GeoNames city dump. An empty result is an error
// wrapping ErrNoCities that names the file and any filters that may have
// emptied it.
func (s Sources) LoadCities() ([]city.SpatialCity, error) {
	cities, err := dataLoader.LoadGeoNamesCSVWithOptions(s.CitiesFile, s.Options)
	if err != nil {
		return nil, err
	}
	if len(cities) == 0 {
		hint := "the file is empty, truncated or not a GeoNames dump"
		if len(s.Options.IncludeFeatureClasses) > 0 || s.Options.ExcludeAdminDivisions {
			hint += ", or include_feature_classes/exclude_admin_divisions filtered out every row"
		}
		return nil, fmt.Errorf("%w from %s (%s); refusing to build empty indexes", ErrNoCities, s.CitiesFile, hint)
	}
	return cities, nil
}

// LoadPostal parses the postal code dump. A file that fails to load is an
// error; one that loads with zero rows is allowed (lookups just find
// nothing), because the postal table is a separate, optional-in-content
// dataset.
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
	case errors.Is(citiesErr, ErrNoCities):
		return nil, nil, citiesErr
	case citiesErr != nil:
		return nil, nil, fmt.Errorf("failed to load GeoNames data from CSV: %v", citiesErr)
	case postalErr != nil:
		return nil, nil, fmt.Errorf("failed to load Postal Code data: %v", postalErr)
	case groupErr != nil:
		return nil, nil, groupErr // cancelled before a step started
	}
	return cities, postalCodes, nil
}
