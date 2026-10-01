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
	"github.com/golang/geo/s1"
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

// populationRankInitialRadiusKm is the radius of the first population-ranked
// query disc. Calibrated so a query in or near any populated place usually
// terminates on the first or second iteration (measured: metro queries
// resolve within 10 km; see the PR summary for the per-class table). It is a
// starting point for the escalation loop, not a cap on the answer.
const populationRankInitialRadiusKm = 10.0

// populationRankRadiusGrowth multiplies the search radius each escalation
// step of the population ranking. Five grows 10 km to global coverage
// (half circumference) in six steps, so even a mid-ocean query pays at most
// six radius-pruned scans before the final unbounded iteration.
const populationRankRadiusGrowth = 5.0

// maxSearchRadiusKm is the great-circle half-circumference: the largest
// distance any two points on the sphere can be apart.
const maxSearchRadiusKm = math.Pi * earthRadiusKm

// S2Finder uses a ShapeIndex for efficient nearest neighbor searches.
type S2Finder struct {
	Index  *s2.ShapeIndex
	Cities []city.City

	// maxPopulation is the largest Population across Cities (0 when no city
	// carries population data). It bounds the gravity score of every city
	// outside a search radius, which is what lets population-ranked queries
	// stop escalating. Derived from Cities — never serialized; both
	// constructors recompute it in one pass, so the on-disk format is
	// unchanged.
	maxPopulation int32
}

// maxPopulationOf returns the largest Population in cities (0 when empty or
// all population-less).
func maxPopulationOf(cities []city.City) int32 {
	var max int32
	for i := range cities {
		if cities[i].Population > max {
			max = cities[i].Population
		}
	}
	return max
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

	return &S2Finder{Index: index, Cities: cityData, maxPopulation: maxPopulationOf(cityData)}, nil
}

