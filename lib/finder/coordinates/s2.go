package coordinates

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/cheggaaa/pb/v3"
	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
	"github.com/klauspost/compress/zstd"
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
	// initializer (re)attaches on every boot, warm or cold.
	Admin1Names map[string]string

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

// SerializableS2Finder is the v3 on-disk payload: the city table plus the
// admin attribution arrays and code tables. Admin1IDs/Admin2IDs are parallel
// to Cities (-1 = absent); the code tables hold "CC.CODE" composite keys in
// first-encounter order.
type SerializableS2Finder struct {
	Cities      []city.City
	Admin1IDs   []int32
	Admin2IDs   []int32
	Admin1Codes []string
	Admin2Codes []string
}

// Serialized index file format (version 3):
//
//	gob(indexHeader{Magic: "CFS2IDX", Version: 3, Count: len(Cities)})  // raw
//	zstd-frame(gob(SerializableS2Finder))                               // one frame
//
// The leading header lets a truncated or version-skewed file be rejected
// with a descriptive error instead of silently poisoning the finder with
// zero-filled data (gob zero-fills fields it does not find, so an index
// written before a payload-struct change would otherwise load as garbage).
// The header stays uncompressed so version checks — including v2 rejection —
// run before any decompression.
//
// Version history: v2 embedded Population in City (bumped in lockstep with
// the name index's City change); v3 adds the admin attribution arrays and
// moves the payload behind one zstd frame (the same framing the name index
// v2 established: SpeedFastest, frame CRC, per-call encoder/decoder). A v2
// file fails the version check below and returns ErrCorruptIndex exactly as
// truncated files do; the initializer's ensure*Index fallback rebuilds from
// the source datasets and rewrites the file as v3 — the proven v1→v2 path.
// Admin1Names is deliberately NOT in the payload: it is optional side data,
// re-attached per boot, so a names-file change never invalidates the index.
//
// Writes go to filepath+".part" and are renamed into place only after a
// complete encode, so a crash mid-write never replaces a valid index with a
// truncated one.
const (
	indexMagic   = "CFS2IDX"
	indexVersion = uint32(3)
)

// s2IndexZstdLevel trades compression ratio for encode/decode speed, matching
// the name index v2 framing decision: SpeedFastest keeps the decode off the
// warm-start critical path; SpeedDefault would shave a few more MB but tax
// every boot.
const s2IndexZstdLevel = zstd.SpeedFastest

// s2IndexZstdCRC enables the per-frame checksum: corruption inside an
// otherwise structurally valid frame is then detected deterministically by
// the decoder instead of surfacing as garbage that happens to survive gob.
const s2IndexZstdCRC = true

// decodeZstdFrame decompresses one complete zstd frame with a per-call
// decoder that is closed immediately afterwards. A shared package-level
// decoder was measured (name index v2) to retain ~900 MB of internal
// window/worker buffers after a prod-scale frame; warm start is a
// once-per-boot operation, so paying decoder setup (µs) to release that
// memory is strictly better. DecodeAll itself is stateless.
func decodeZstdFrame(compressed []byte) ([]byte, error) {
	zd, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer zd.Close()
	return zd.DecodeAll(compressed, nil)
}

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

