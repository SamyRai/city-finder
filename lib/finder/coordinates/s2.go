package coordinates

import (
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/SamyRai/cityFinder/lib/city"
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

// S2Finder uses a ShapeIndex for efficient nearest neighbor searches.
type S2Finder struct {
	Index  *s2.ShapeIndex
	Cities []city.City

	// Admin-region attribution (v1.1 design note): per-city ids into the code
	// tables below, -1 = the city carries no code. Built from the loader's
	// build-only SpatialCity.AdminXCode fields and serialized with the index;
	// lazily consumed by NearestPlaceWithAdmin, so the default distance/
	// population paths pay nothing. Heap cost at prod: 2 x 13.47M x 4 B
	// ~= 108 MB + tiny tables.
	Admin1IDs   []int32
	Admin2IDs   []int32
	Admin1Codes []string // "US.CA" style composite keys (country.code)
	Admin2Codes []string

	// Admin1Names maps a composite Admin1Codes key to its display name from
	// the OPTIONAL admin1CodesASCII.txt dataset. nil when that file is absent
	// (codes-only mode). Never serialized: it is a ~120 KB side dataset the
	// initializer (re)attaches on every boot, warm or cold, through
	// AttachAdmin1Names. The field stays exported only because server tests
	// build finders without the initializer; production code writes it once.
	Admin1Names map[string]string

	// maxPopulation is the largest Population across Cities (0 when no city
	// carries population data). It detects the all-zero degenerate case of
	// population-ranked queries. Derived from Cities — never serialized; both
	// constructors recompute it in one pass, so the on-disk format is
	// unchanged.
	maxPopulation int32

	// topPopulations is the topPopulationK-table behind the anytime bound of
	// population-ranked queries: the most populous cities with Population > 0,
	// sorted by population descending (ties broken by city index ascending, so
	// the table is deterministic). populationOutsideBound scores these cities
	// exactly outside a search radius and bounds everyone else by the K-th
	// largest population. Derived from Cities in memory — never serialized,
	// never mutated after construction — so the on-disk format is unchanged.
	// Empty when no city carries population data.
	topPopulations []topPopulationEntry

	// distanceQueryPool recycles the RankDistance query objects (see
	// pooledDistanceQuery). A sync.Pool gives each concurrent goroutine its
	// own instance, so NearestPlace stays safe under the concurrent load the
	// finder serves. Never serialized. S2Finder must stay pointer-only (the
	// pool makes it nocopy; both constructors already return pointers).
	distanceQueryPool sync.Pool

	// populationQueryPool recycles the unbounded-result query of the
	// population path (nearestByPopulation), which runs several discs per
	// request; each disc sets its own distance limit on the pooled options.
	// Same reuse-safety reading as distanceQueryPool.
	populationQueryPool sync.Pool
}

// pooledDistanceQuery bundles the per-finder-reusable objects of the
// RankDistance path: the options carrying MaxResults(1) and the EdgeQuery
// built from them.
//
// Reuse safety (verified against the pinned golang/geo source,
// v0.0.0-20260526120156): EdgeQuery.FindEdges re-initializes every per-call
// field on each invocation — findEdgesInternal assigns fresh testedEdges and
// results slices, recomputes distanceLimit/avoidDuplicates/useConservative-
// CellDistance, and the optimized traversal's priority queue is always empty
// or explicitly reset when FindEdges returns. What persists across calls is
// exactly the same-index cache you WANT to persist: the precomputed index
// covering (indexCovering/indexCells), the ShapeIndexIterator over this
// finder's immutable index, and the edge count — which is why a reused query
// is not just safe but faster than a fresh one. The public Reset() method
// exists for switching indexes/options and would only discard that cache; it
// is deliberately NOT called. The MinDistanceToPointTarget cannot be pooled
// (its point field is unexported) and is allocated per call.
type pooledDistanceQuery struct {
	options *s2.EdgeQueryOptions
	query   *s2.EdgeQuery
}

// distanceQuery acquires a RankDistance query bundle from the pool (or builds
// one bound to this finder's index on first use).
func (f *S2Finder) distanceQuery() *pooledDistanceQuery {
	if pq, ok := f.distanceQueryPool.Get().(*pooledDistanceQuery); ok {
		return pq
	}
	options := s2.NewClosestEdgeQueryOptions().MaxResults(1)
	return &pooledDistanceQuery{
		options: options,
		query:   s2.NewClosestEdgeQuery(f.Index, options),
	}
}

// populationQuery acquires a population-path query bundle from the pool (or
// builds one bound to this finder's index on first use). The options carry
// no MaxResults: a disc must return every city inside it.
func (f *S2Finder) populationQuery() *pooledDistanceQuery {
	if pq, ok := f.populationQueryPool.Get().(*pooledDistanceQuery); ok {
		return pq
	}
	options := s2.NewClosestEdgeQueryOptions()
	return &pooledDistanceQuery{
		options: options,
		query:   s2.NewClosestEdgeQuery(f.Index, options),
	}
}

// adminCodeID interns one composite admin code ("CC.CODE") into the code
// table, returning its id; an empty per-country code means the row carries
// none and records -1. First-encounter order keeps the tables tiny and the
// encoding delta-friendly (~4K distinct admin1 pairs worldwide; admin2 is
// larger but still five orders below the city count).
func adminCodeID(country, code string, table map[string]int32, codes *[]string) int32 {
	if code == "" {
		return -1
	}
	key := country + "." + code
	if id, exists := table[key]; exists {
		return id
	}
	id := int32(len(*codes))
	table[key] = id
	*codes = append(*codes, key)
	return id
}

// commaFormat formats a non-negative count with thousands separators
// ("4,000,000"), the console progress format of BuildIndex.
func commaFormat(n int) string {
	digits := strconv.Itoa(n)
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	for i := 0; i < len(digits); i++ {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(digits[i])
	}
	return b.String()
}

// BuildIndex creates an S2 spatial index from raw city data. The index is
// always built with golang/geo's ShapeIndex defaults; there is nothing to tune.
func BuildIndex(cities []city.SpatialCity) (*S2Finder, error) {
	points := make(s2.PointVector, len(cities))
	cityData := make([]city.City, len(cities))
	admin1IDs := make([]int32, len(cities))
	admin2IDs := make([]int32, len(cities))
	admin1Codes, admin2Codes := []string{}, []string{}
	admin1Index := make(map[string]int32, 8192)
	admin2Index := make(map[string]int32, 8192)

	// Progress reporting is a deterministic log line every ~2M cities plus a
	// completion line (replacing the former pb progress bar): server consoles
	// get readable milestones with no TTY control characters, and the only
	// per-iteration cost is the modulo check. At prod scale (13.47M cities)
	// this prints six milestone lines and one completion line.
	const progressInterval = 2_000_000
	for i, spatialCity := range cities {
		points[i] = s2.PointFromLatLng(s2.LatLngFromDegrees(spatialCity.Latitude, spatialCity.Longitude))
		cityData[i] = spatialCity.City
		admin1IDs[i] = adminCodeID(spatialCity.Country, spatialCity.Admin1Code, admin1Index, &admin1Codes)
		admin2IDs[i] = adminCodeID(spatialCity.Country, spatialCity.Admin2Code, admin2Index, &admin2Codes)

		if done := i + 1; done%progressInterval == 0 && done < len(cities) {
			log.Printf("s2 index: %s / %s cities", commaFormat(done), commaFormat(len(cities)))
		}
	}
	log.Printf("s2 index: %s / %s cities", commaFormat(len(cities)), commaFormat(len(cities)))

	index := s2.NewShapeIndex()
	index.Add(&points)
	// Build eagerly: without this the full index construction (which the s2
	// library warns can transiently use up to ~20x the index memory) would
	// happen inside the first query instead.
	index.Build()

	return &S2Finder{
		Index:          index,
		Cities:         cityData,
		Admin1IDs:      admin1IDs,
		Admin2IDs:      admin2IDs,
		Admin1Codes:    admin1Codes,
		Admin2Codes:    admin2Codes,
		maxPopulation:  maxPopulationOf(cityData),
		topPopulations: topPopulationsOf(cityData),
	}, nil
}

// NearestPlace finds the city nearest to the given latitude and longitude,
// ordered by the requested ranking mode.
//
// rank == RankDistance (the default, and the behavior of every pre-ranking
// release): issues the historical MaxResults(1) query and returns its single
// closest result, so the returned city is exactly the pre-change path's; the
// query OBJECT is now recycled through a per-finder pool (identical options,
// same index — see pooledDistanceQuery), which only removes per-call
// allocations. TestNearestPlaceDistanceRankMatchesMaxResultsOne pins the
// equivalence against the verbatim unpooled query. (A shared multi-result
// pool fetch whose results[0] is provably the same city was tried first — the
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
	nearestCity, _, distanceKm, err := f.nearest(lat, lon, rank)
	return nearestCity, distanceKm, err
}

