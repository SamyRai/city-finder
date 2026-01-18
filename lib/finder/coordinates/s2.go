package coordinates

import (
	"encoding/gob"
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

// NewS2Finder creates a new S2Finder instance by deserializing from a file.
func NewS2Finder(cfgS2 *config.S2) (*S2Finder, error) {
	return DeserializeIndex(cfgS2.IndexFile)
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

	return &S2Finder{Index: index, Cities: cityData}, nil
}

// NearestPlace finds the nearest city to the given latitude and longitude.
func (f *S2Finder) NearestPlace(lat, lon float64) (*city.City, float64, error) {
	if f.Index == nil {
		return nil, 0, fmt.Errorf("s2 index is not initialized")
	}
	targetPoint := s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lon))
	query := s2.NewClosestEdgeQuery(f.Index, s2.NewClosestEdgeQueryOptions())
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

// SerializeIndex saves the finder's data to a file using gob.
func (f *S2Finder) SerializeIndex(filepath string) error {
	serializable := SerializableS2Finder{
		Cities: f.Cities,
	}

	file, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create index file: %w", err)
	}

	encoder := gob.NewEncoder(file)
	encodeErr := encoder.Encode(serializable)
	closeErr := file.Close()

	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

// DeserializeIndex loads the finder's data from a file.
func DeserializeIndex(filepath string) (*S2Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}

	var serializable SerializableS2Finder
	decoder := gob.NewDecoder(file)
	decodeErr := decoder.Decode(&serializable)
	closeErr := file.Close()

	if decodeErr != nil {
		return nil, fmt.Errorf("error decoding file: %w", decodeErr)
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

	return &S2Finder{
		Index:  index,
		Cities: serializable.Cities,
	}, nil
}