// NearestPlace finds the city nearest to the given latitude and longitude,
// ordered by the requested ranking mode.
//
// rank == RankDistance (the default, and the behavior of every pre-ranking
// release): issues the historical MaxResults(1) query verbatim and returns
// its single closest result, so both the returned city and the latency
// profile are exactly the pre-change path's. (A shared multi-result pool
// fetch whose results[0] is provably the same city was tried first — the
// equivalence is real and test-pinned — but this golang/geo version never
// tightens the search limit for maxResults > 1, turning every multi-result
// query without a distance limit into a full 13.47M-edge scan (~10 s per
// query at prod scale, measured); see the PR summary.)
//
// rank == RankPopulation: ranks cities by a gravity model,
//
//	score = population / (d*d + 1)
//
// where d is the great-circle distance in kilometers on the same sphere the
// reported distance uses. The +1 keeps the denominator positive and makes a
// query issued at a city's exact coordinates score that city at exactly its
// population. The winner is EXACT — the highest-scoring city over the whole
// dataset, with no candidate-pool truncation — found with an escalating
// radius under an anytime bound; see nearestByPopulation. Ties on score are
// broken by the smaller distance, then stably by candidate index: each
// radius query returns its results sorted by distance, so scanning in order
// and keeping a strictly greater score yields both tie-breaks for free.
func (f *S2Finder) NearestPlace(lat, lon float64, rank Rank) (*city.City, float64, error) {
	if f.Index == nil {
		return nil, 0, fmt.Errorf("s2 index is not initialized")
	}
	targetPoint := s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lon))

	var winner s2.EdgeQueryResult
	switch rank {
	case RankDistance:
		// MaxResults(1) prunes the search: the default (MaxInt32) would
		// collect and sort a result for every indexed point on each query.
		query := s2.NewClosestEdgeQuery(f.Index, s2.NewClosestEdgeQueryOptions().MaxResults(1))
		results := query.FindEdges(s2.NewMinDistanceToPointTarget(targetPoint))
		if len(results) == 0 {
			return nil, 0, fmt.Errorf("no city found")
		}
		winner = results[0]
	case RankPopulation:
		result, err := f.nearestByPopulation(targetPoint)
		if err != nil {
			return nil, 0, err
		}
		winner = result
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

// kmToChordAngle converts a great-circle kilometer radius on the finder's
// sphere into the ChordAngle distance the s2 query API uses.
func kmToChordAngle(km float64) s1.ChordAngle {
	return s1.ChordAngleFromAngle(s1.Angle(km / earthRadiusKm))
}

// nearestByPopulation returns the highest gravity-scored query result over
// ALL cities — exact, no truncation — using a radius that escalates until an
// anytime bound proves no city outside it can win:
//
//  1. Query every city within radius R of the target. DistanceLimit makes
//     the traversal visit only nearby index cells (the priority queue pops
//     cells in increasing distance and stops past the limit), so the cost
//     scales with the populated area inside the disc, not the index size.
//     The limit is passed as chord.Successor() because the option's
//     semantics are exclusive ("edges whose distance is equal are not
//     returned"); the successor makes each disc inclusive of its rim.
//  2. Track the best gravity score s* among the returned candidates.
//  3. Any city beyond R scores at most maxPopulation/(R*R + 1) — population
//     is a non-negative int32, so that fraction bounds every excluded city.
//     If s* already exceeds the bound, the in-radius winner is the global
//     winner and the search stops.
//  4. Otherwise R grows by populationRankRadiusGrowth. The final iteration
//     drops the distance limit entirely (an exclusive limit at exactly the
//     half-circumference could drop an antipodal city, and Successor() at
//     the straight angle degenerates), covering the whole sphere — so the
//     loop always terminates with the exact answer. That unbounded
//     iteration is the full-scan worst case (~seconds at prod scale,
//     measured); in practice it is reached only from mid-ocean points far
//     from any populated place.
//
// With no population data anywhere (maxPopulation == 0), every score is 0
// and the gravity winner is simply the nearest city.
func (f *S2Finder) nearestByPopulation(targetPoint s2.Point) (s2.EdgeQueryResult, error) {
	queryAll := func(radiusKm float64, limited bool) []s2.EdgeQueryResult {
		options := s2.NewClosestEdgeQueryOptions()
		if limited {
			options = options.DistanceLimit(kmToChordAngle(radiusKm).Successor())
		}
		query := s2.NewClosestEdgeQuery(f.Index, options)
		return query.FindEdges(s2.NewMinDistanceToPointTarget(targetPoint))
	}

	var none s2.EdgeQueryResult
	if f.maxPopulation == 0 {
		// Degenerate case: with no population data anywhere the anytime
		// bound can never certify a winner, so every rank=population query
		// pays the unbounded full-scan iteration (the loop's documented
		// worst case) — the nearest city is still returned correctly.
		results := queryAll(0, false)
		if len(results) == 0 {
			return none, fmt.Errorf("no city found")
		}
		return results[0], nil
	}

	radiusKm := populationRankInitialRadiusKm
	for {
		results := queryAll(radiusKm, true)
		if len(results) > 0 {
			best := f.bestPopulationRank(results)
			// Anytime bound: every city at distance >= radiusKm scores at
			// most maxPopulation / (radiusKm^2 + 1); a strictly better
			// in-radius winner cannot be beaten (or tied) from outside.
			bound := float64(f.maxPopulation) / (radiusKm*radiusKm + 1.0)
			if f.populationRankScore(best) > bound {
				return best, nil
			}
		}
		next := radiusKm * populationRankRadiusGrowth
		if next < maxSearchRadiusKm {
			radiusKm = next
			continue
		}
		// Final iteration: no distance limit — the whole sphere, exact.
		results = queryAll(0, false)
		if len(results) == 0 {
			return none, fmt.Errorf("no city found")
		}
		return f.bestPopulationRank(results), nil
	}
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
		Index:         index,
		Cities:        serializable.Cities,
		maxPopulation: maxPopulationOf(serializable.Cities),
	}, nil
}
