package coordinates

import (
	"encoding/gob"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/cheggaaa/pb/v3"
	"github.com/golang/geo/s2"
)

const earthRadiusKm = 6371.0

// Rank selects how NearestPlace chooses the winning city from the candidate
// pool around the query point.
type Rank int

const (
	// RankDistance returns the closest candidate. It is the zero value so
	// the historical nearest-neighbor behavior remains the default.
	RankDistance Rank = iota
	// RankPopulation returns the candidate with the highest gravity-model
	// score (see NearestPlace for the formula).
	RankPopulation
)

// rankCandidatePool is how many nearest candidates the population ranking
// fetches and scores. The gravity model decays with squared distance, so the
// weighted winner always comes from the query point's immediate neighborhood
// — 16 keeps the pool comfortably larger than any realistic winner
// neighborhood. It is a structural constant for decay-based ranking, not a
// tuning knob. (In this golang/geo version a multi-result query cannot prune
// the search — see NearestPlace — so the pool size does not change the cost
// of a population-ranked query at prod scale; it only defines which
// candidates can win.)
const rankCandidatePool = 16

// S2Finder uses a ShapeIndex for efficient nearest neighbor searches.
type S2Finder struct {
	Index  *s2.ShapeIndex
	Cities []city.City
}

// SerializableS2Finder is a helper struct for gob encoding/decoding.
type SerializableS2Finder struct {
	Cities []city.City
}

// Serialized index file format (version 2):
//
//	gob(indexHeader{Magic: "CFS2IDX", Version: 2, Count: len(Cities)})
//	gob(SerializableS2Finder)
//
// The leading header lets a truncated or version-skewed file be rejected
// with a descriptive error instead of silently poisoning the finder with
// zero-filled data (gob zero-fills fields it does not find, so an index
// written before a City-struct change would otherwise load as garbage). The
// payload struct is unchanged from v1, but v2 bumped the version in lockstep
// with the name index because the embedded City gained Population: a v1 file
// decoded into the new struct would load every population as 0 — silently
// wrong data for the WEIGHTED ranking path. The bump forces one coordinated
// regenerate so no v1 S2 file can be interpreted with zero-filled
// populations. Writes go to filepath+".part" and are renamed into place only
// after a complete encode, so a crash mid-write never replaces a valid index
// with a truncated one.
const (
	indexMagic   = "CFS2IDX"
	indexVersion = uint32(2)
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

// NearestPlace finds the city nearest to the given latitude and longitude,
// ordered by the requested ranking mode.
//
// rank == RankDistance (the default, and the behavior of every pre-ranking
// release): issues the historical MaxResults(1) query verbatim and returns
// its single closest result, so both the returned city and the latency
// profile are exactly the pre-change path's. (A shared rankCandidatePool
// fetch whose results[0] is provably the same city was tried first — the
// equivalence is real and test-pinned — but this golang/geo version never
// tightens the search limit for maxResults > 1, turning every multi-result
// query into a full 13.47M-edge scan (~10 s per query at prod scale,
// measured); see the PR summary.)
//
// rank == RankPopulation: fetches the rankCandidatePool closest candidates
// and ranks them with a gravity model,
//
//	score = population / (d*d + 1)
//
// where d is the great-circle distance in kilometers on the same sphere the
// reported distance uses. The +1 keeps the denominator positive and makes a
// query issued at a city's exact coordinates score that city at exactly its
// population. Ties on score are broken by the smaller distance, then stably
// by candidate index: candidates arrive sorted by distance, so scanning in
// order and keeping a strictly greater score yields both tie-breaks for free.
// Note the same library caveat: the pool fetch is a full index scan at prod
// scale in this golang/geo version, so weighted queries are ~seconds each;
// correctness is unaffected (results are sorted, then truncated to the pool).
func (f *S2Finder) NearestPlace(lat, lon float64, rank Rank) (*city.City, float64, error) {
	if f.Index == nil {
		return nil, 0, fmt.Errorf("s2 index is not initialized")
	}
	targetPoint := s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lon))
	// Pruning the query (vs the MaxInt32 default) avoids collecting and
	// sorting a result for every indexed point on each query — but the
	// pruning only bites at maxResults == 1 in this golang/geo version, so
	// the distance path asks for exactly one result and only the population
	// path pays for the candidate pool.
	poolSize := 1
	if rank == RankPopulation {
		poolSize = rankCandidatePool
	}
	query := s2.NewClosestEdgeQuery(f.Index, s2.NewClosestEdgeQueryOptions().MaxResults(poolSize))
	target := s2.NewMinDistanceToPointTarget(targetPoint)
	results := query.FindEdges(target)

	if len(results) == 0 {
		return nil, 0, fmt.Errorf("no city found")
	}

	var winner s2.EdgeQueryResult
	switch rank {
	case RankDistance:
		winner = results[0]
	case RankPopulation:
		winner = f.bestPopulationRank(results)
	default:
		return nil, 0, fmt.Errorf("invalid rank %d", int(rank))
	}
	cityIndex := winner.EdgeID()
	if int(cityIndex) >= len(f.Cities) {
		return nil, 0, fmt.Errorf("invalid city index %d found (total cities: %d)", cityIndex, len(f.Cities))
	}
	nearestCity := f.Cities[cityIndex]

	distanceKm := winner.Distance().Angle().Radians() * earthRadiusKm

	return &nearestCity, distanceKm, nil
}

// bestPopulationRank returns the result with the highest gravity-model score
// population / (d*d + 1). See NearestPlace for the formula and tie-breaks;
// the strictly-greater comparison resolves equal scores to the earlier
// candidate, which — because results arrive sorted by distance — is the
// nearer one, and at equal distance the lower candidate index.
func (f *S2Finder) bestPopulationRank(results []s2.EdgeQueryResult) s2.EdgeQueryResult {
	best := results[0]
	bestScore := f.populationRankScore(best)
	for _, candidate := range results[1:] {
		if score := f.populationRankScore(candidate); score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best
}

// populationRankScore scores one query result under the gravity model. An
// out-of-range edge index cannot occur with a consistently built finder (the
// point vector and the city slice come from the same source); scoring it as
// -Inf keeps a corrupt index from panicking before NearestPlace's own bounds
// check can report it.
func (f *S2Finder) populationRankScore(result s2.EdgeQueryResult) float64 {
	cityIndex := int(result.EdgeID())
	if cityIndex < 0 || cityIndex >= len(f.Cities) {
		return math.Inf(-1)
	}
	distanceKm := result.Distance().Angle().Radians() * earthRadiusKm
	return float64(f.Cities[cityIndex].Population) / (distanceKm*distanceKm + 1.0)
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
