package initializer

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

// Initialize ensures datasets are downloaded and extracted, and the indexes are built
func Initialize(cfg *config.Config) (*finder.Finder, error) {
	return InitializeContext(context.Background(), cfg)
}

// InitializeContext is Initialize with a context: cancelling it aborts a
// dataset download in flight and the pause between download attempts.
func InitializeContext(ctx context.Context, cfg *config.Config) (*finder.Finder, error) {
	dl := newDownloader()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := ensureDatasetsFolder(cfg); err != nil {
		return nil, err
	}
	release, err := acquireInitLock(cfg.DatasetsFolder)
	if err != nil {
		return nil, err
	}
	defer release()

	admin1NamesPath := ensureAdmin1NamesPath(ctx, dl, cfg)

	s2Path, namePath, postalPath := cfg.IndexFilePaths()
	if allIndexesPresent(s2Path, namePath, postalPath) {
		// Warm start: the raw datasets would only be needed to rebuild an
		// index, and datasetSource.load re-ensures them on demand in that
		// case. Skipping here avoids a ~442 MB re-download plus a ~1.9 GB
		// re-extract for volumes seeded with indexes but no datasets (or
		// after an operator deleted the raw files).
		log.Printf("all indexes present, skipping dataset ensure (delete an index file to force a refresh)")
	} else if err := ensureDatasets(ctx, dl, cfg); err != nil {
		return nil, err
	}
	return ensureFinders(ctx, dl, cfg, admin1NamesPath)
}

// ensureDatasetsFolder creates the datasets folder when missing. MkdirAll so
// nested paths (config datasets_folder: "data/datasets") work.
func ensureDatasetsFolder(cfg *config.Config) error {
	if _, err := os.Stat(cfg.DatasetsFolder); os.IsNotExist(err) {
		if err := os.MkdirAll(cfg.DatasetsFolder, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create datasets folder: %v", err)
		}
	}
	return nil
}

// ensureFinders returns the finders with the admin1 names attached.
//
// admin1NamesPath points at the OPTIONAL admin1CodesASCII.txt dataset: empty
// disables names entirely; a present file attaches the composite-key ->
// name map to the S2 finder; a configured-but-missing file degrades to
// codes-only mode with one log line (responses carry admin1 CODE, no name —
// the dataset is enhancement data, never a startup requirement). Names are
// attached on every boot (warm or cold): the map is ~120 KB and deliberately
// not serialized with the index, so updating the names file never
// invalidates it.
func ensureFinders(ctx context.Context, dl *downloader, cfg *config.Config, admin1NamesPath string) (*finder.Finder, error) {
	f, err := loadOrBuildFinders(ctx, dl, cfg)
	if err != nil {
		return nil, err
	}
	f.S2Finder.AttachAdmin1Names(ensureAdmin1Names(admin1NamesPath))
	return f, nil
}

// loadOrBuildFinders ensures that the indexes are built and serialized.
// When every serialized index already exists, the expensive dataset load
// (multi-GB TSV parse) is skipped entirely: each ensure*Index call then
// deserializes its index from disk instead. An index that fails to decode
// is rebuilt once from the source data (see the ensure*Index functions),
// which re-materializes the datasets on demand.
func loadOrBuildFinders(ctx context.Context, dl *downloader, cfg *config.Config) (*finder.Finder, error) {
	s2IndexPath, nameIndexPath, postalCodeIndexPath := cfg.IndexFilePaths()

	data := &datasetSource{cfg: cfg, dl: dl}
	if allIndexesPresent(s2IndexPath, nameIndexPath, postalCodeIndexPath) {
		log.Printf("all indexes present, skipping dataset load")

		// Warm start: the three decodes are independent, so they run
		// concurrently. Name decode dominates (~14 s of the ~20 s warm
		// start at prod); overlapping it with S2 (~4 s) and postal (~1 s)
		// removes the two smaller decodes from the critical path. Peak RSS
		// rises by the smaller indexes' transient buffers (~0.5–1 GB) —
		// within the chart's request/limit headroom. Any decode failure
		// falls through to the sequential ensure path below, which alone
		// owns rebuilds (datasetSource is not synchronized).
		type s2Result struct {
			finder *coordinates.S2Finder
			err    error
		}
		type nameResult struct {
			finder *name.Finder
			err    error
		}
		type postalResult struct {
			finder *postalCode.Finder
			err    error
		}
		s2Ch := make(chan s2Result, 1)
		nameCh := make(chan nameResult, 1)
		postalCh := make(chan postalResult, 1)
		go func() {
			f, err := coordinates.DeserializeIndex(s2IndexPath)
			s2Ch <- s2Result{f, err}
		}()
		go func() {
			f, err := name.DeserializeIndex(nameIndexPath)
			nameCh <- nameResult{f, err}
		}()
		go func() {
			f, err := postalCode.DeserializeIndex(postalCodeIndexPath)
			postalCh <- postalResult{f, err}
		}()
		s2Res, nameRes, postalRes := <-s2Ch, <-nameCh, <-postalCh
		if s2Res.err == nil && nameRes.err == nil {
			// The name index resolves its ids through the S2 index's city
			// table; an index that cannot attach is rebuilt by the
			// sequential path below.
			nameRes.err = attachNameIndex(nameIndexPath, nameRes.finder, s2Res.finder.Cities)
		}
		if s2Res.err == nil && nameRes.err == nil && postalRes.err == nil {
			migratePostalIndex(postalCodeIndexPath, postalRes.finder)
			return &finder.Finder{
				S2Finder:         s2Res.finder,
				NameFinder:       nameRes.finder,
				PostalCodeFinder: postalRes.finder,
			}, nil
		}
		log.Printf("warm decode incomplete (s2=%v, name=%v, postal=%v); falling back to sequential ensure",
			s2Res.err, nameRes.err, postalRes.err)
	} else if err := data.load(ctx); err != nil {
		return nil, err
	}

	s2Finder, err := ensureS2Index(ctx, s2IndexPath, cfg, data)
	if err != nil {
		return nil, err
	}
	nameFinder, err := ensureNameIndex(ctx, nameIndexPath, data, s2Finder.Cities)
	if err != nil {
		return nil, err
	}

	postalCodeFinder, err := ensurePostalCodeIndex(ctx, postalCodeIndexPath, data)
	if err != nil {
		return nil, err
	}

	return &finder.Finder{
		S2Finder:         s2Finder,
		NameFinder:       nameFinder,
		PostalCodeFinder: postalCodeFinder,
	}, nil
}