// NearestPlaceWithAdmin is NearestPlace plus the winning city's
// administrative attribution (see AdminAttribution): the pragmatic
// "reverse geocoding" of the nearest-city winner — NOT polygon containment.
// Near boundaries the attribution follows the nearest city, which may sit
// across the line; the API documents this accuracy boundary. The
// attribution read is two int32 slice loads plus at most two tiny table
// lookups per query.
func (f *S2Finder) NearestPlaceWithAdmin(lat, lon float64, rank Rank) (*city.City, float64, AdminAttribution, error) {
	nearestCity, cityIndex, distanceKm, err := f.nearest(lat, lon, rank)
	if err != nil {
		return nil, 0, AdminAttribution{}, err
	}
	return nearestCity, distanceKm, f.adminOf(cityIndex), nil
}

// ErrInvalidCoordinate reports a query point that is not a place on the
// sphere: a NaN or infinite coordinate, or a latitude outside [-90, 90].
var ErrInvalidCoordinate = errors.New("invalid coordinate")

// nearest is the shared query core of NearestPlace and
// NearestPlaceWithAdmin: it also returns the winning city's index into
// Cities, which the attribution lookup needs and the plain callers discard.
func (f *S2Finder) nearest(lat, lon float64, rank Rank) (*city.City, int, float64, error) {
	if f.Index == nil {
		return nil, 0, 0, fmt.Errorf("s2 index is not initialized")
	}
	// A non-finite point compares false against every bound, so a
	// population query would escalate to the full-sphere scan (seconds at
	// production scale) before returning nonsense. Longitude wraps, so any
	// finite value is a real place; latitude beyond the poles is not.
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) || lat < -90 || lat > 90 {
		return nil, 0, 0, fmt.Errorf("%w: lat %v, lon %v", ErrInvalidCoordinate, lat, lon)
	}
	targetPoint := s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lon))

	var winner s2.EdgeQueryResult
	switch rank {
	case RankDistance:
		// MaxResults(1) prunes the search: the default (MaxInt32) would
		// collect and sort a result for every indexed point on each query.
		// The query object comes from distanceQueryPool: same query, same
		// options, recycled across calls (see pooledDistanceQuery for the
		// reuse-safety reading of the pinned golang/geo source).
		pq := f.distanceQuery()
		results := pq.query.FindEdges(s2.NewMinDistanceToPointTarget(targetPoint))
		if len(results) == 0 {
			f.distanceQueryPool.Put(pq)
			return nil, 0, 0, fmt.Errorf("no city found")
		}
		winner = results[0] // value copy, safe to make before returning the query
		f.distanceQueryPool.Put(pq)
	case RankPopulation:
		result, _, err := f.nearestByPopulation(targetPoint)
		if err != nil {
			return nil, 0, 0, err
		}
		winner = result
	default:
		return nil, 0, 0, fmt.Errorf("invalid rank %d", int(rank))
	}
	cityIndex := winner.EdgeID()
	if cityIndex < 0 || int(cityIndex) >= len(f.Cities) {
		return nil, 0, 0, fmt.Errorf("invalid city index %d found (total cities: %d)", cityIndex, len(f.Cities))
	}
	nearestCity := f.Cities[cityIndex]

	distanceKm := winner.Distance().Angle().Radians() * earthRadiusKm

	return &nearestCity, int(cityIndex), distanceKm, nil
}
