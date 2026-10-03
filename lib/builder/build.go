package builder

import (
	"context"
	"fmt"
	"log"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

// Write serializes an index that was just built. The Build functions return
// one instead of writing, so the caller can run the writes concurrently once
// every index exists in memory. A nil Write means there is nothing to write
// (an index that was loaded from its file).
type Write func() error

// WriteAll runs the non-nil writes concurrently and returns the first
// failure after all of them have returned. They only read the finders
// (the serializers take their own read locks), so they cannot race each
// other; the builds stay sequential because three at once would add their
// transient peaks.
func WriteAll(ctx context.Context, writes ...Write) error {
	g := newGroup(ctx)
	for _, write := range writes {
		if write == nil {
			continue
		}
		g.Go(func(context.Context) error { return write() })
	}
	return g.Wait()
}

// BuildS2 builds the S2 index from the loaded rows and returns the write that
// serializes it to path.
func BuildS2(path string, cities []city.SpatialCity) (*coordinates.S2Finder, Write, error) {
	log.Printf("Building S2 index at %s", path)
	s2Finder, err := coordinates.BuildIndex(cities)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build S2 index: %v", err)
	}
	return s2Finder, func() error {
		if err := s2Finder.SerializeIndex(path); err != nil {
			return fmt.Errorf("failed to serialize S2 index: %v", err)
		}
		return nil
	}, nil
}

// BuildName builds the name index from the loaded rows and attaches it to
// the S2 index's city table (s2Cities), so the file references the table
// instead of embedding a second copy and the server holds every city once.
// Call it after BuildS2 (or after loading the S2 index) on the same rows.
//
// ShareCities policy, the same for every caller: both indexes are built from
// the same rows, so the tables are identical and sharing cannot fail. If it
// ever did, the index keeps (and serializes) its own copy and a warning is
// logged: larger, never wrong, and not worth failing a boot or a build over.
// Sharing happens before the write, because the compact format serializes
// only references to the table. (Attaching a name index loaded from a file
// is a different case: a mismatch there means the two files disagree, so the
// initializer rebuilds; see initializer.attachNameIndex.)
func BuildName(path string, cities []city.SpatialCity, s2Cities []city.City) (*name.Finder, Write, error) {
	log.Printf("Building name index at %s", path)
	nameFinder := name.BuildIndex(cities)
	if err := nameFinder.ShareCities(s2Cities); err != nil {
		log.Printf("Warning: name index keeps its own city table: %v", err)
	}
	return nameFinder, func() error {
		if err := nameFinder.SerializeIndex(path); err != nil {
			return fmt.Errorf("failed to serialize name index: %v", err)
		}
		return nil
	}, nil
}

// BuildPostal builds the postal code index and returns the write that
// serializes it to path.
func BuildPostal(path string, postalCodes PostalCodes) (*postalCode.Finder, Write, error) {
	log.Printf("Building postal code index at %s", path)
	postalCodeFinder := postalCode.BuildIndex(postalCodes)
	return postalCodeFinder, func() error {
		if err := postalCodeFinder.SerializeIndex(path); err != nil {
			return fmt.Errorf("failed to serialize postal code index: %v", err)
		}
		return nil
	}, nil
}
