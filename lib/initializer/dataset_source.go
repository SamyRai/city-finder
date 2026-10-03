package initializer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/SamyRai/cityFinder/lib/builder"
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
	postalCodes builder.PostalCodes
	loaded      bool
}

// load loads the raw datasets on first call and is a no-op afterwards. A
// failed load retries once after re-running ensureDatasets: a warm start that
// found all indexes skips the dataset ensure, so a rebuild triggered by a
// corrupt/legacy index may reach this point with the raw files absent. A load
// that succeeds but yields no cities is an error, not an empty index, and
// is not retried: see builder.ErrNoCities.
func (s *datasetSource) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	cities, postalCodes, err := loadData(ctx, s.cfg)
	if err != nil {
		if errors.Is(err, builder.ErrNoCities) {
			return err // the file is there; re-ensuring the datasets cannot fix it
		}
		if ensureErr := ensureDatasets(ctx, s.dl, s.cfg); ensureErr != nil {
			return fmt.Errorf("%v (additionally, ensuring the missing datasets failed: %v)", err, ensureErr)
		}
		if cities, postalCodes, err = loadData(ctx, s.cfg); err != nil {
			return err
		}
	}
	s.cities, s.postalCodes, s.loaded = cities, postalCodes, true
	return nil
}

// loadData parses the city and postal datasets named by cfg (see
// builder.Sources.Load).
func loadData(ctx context.Context, cfg *config.Config) ([]city.SpatialCity, builder.PostalCodes, error) {
	return builder.Sources{
		CitiesFile: filepath.Join(cfg.DatasetsFolder, cfg.AllCitiesFile),
		PostalFile: filepath.Join(cfg.DatasetsFolder, cfg.PostalCodesFile),
		Options: dataLoader.LoadOptions{
			ExcludeAdminDivisions: cfg.ExcludeAdminDivisions,
			IncludeFeatureClasses: cfg.IncludeFeatureClasses,
		},
	}.Load(ctx)
}
