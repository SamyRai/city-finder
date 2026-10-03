package initializer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

// allIndexesPresent reports whether every given file exists. Any stat error
// counts as missing so the caller falls back to the full load-and-build path.
func allIndexesPresent(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// pendingWrite serializes an index that was just built. The ensure*Index
// functions return one instead of writing, so the caller can run the three
// writes concurrently once every index exists in memory. It is nil when the
// index was loaded from its file and there is nothing to write.
type pendingWrite func() error

// writeAll runs the non-nil writes concurrently and returns the first
// failure after all of them have returned. They only read the finders
// (the serializers take their own read locks), so they cannot race each
// other; the builds stay sequential because three at once would add their
// transient peaks.
func writeAll(ctx context.Context, writes ...pendingWrite) error {
	g := newGroup(ctx)
	for _, write := range writes {
		if write == nil {
			continue
		}
		g.Go(func(context.Context) error { return write() })
	}
	return g.Wait()
}

// ensureS2Index returns the S2 index. A missing file builds the index from
// the source data and returns the write that serializes it. A file that
// exists but fails to decode (truncated by a crash mid-write, or written by
// an incompatible format version) is logged and rebuilt once the same way,
// the write replacing the bad file; a rebuild that also fails is fatal,
// exactly as a build failure is.
func ensureS2Index(ctx context.Context, s2IndexPath string, data *datasetSource) (*coordinates.S2Finder, pendingWrite, error) {
	log.Printf("Ensuring S2 index is built and serialized in %s", s2IndexPath)
	if _, errStat := os.Stat(s2IndexPath); os.IsNotExist(errStat) {
		if err := data.load(ctx); err != nil {
			return nil, nil, fmt.Errorf("failed to load datasets to build S2 index: %v", err)
		}
		return buildS2Index(s2IndexPath, data)
	}

	s2Finder, err := coordinates.DeserializeIndex(s2IndexPath)
	if err != nil {
		if !errors.Is(err, coordinates.ErrCorruptIndex) {
			return nil, nil, fmt.Errorf("failed to deserialize S2 index: %v", err)
		}
		log.Printf("Warning: %v", err)
		if err := data.load(ctx); err != nil {
			return nil, nil, fmt.Errorf("failed to load datasets needed to rebuild the S2 index: %v", err)
		}
		return buildS2Index(s2IndexPath, data)
	}
	return s2Finder, nil, nil
}

func buildS2Index(s2IndexPath string, data *datasetSource) (*coordinates.S2Finder, pendingWrite, error) {
	log.Printf("Building S2 index at %s", s2IndexPath)
	s2Finder, err := coordinates.BuildIndex(data.cities)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build S2 index: %v", err)
	}
	return s2Finder, func() error {
		if err := s2Finder.SerializeIndex(s2IndexPath); err != nil {
			return fmt.Errorf("failed to serialize S2 index: %v", err)
		}
		return nil
	}, nil
}

// ensureNameIndex returns the name index attached to the S2 index's city
// table (cities), building it when the file is missing and returning the
// write that serializes it. A file that exists but fails to decode with
// name.ErrCorruptIndex (truncated by a crash mid-write, legacy format, or a
// future version mismatch), or that cannot attach to cities (it was built
// against a different S2 index), is logged and rebuilt once from the source
// data, the write replacing the bad file. Any other error — for example a
// wrapped fs error from an unreadable file — is fatal, exactly as it is for
// the S2 and postal code indexes.
func ensureNameIndex(ctx context.Context, nameIndexPath string, data *datasetSource, cities []city.City) (*name.Finder, pendingWrite, error) {
	log.Printf("Ensuring name index is built and serialized in %s", nameIndexPath)
	if _, errStat := os.Stat(nameIndexPath); os.IsNotExist(errStat) {
		if err := data.load(ctx); err != nil {
			return nil, nil, fmt.Errorf("failed to load datasets to build name index: %v", err)
		}
		return buildNameIndex(nameIndexPath, data, cities)
	}

	nameFinder, err := name.DeserializeIndex(nameIndexPath)
	if err == nil {
		err = attachNameIndex(nameIndexPath, nameFinder, cities)
	}
	if err != nil {
		if !errors.Is(err, name.ErrCorruptIndex) && !errors.Is(err, name.ErrCityTableMismatch) {
			return nil, nil, fmt.Errorf("failed to deserialize name index: %v", err)
		}
		log.Printf("Warning: %v", err)
		if err := data.load(ctx); err != nil {
			return nil, nil, fmt.Errorf("failed to load datasets needed to rebuild the name index: %v", err)
		}
		return buildNameIndex(nameIndexPath, data, cities)
	}
	return nameFinder, nil, nil
}

