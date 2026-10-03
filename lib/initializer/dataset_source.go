package initializer

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// datasetSource lazily loads and memoizes the raw datasets. A warm start
// (every index file present) skips the load entirely; if an index then fails
// to decode, the rebuild path materializes the data on demand exactly once
// instead of re-parsing for each rebuilt index.
type datasetSource struct {
	cfg         *config.Config
	dl          *downloader
	cities      []city.SpatialCity
	postalCodes map[string]map[string]dataLoader.PostalCodeEntry
	loaded      bool
}

// load loads the raw datasets on first call and is a no-op afterwards. A
// failed load retries once after re-running ensureDatasets: a warm start that
// found all indexes skips the dataset ensure, so a rebuild triggered by a
// corrupt/legacy index may reach this point with the raw files absent. A load
// that succeeds but yields no cities is an error, not an empty index: see
// errNoCities.
func (s *datasetSource) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	cities, postalCodes, err := loadData(ctx, s.cfg)
	if err != nil {
		if ensureErr := ensureDatasets(ctx, s.dl, s.cfg); ensureErr != nil {
			return fmt.Errorf("%v (additionally, ensuring the missing datasets failed: %v)", err, ensureErr)
		}
		if cities, postalCodes, err = loadData(ctx, s.cfg); err != nil {
			return err
		}
	}
	if len(cities) == 0 {
		return s.errNoCities()
	}
	s.cities, s.postalCodes, s.loaded = cities, postalCodes, true
	return nil
}

// errNoCities explains an empty city load. Building indexes from it would
// succeed, serialize empty files, and make every later boot a "warm" start
// that serves nothing (every nearest query a 500), so the boot fails instead
// and writes nothing; fix the data and restart. The postal table may be
// empty: it is a separate dataset and lookups just find nothing.
func (s *datasetSource) errNoCities() error {
	path := filepath.Join(s.cfg.DatasetsFolder, s.cfg.AllCitiesFile)
	hint := "the file is empty, truncated or not a GeoNames dump"
	if len(s.cfg.IncludeFeatureClasses) > 0 || s.cfg.ExcludeAdminDivisions {
		hint += ", or include_feature_classes/exclude_admin_divisions filtered out every row"
	}
	return fmt.Errorf("no cities loaded from %s (%s); refusing to build empty indexes", path, hint)
}

// loadData parses the city and postal datasets concurrently: they are
// independent files, and the postal parse (~1 s at production scale) hides
// behind the multi-second city parse. Both steps are drained before it
// returns, so a failure of one never leaves the other running. The city
// error is reported first when both fail, as it was when the parses ran in
// sequence.
func loadData(ctx context.Context, cfg *config.Config) ([]city.SpatialCity, map[string]map[string]dataLoader.PostalCodeEntry, error) {
	var (
		cities               []city.SpatialCity
		postalCodes          map[string]map[string]dataLoader.PostalCodeEntry
		citiesErr, postalErr error
	)
	g := newGroup(ctx)
	g.Go(func(context.Context) error {
		cities, citiesErr = dataLoader.LoadGeoNamesCSVWithOptions(
			filepath.Join(cfg.DatasetsFolder, cfg.AllCitiesFile),
			dataLoader.LoadOptions{
				ExcludeAdminDivisions: cfg.ExcludeAdminDivisions,
				IncludeFeatureClasses: cfg.IncludeFeatureClasses,
			},
		)
		return citiesErr
	})
	g.Go(func(context.Context) error {
		postalCodes, postalErr = dataLoader.LoadPostalCodes(filepath.Join(cfg.DatasetsFolder, cfg.PostalCodesFile))
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
