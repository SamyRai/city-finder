package coordinates

import (
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/cheggaaa/pb/v3"
	"github.com/golang/geo/s2"
)

const earthRadiusKm = 6371.0

// S2Finder uses a ShapeIndex for efficient nearest neighbor searches.
type S2Finder struct {
	Index  *s2.ShapeIndex
	Cities []city.City
}

// SerializableS2Finder is a helper struct for gob encoding/decoding.
type SerializableS2Finder struct {
	Cities []city.City
}

// Serialized index file format (version 1):
//
//	gob(indexHeader{Magic: "CFS2IDX", Version: 1, Count: len(Cities)})
//	gob(SerializableS2Finder)
//
// The leading header lets a truncated or version-skewed file be rejected
// with a descriptive error instead of silently poisoning the finder with
// zero-filled data (gob zero-fills fields it does not find, so an index
// written before a City-struct change would otherwise load as garbage).
// Writes go to filepath+".part" and are renamed into place only after a
// complete encode, so a crash mid-write never replaces a valid index with a
// truncated one.
const (
	indexMagic   = "CFS2IDX"
	indexVersion = uint32(1)
)

// indexHeader is the first gob value of every serialized S2 index.
type indexHeader struct {
	Magic   string
	Version uint32
	Count   int
}

// ErrCorruptIndex reports an index file that cannot be trusted: truncated,
// undecodable, or written by an incompatible format version. Detect it with
// errors.Is to decide whether a rebuild from source data is possible.
var ErrCorruptIndex = errors.New("s2 index file is corrupt or incompatible")

// decodeIndexFile decodes and validates the header+payload stream of an S2
// index. Any failure means the file must not be used.
func decodeIndexFile(decoder *gob.Decoder, header *indexHeader, payload *SerializableS2Finder) error {
	if err := decoder.Decode(header); err != nil {
		return err
	}
	if header.Magic != indexMagic {
		return fmt.Errorf("bad magic %q (want %q)", header.Magic, indexMagic)
	}
	if header.Version != indexVersion {
		return fmt.Errorf("unsupported version %d (want %d)", header.Version, indexVersion)
	}
	if err := decoder.Decode(payload); err != nil {
		return err
	}
	if header.Count != len(payload.Cities) {
		return fmt.Errorf("payload holds %d cities but the header recorded %d", len(payload.Cities), header.Count)
	}
	return nil
}

// BuildIndex creates an S2 spatial index from raw city data.
func BuildIndex(cities []city.SpatialCity, config *config.S2) (*S2Finder, error) {
	points := make(s2.PointVector, len(cities))
	cityData := make([]city.City, len(cities))

	// Use progress bar with infrequent updates to reduce overhead
	bar := pb.Full.Start(len(cities))
	bar.SetRefreshRate(time.Second) // Update every second instead of every item

	// Process cities in batches to minimize progress bar overhead
	batchSize := 100000 // Update progress every 100k items
	for i, spatialCity := range cities {
		points[i] = s2.PointFromLatLng(s2.LatLngFromDegrees(spatialCity.Latitude, spatialCity.Longitude))
		cityData[i] = spatialCity.City

		// Only update progress bar every batchSize items to reduce overhead
		if (i+1)%batchSize == 0 || i == len(cities)-1 {
			bar.SetCurrent(int64(i + 1))
		}
	}
	bar.Finish()

	index := s2.NewShapeIndex()
	index.Add(&points)
	// Build eagerly: without this the full index construction (which the s2
	// library warns can transiently use up to ~20x the index memory) would
	// happen inside the first query instead.
	index.Build()

	return &S2Finder{Index: index, Cities: cityData}, nil
}

// NearestPlace finds the nearest city to the given latitude and longitude.
func (f *S2Finder) NearestPlace(lat, lon float64) (*city.City, float64, error) {
	if f.Index == nil {
		return nil, 0, fmt.Errorf("s2 index is not initialized")
	}
	targetPoint := s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lon))
	// MaxResults(1) prunes the search: the default (MaxInt32) would collect
	// and sort a result for every indexed point on each query.
	query := s2.NewClosestEdgeQuery(f.Index, s2.NewClosestEdgeQueryOptions().MaxResults(1))
	target := s2.NewMinDistanceToPointTarget(targetPoint)
	results := query.FindEdges(target)

	if len(results) == 0 {
		return nil, 0, fmt.Errorf("no city found")
	}

	closest := results[0]
	cityIndex := closest.EdgeID()
	if int(cityIndex) >= len(f.Cities) {
		return nil, 0, fmt.Errorf("invalid city index %d found (total cities: %d)", cityIndex, len(f.Cities))
	}
	nearestCity := f.Cities[cityIndex]

	distanceKm := closest.Distance().Angle().Radians() * earthRadiusKm

	return &nearestCity, distanceKm, nil
}

// SerializeIndex saves the finder's data to a file using gob. The stream is
// header-then-payload (see indexHeader) and is written atomically: the bytes
// land in filepath+".part" first and are renamed over filepath only after a
// complete encode, so readers never observe a half-written index.
func (f *S2Finder) SerializeIndex(filepath string) error {
	partPath := filepath + ".part"
	file, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("failed to create index file: %w", err)
	}

	encoder := gob.NewEncoder(file)
	encodeErr := encoder.Encode(indexHeader{Magic: indexMagic, Version: indexVersion, Count: len(f.Cities)})
	if encodeErr == nil {
		encodeErr = encoder.Encode(SerializableS2Finder{Cities: f.Cities})
	}
	closeErr := file.Close()

	if encodeErr != nil {
		_ = os.Remove(partPath) // never leave a partial index behind
		return fmt.Errorf("failed to encode s2 index: %w", encodeErr)
	}
	if closeErr != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to close index file: %w", closeErr)
	}
	if err := os.Rename(partPath, filepath); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to move %s to %s: %w", partPath, filepath, err)
	}
	return nil
}

// DeserializeIndex loads the finder's data from a file.
func DeserializeIndex(filepath string) (*S2Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}

	var header indexHeader
	var serializable SerializableS2Finder
	decodeErr := decodeIndexFile(gob.NewDecoder(file), &header, &serializable)
	closeErr := file.Close()

	if decodeErr != nil {
		return nil, fmt.Errorf("%w: index file %s appears truncated or from an incompatible version; delete %s or rebuild the index: %v",
			ErrCorruptIndex, filepath, filepath, decodeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("error closing file: %w", closeErr)
	}

	// Rebuild index efficiently without progress bar overhead
	points := make(s2.PointVector, len(serializable.Cities))
	for i, c := range serializable.Cities {
		points[i] = s2.PointFromLatLng(s2.LatLngFromDegrees(c.Latitude, c.Longitude))
	}

	index := s2.NewShapeIndex()
	index.Add(&points)
	// Build eagerly so the first query does not pay the construction cost.
	index.Build()

	return &S2Finder{
		Index:  index,
		Cities: serializable.Cities,
	}, nil
}