// attachNameIndex makes a loaded name index resolve through the S2 index's
// city table, so every city is held once per process. A file that embeds its
// own copy (the legacy v2 format, or a standalone v3 file) and matches is
// re-serialized in the compact format that only references the table — a
// one-time, in-place migration that needs no dataset download. A failed
// migration write is logged and ignored: the attached index is already
// correct, and the next boot retries.
//
// It returns an error wrapping name.ErrCityTableMismatch when the index was
// built against a different table. An embedding index stays usable on its
// own copy in that case, but it is still reported, because the pair of index
// files disagrees and the caller rebuilds.
func attachNameIndex(nameIndexPath string, nameFinder *name.Finder, cities []city.City) error {
	embedded := nameFinder.OwnsCityTable()
	if err := nameFinder.ShareCities(cities); err != nil {
		return fmt.Errorf("name index %s does not match the S2 index: %w", nameIndexPath, err)
	}
	if embedded {
		if err := nameFinder.SerializeIndex(nameIndexPath); err != nil {
			log.Printf("Warning: migrating name index %s to the shared-table format failed (will retry next boot): %v", nameIndexPath, err)
		} else {
			log.Printf("migrated name index %s to the shared-table format", nameIndexPath)
		}
	}
	return nil
}

func buildNameIndex(nameIndexPath string, data *datasetSource, cities []city.City) (*name.Finder, pendingWrite, error) {
	log.Printf("Building name index at %s", nameIndexPath)
	nameFinder := name.BuildIndex(data.cities)
	// Both indexes are built from the same rows, so the tables are identical
	// and sharing cannot fail; if it ever did, the index keeps (and
	// serializes) its own copy — larger, never wrong. Sharing happens before
	// the write: the compact format serializes only references to the table.
	if err := nameFinder.ShareCities(cities); err != nil {
		log.Printf("Warning: name index keeps its own city table: %v", err)
	}
	return nameFinder, func() error {
		if err := nameFinder.SerializeIndex(nameIndexPath); err != nil {
			return fmt.Errorf("failed to serialize name index: %v", err)
		}
		return nil
	}, nil
}

// ensurePostalCodeIndex returns the postal code index, building it when the
// file is missing and returning the write that serializes it. A file that
// exists but fails to decode is logged and rebuilt once from the source
// data, the write replacing the bad file; a rebuild that also fails is
// fatal.
func ensurePostalCodeIndex(ctx context.Context, postalCodeIndexPath string, data *datasetSource) (*postalCode.Finder, pendingWrite, error) {
	log.Printf("Ensuring postal code index is built and serialized in %s", postalCodeIndexPath)
	if _, errStat := os.Stat(postalCodeIndexPath); os.IsNotExist(errStat) {
		if err := data.load(ctx); err != nil {
			return nil, nil, fmt.Errorf("failed to load datasets to build postal code index: %v", err)
		}
		return buildPostalCodeIndex(postalCodeIndexPath, data)
	}

	postalCodeFinder, err := postalCode.DeserializeIndex(postalCodeIndexPath)
	if err != nil {
		if !errors.Is(err, postalCode.ErrCorruptIndex) {
			return nil, nil, fmt.Errorf("failed to deserialize postal code index: %v", err)
		}
		log.Printf("Warning: %v", err)
		if err := data.load(ctx); err != nil {
			return nil, nil, fmt.Errorf("failed to load datasets needed to rebuild the postal code index: %v", err)
		}
		return buildPostalCodeIndex(postalCodeIndexPath, data)
	}
	migratePostalIndex(postalCodeIndexPath, postalCodeFinder)
	return postalCodeFinder, nil, nil
}

// migratePostalIndex rewrites a postal index loaded from a legacy file in
// the current format (a one-time, in-place conversion; the loaded finder is
// already in the compact layout). A failed write is logged and ignored: the
// next boot retries.
func migratePostalIndex(path string, f *postalCode.Finder) {
	if !f.LegacyFormat() {
		return
	}
	if err := f.SerializeIndex(path); err != nil {
		log.Printf("Warning: migrating postal code index %s to the current format failed (will retry next boot): %v", path, err)
		return
	}
	log.Printf("migrated postal code index %s to the current format", path)
}

func buildPostalCodeIndex(postalCodeIndexPath string, data *datasetSource) (*postalCode.Finder, pendingWrite, error) {
	log.Printf("Building postal code index at %s", postalCodeIndexPath)
	postalCodeFinder := postalCode.BuildIndex(data.postalCodes)
	return postalCodeFinder, func() error {
		if err := postalCodeFinder.SerializeIndex(postalCodeIndexPath); err != nil {
			return fmt.Errorf("failed to serialize postal code index: %v", err)
		}
		return nil
	}, nil
}