// BuildIndex creates an S2 spatial index from raw city data.
func BuildIndex(cities []city.SpatialCity, config *config.S2) (*S2Finder, error) {
	points := make(s2.PointVector, len(cities))
	cityData := make([]city.City, len(cities))
	admin1IDs := make([]int32, len(cities))
	admin2IDs := make([]int32, len(cities))
	admin1Codes, admin2Codes := []string{}, []string{}
	admin1Index := make(map[string]int32, 8192)
	admin2Index := make(map[string]int32, 8192)

	// Use progress bar with infrequent updates to reduce overhead
	bar := pb.Full.Start(len(cities))
	bar.SetRefreshRate(time.Second) // Update every second instead of every item

	// Process cities in batches to minimize progress bar overhead
	batchSize := 100000 // Update progress every 100k items
	for i, spatialCity := range cities {
		points[i] = s2.PointFromLatLng(s2.LatLngFromDegrees(spatialCity.Latitude, spatialCity.Longitude))
		cityData[i] = spatialCity.City
		admin1IDs[i] = adminCodeID(spatialCity.Country, spatialCity.Admin1Code, admin1Index, &admin1Codes)
		admin2IDs[i] = adminCodeID(spatialCity.Country, spatialCity.Admin2Code, admin2Index, &admin2Codes)

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

	return &S2Finder{
		Index:         index,
		Cities:        cityData,
		Admin1IDs:     admin1IDs,
		Admin2IDs:     admin2IDs,
		Admin1Codes:   admin1Codes,
		Admin2Codes:   admin2Codes,
		maxPopulation: maxPopulationOf(cityData),
	}, nil
}

// AdminAttribution is the administrative attribution of one winning city:
// the raw per-country admin1/admin2 codes ("CA", "073" — not the composite
// "US.CA" table keys) and, when the optional names dataset was loaded, the
// admin1 display name. Zero values mean "absent": an empty Admin1Name is the
// codes-only mode, an empty Admin2Code a city without admin2 data.
type AdminAttribution struct {
	Admin1Code string
	Admin1Name string
	Admin2Code string
}

// rawAdminCode strips the "CC." country prefix from a composite table key.
// Country codes contain no '.', so the first dot is the separator; a key
// without one (defensive) is returned unchanged.
func rawAdminCode(composite string) string {
	if i := strings.IndexByte(composite, '.'); i >= 0 {
		return composite[i+1:]
	}
	return composite
}

// adminOf returns the attribution of the city at cityIndex. Out-of-range
// indexes or a finder without admin arrays (none of the constructors produce
// one, but a zero-value S2Finder is representable) yield the zero
// attribution: attribution is enhancement data, never a query failure.
func (f *S2Finder) adminOf(cityIndex int) AdminAttribution {
	var attr AdminAttribution
	if cityIndex < 0 || cityIndex >= len(f.Cities) {
		return attr
	}
	if cityIndex < len(f.Admin1IDs) {
		if id := f.Admin1IDs[cityIndex]; id >= 0 && int(id) < len(f.Admin1Codes) {
			composite := f.Admin1Codes[id]
			attr.Admin1Code = rawAdminCode(composite)
			attr.Admin1Name = f.Admin1Names[composite] // "" in codes-only mode
		}
	}
	if cityIndex < len(f.Admin2IDs) {
		if id := f.Admin2IDs[cityIndex]; id >= 0 && int(id) < len(f.Admin2Codes) {
			attr.Admin2Code = rawAdminCode(f.Admin2Codes[id])
		}
	}
	return attr
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

// nearest is the shared query core of NearestPlace and
// NearestPlaceWithAdmin: it also returns the winning city's index into
// Cities, which the attribution lookup needs and the plain callers discard.
func (f *S2Finder) nearest(lat, lon float64, rank Rank) (*city.City, int, float64, error) {
	if f.Index == nil {
		return nil, 0, 0, fmt.Errorf("s2 index is not initialized")
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
			return nil, 0, 0, fmt.Errorf("no city found")
		}
		winner = results[0]
	case RankPopulation:
		result, err := f.nearestByPopulation(targetPoint)
		if err != nil {
			return nil, 0, 0, err
		}
		winner = result
	default:
		return nil, 0, 0, fmt.Errorf("invalid rank %d", int(rank))
	}
	cityIndex := winner.EdgeID()
	if int(cityIndex) >= len(f.Cities) {
		return nil, 0, 0, fmt.Errorf("invalid city index %d found (total cities: %d)", cityIndex, len(f.Cities))
	}
	nearestCity := f.Cities[cityIndex]

	distanceKm := winner.Distance().Angle().Radians() * earthRadiusKm

	return &nearestCity, int(cityIndex), distanceKm, nil
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

// SerializeIndex saves the finder's data to a file. The stream is
// uncompressed header, then one zstd frame holding the gob payload (see the
// format comment above), written atomically: the bytes land in
// filepath+".part" first and are renamed over filepath only after a complete
// encode, so readers never observe a half-written index. A fresh zstd
// encoder per call: encoders are not reusable, and serialization is a
// one-shot init-path operation.
func (f *S2Finder) SerializeIndex(filepath string) error {
	payload := SerializableS2Finder{
		Cities:      f.Cities,
		Admin1IDs:   f.Admin1IDs,
		Admin2IDs:   f.Admin2IDs,
		Admin1Codes: f.Admin1Codes,
		Admin2Codes: f.Admin2Codes,
	}

	partPath := filepath + ".part"
	file, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("failed to create index file: %w", err)
	}
	fail := func(err error) error {
		_ = file.Close()
		_ = os.Remove(partPath)
		return err
	}

	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(indexHeader{Magic: indexMagic, Version: indexVersion, Count: len(f.Cities)}); err != nil {
		return fail(fmt.Errorf("failed to encode s2 index header: %w", err))
	}
	zw, err := zstd.NewWriter(file, zstd.WithEncoderLevel(s2IndexZstdLevel), zstd.WithEncoderCRC(s2IndexZstdCRC))
	if err != nil {
		return fail(fmt.Errorf("failed to create s2 index zstd writer: %w", err))
	}
	if err := gob.NewEncoder(zw).Encode(&payload); err != nil {
		_ = zw.Close()
		return fail(fmt.Errorf("failed to encode s2 index payload: %w", err))
	}
	if err := zw.Close(); err != nil {
		return fail(fmt.Errorf("failed to finalize s2 index zstd frame: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to close index file: %w", err)
	}
	if err := os.Rename(partPath, filepath); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to move %s to %s: %w", partPath, filepath, err)
	}
	return nil
}

// DeserializeIndex loads the finder's data from a file. The header must be
// readable and compatible (magic and version, including the v2 rejection)
// before any decompression runs; every decode failure — bad header,
// truncated or corrupted zstd frame, malformed payload, or counts that
// disagree with the header — wraps ErrCorruptIndex so the initializer can
// fall back to a rebuild (open/read/close failures are environmental and
// returned unwrapped). The frame is decompressed whole and the read/zstd/gob
// split is logged, mirroring the name index, so warm-start time stays
// attributable.
func DeserializeIndex(filepath string) (*S2Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}

	corrupt := func(format string, args ...any) error {
		return fmt.Errorf("%w: index file %s appears truncated or from an incompatible version; delete %s or rebuild the index: "+format,
			append([]any{ErrCorruptIndex, filepath, filepath}, args...)...)
	}

	// The bufio.Reader is shared by the header gob decoder and the payload
	// read: gob consumes exactly the header's bytes, and whatever it buffered
	// past them belongs to the zstd frame.
	bufFile := bufio.NewReader(file)
	var header indexHeader
	if err := gob.NewDecoder(bufFile).Decode(&header); err != nil {
		_ = file.Close()
		return nil, corrupt("header decode: %v", err)
	}
	if header.Magic != indexMagic {
		_ = file.Close()
		return nil, corrupt("bad magic %q (want %q)", header.Magic, indexMagic)
	}
	if header.Version != indexVersion {
		_ = file.Close()
		return nil, corrupt("unsupported version %d (want %d)", header.Version, indexVersion)
	}

	readStart := time.Now()
	compressed, err := io.ReadAll(bufFile)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("error reading s2 index payload from %s: %w", filepath, err)
	}
	compressedLen := len(compressed)
	zstdStart := time.Now()
	payloadBytes, err := decodeZstdFrame(compressed)
	if err != nil {
		_ = file.Close()
		return nil, corrupt("payload is not a decodable zstd frame: %v", err)
	}
	zstdDone := time.Now()
	compressed = nil // release the compressed buffer before the gob decode allocates
	gobStart := zstdDone
	var payload SerializableS2Finder
	if err := gob.NewDecoder(bytes.NewReader(payloadBytes)).Decode(&payload); err != nil {
		_ = file.Close()
		return nil, corrupt("payload decode: %v", err)
	}
	if header.Count != len(payload.Cities) {
		_ = file.Close()
		return nil, corrupt("payload holds %d cities but the header recorded %d", len(payload.Cities), header.Count)
	}
	if len(payload.Admin1IDs) != len(payload.Cities) || len(payload.Admin2IDs) != len(payload.Cities) {
		_ = file.Close()
		return nil, corrupt("admin id arrays hold %d/%d entries for %d cities", len(payload.Admin1IDs), len(payload.Admin2IDs), len(payload.Cities))
	}
	gobDone := time.Now()

	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("error closing file: %w", err)
	}
	log.Printf("s2 index %s decoded: read %d B in %s, zstd %d->%d B in %s, gob %s",
		filepath, compressedLen, zstdStart.Sub(readStart), compressedLen, len(payloadBytes),
		zstdDone.Sub(zstdStart), gobDone.Sub(gobStart))

	// Rebuild index efficiently without progress bar overhead. The same pass
	// bounds-checks every admin id: a structurally valid frame can still
	// carry ids that point outside the code tables, and such a file must be
	// rejected rather than surfaced as a wrong-code query answer.
	points := make(s2.PointVector, len(payload.Cities))
	for i, c := range payload.Cities {
		points[i] = s2.PointFromLatLng(s2.LatLngFromDegrees(c.Latitude, c.Longitude))
		if id := payload.Admin1IDs[i]; id >= 0 && int(id) >= len(payload.Admin1Codes) {
			return nil, corrupt("admin1 id %d at city %d outside the %d-entry table", id, i, len(payload.Admin1Codes))
		}
		if id := payload.Admin2IDs[i]; id >= 0 && int(id) >= len(payload.Admin2Codes) {
			return nil, corrupt("admin2 id %d at city %d outside the %d-entry table", id, i, len(payload.Admin2Codes))
		}
	}

	index := s2.NewShapeIndex()
	index.Add(&points)
	// Build eagerly so the first query does not pay the construction cost.
	index.Build()

	return &S2Finder{
		Index:         index,
		Cities:        payload.Cities,
		Admin1IDs:     payload.Admin1IDs,
		Admin2IDs:     payload.Admin2IDs,
		Admin1Codes:   payload.Admin1Codes,
		Admin2Codes:   payload.Admin2Codes,
		maxPopulation: maxPopulationOf(payload.Cities),
	}, nil
}
