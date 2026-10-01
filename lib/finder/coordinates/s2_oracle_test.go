package coordinates

import (
	"math"
	"math/rand"
	"os"
	"sort"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/golang/geo/s2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tieToleranceKm is the equidistance tolerance: when two cities are within
// this distance of each other relative to the query point, either winner is
// accepted. 1 meter.
const tieToleranceKm = 0.001

// oracleHaversineKm returns the great-circle distance in kilometers between
// two degree coordinates on a sphere of radius 6371 km — the same earth model
// NearestPlace reports (angle in radians * earthRadiusKm).
func oracleHaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const degToRad = math.Pi / 180
	phi1 := lat1 * degToRad
	phi2 := lat2 * degToRad
	dPhi := (lat2 - lat1) * degToRad
	dLambda := (lon2 - lon1) * degToRad
	sinPhi := math.Sin(dPhi / 2)
	sinLambda := math.Sin(dLambda / 2)
	a := sinPhi*sinPhi + math.Cos(phi1)*math.Cos(phi2)*sinLambda*sinLambda
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// oracleCitySet builds a deterministic city set: fixed edge-case cities
// (poles, date line, equator, exact equidistant pairs) plus seeded uniform
// random cities. All names are unique.
func oracleCitySet() []city.SpatialCity {
	fixed := []city.SpatialCity{
		{City: city.City{Name: "San Francisco", Latitude: 37.7749, Longitude: -122.4194}},
		{City: city.City{Name: "New York", Latitude: 40.7128, Longitude: -74.0060}},
		{City: city.City{Name: "London", Latitude: 51.5074, Longitude: -0.1278}},
		{City: city.City{Name: "Tokyo", Latitude: 35.6762, Longitude: 139.6503}},
		{City: city.City{Name: "Sydney", Latitude: -33.8688, Longitude: 151.2093}},
		{City: city.City{Name: "Cape Town", Latitude: -33.9249, Longitude: 18.4241}},
		{City: city.City{Name: "Buenos Aires", Latitude: -34.6037, Longitude: -58.3816}},
		{City: city.City{Name: "Reykjavik", Latitude: 64.1466, Longitude: -21.9426}},
		{City: city.City{Name: "Auckland", Latitude: -36.8485, Longitude: 174.7633}},
		{City: city.City{Name: "Honolulu", Latitude: 21.3069, Longitude: -157.8583}},
		{City: city.City{Name: "Singapore", Latitude: 1.3521, Longitude: 103.8198}},
		{City: city.City{Name: "North Pole", Latitude: 90.0, Longitude: 0.0}},
		{City: city.City{Name: "South Pole", Latitude: -90.0, Longitude: 0.0}},
		{City: city.City{Name: "Date Line East", Latitude: 0.0, Longitude: 179.5}},
		{City: city.City{Name: "Date Line West", Latitude: 0.0, Longitude: -179.5}},
		{City: city.City{Name: "High Arctic", Latitude: 85.5, Longitude: 178.2}},
		{City: city.City{Name: "Equator Prime", Latitude: 0.0, Longitude: 0.0}},
		// Exact equidistant pairs: the query midpoints hit these dead-on.
		{City: city.City{Name: "Pair A West", Latitude: 0.0, Longitude: -20.0}},
		{City: city.City{Name: "Pair A East", Latitude: 0.0, Longitude: -18.0}},
		{City: city.City{Name: "Pair B West", Latitude: 45.0, Longitude: 10.5}},
		{City: city.City{Name: "Pair B East", Latitude: 45.0, Longitude: 11.5}},
		{City: city.City{Name: "Near Pole North", Latitude: 89.0, Longitude: 0.0}},
		{City: city.City{Name: "Near Pole South", Latitude: -89.0, Longitude: 0.0}},
	}

	cities := make([]city.SpatialCity, 0, len(fixed)+500)
	cities = append(cities, fixed...)

	rng := rand.New(rand.NewSource(20260930))
	for i := 0; i < 500; i++ {
		cities = append(cities, city.SpatialCity{
			City: city.City{
				Name:      "RandCity-" + itoa3(i),
				Country:   "RC",
				Latitude:  rng.Float64()*180 - 90,
				Longitude: rng.Float64()*360 - 180,
			},
		})
	}
	return cities
}

func itoa3(i int) string {
	return string([]byte{byte('0' + i/100), byte('0' + (i/10)%10), byte('0' + i%10)})
}

// oracleQueries builds the deterministic query list: seeded random points,
// polar/date-line/equator edge cases, exact city coordinates, and midpoints
// between near-equidistant cities.
func oracleQueries(cities []city.SpatialCity) []struct{ lat, lon float64 } {
	type q = struct{ lat, lon float64 }
	queries := make([]q, 0, 260)

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 200; i++ {
		queries = append(queries, q{rng.Float64()*180 - 90, rng.Float64()*360 - 180})
	}

	// Edge cases: both poles, date line, equator, corner extremes.
	queries = append(queries,
		q{90.0, 0.0},
		q{-90.0, 0.0},
		q{0.0, 179.999},
		q{0.0, -179.999},
		q{45.0, 179.999},
		q{45.0, -179.999},
		q{-45.0, 179.999},
		q{0.0, 0.0},
		q{0.0, 120.0},
		q{89.9999, 179.9999},
		q{-89.9999, -179.9999},
	)

	// Exact city coordinates: every fixed city (first 24) plus the next ten
	// seeded random ones.
	for i := 0; i < min(len(cities), 34); i++ {
		queries = append(queries, q{cities[i].Latitude, cities[i].Longitude})
	}

	// Midpoints between near-equidistant cities.
	queries = append(queries,
		q{0.0, -19.0}, // exactly between Pair A West/East
		q{45.0, 11.0}, // exactly between Pair B West/East
		q{(37.7749 + 40.7128) / 2, (-122.4194 + -74.0060) / 2}, // SF/NYC midpoint
		q{(35.6762 + -33.8688) / 2, (139.6503 + 151.2093) / 2}, // Tokyo/Sydney midpoint
	)

	return queries
}

// bruteForceNearest returns the name and distance of the haversine-nearest
// city, plus the second-best distance (math.Inf when there is only one city).
func bruteForceNearest(cities []city.SpatialCity, lat, lon float64) (string, float64, float64) {
	best, second := math.Inf(1), math.Inf(1)
	bestName := ""
	for _, c := range cities {
		d := oracleHaversineKm(lat, lon, c.Latitude, c.Longitude)
		if d < best {
			second = best
			best, bestName = d, c.Name
		} else if d < second {
			second = d
		}
	}
	return bestName, best, second
}

// checkAgainstOracle runs every query against the finder and compares with the
// brute-force nearest. When two cities are within tieToleranceKm of
// equidistant, either winner is accepted.
func checkAgainstOracle(t *testing.T, finder *S2Finder, cities []city.SpatialCity, queries []struct{ lat, lon float64 }) {
	t.Helper()
	for i, query := range queries {
		got, gotDist, err := finder.NearestPlace(query.lat, query.lon, RankDistance)
		require.NoError(t, err, "query %d (%.6f, %.6f): NearestPlace failed", i, query.lat, query.lon)
		require.NotNil(t, got, "query %d (%.6f, %.6f): nil city", i, query.lat, query.lon)

		bestName, bestDist, secondDist := bruteForceNearest(cities, query.lat, query.lon)

		// Reported distance must match the great-circle distance to the
		// returned city (same sphere model), within 1 m.
		trueDist := oracleHaversineKm(query.lat, query.lon, got.Latitude, got.Longitude)
		assert.InDelta(t, trueDist, gotDist, tieToleranceKm,
			"query %d (%.6f, %.6f): returned city %q distance %.6f km, haversine says %.6f km",
			i, query.lat, query.lon, got.Name, gotDist, trueDist)

		// Returned city must be within 1 m of the optimal distance.
		assert.LessOrEqual(t, trueDist, bestDist+tieToleranceKm,
			"query %d (%.6f, %.6f): returned city %q at %.6f km, but brute-force best is %q at %.6f km",
			i, query.lat, query.lon, got.Name, trueDist, bestName, bestDist)

		// When the winner is unique (runner-up more than 1 m farther), the
		// returned city must be exactly the brute-force winner.
		if secondDist-bestDist > tieToleranceKm {
			assert.Equal(t, bestName, got.Name,
				"query %d (%.6f, %.6f): unique winner mismatch: got %q (%.6f km), want %q (%.6f km, second %.6f km)",
				i, query.lat, query.lon, got.Name, trueDist, bestName, bestDist, secondDist)
		}
	}
}

// TestNearestPlaceMatchesBruteForceOracle is the correctness oracle for the
// nearest-neighbor query. It guards performance changes (MaxResults pruning,
// eager index builds) against silently returning a wrong nearest city.
func TestNearestPlaceMatchesBruteForceOracle(t *testing.T) {
	cities := oracleCitySet()
	queries := oracleQueries(cities)

	t.Run("BuildIndex", func(t *testing.T) {
		finder, err := BuildIndex(cities, &config.S2{})
		require.NoError(t, err)
		require.NotNil(t, finder)
		checkAgainstOracle(t, finder, cities, queries)
	})

	t.Run("DeserializeIndex", func(t *testing.T) {
		source, err := BuildIndex(cities, &config.S2{})
		require.NoError(t, err)

		tmpfile, err := os.CreateTemp("", "s2oracle_*.gob")
		require.NoError(t, err)
		defer func() { _ = os.Remove(tmpfile.Name()) }()
		require.NoError(t, source.SerializeIndex(tmpfile.Name()))

		finder, err := DeserializeIndex(tmpfile.Name())
		require.NoError(t, err)
		require.NotNil(t, finder)
		checkAgainstOracle(t, finder, cities, queries)
	})
}

// weightedCitySet is the dedicated fixture for population ranking. It pins
// the scenarios the ranking exists for: a village sitting right next to a
// real city (the city must win under gravity even though the village is
// closer), and a distant big city that must LOSE to the nearby city because
// of the squared-distance decay. Zero-population neighbors model the GeoNames
// norm (most rows carry no population and score 0 everywhere). The Crowd
// Out cluster places 18 tiny places nearer to the query than a populous
// city: a top-16 candidate-pool implementation returns a tiny place there,
// while the exact escalating-radius search must reach through them and
// return the city.
func weightedCitySet() []city.SpatialCity {
	fixed := []city.SpatialCity{
		{City: city.City{Name: "Village", Latitude: 20.0000, Longitude: 20.0000, Population: 200}},
		{City: city.City{Name: "Zero Pop Neighbor", Latitude: 20.0010, Longitude: 20.0010}},
		{City: city.City{Name: "Near City", Latitude: 20.0600, Longitude: 20.0600, Population: 2_000_000}},
		{City: city.City{Name: "Mega City", Latitude: 21.5000, Longitude: 21.5000, Population: 20_000_000}},
		{City: city.City{Name: "Null Island Hut", Latitude: 0.5000, Longitude: 0.5000}},
		{City: city.City{Name: "Far Big", Latitude: -33.0000, Longitude: 151.0000, Population: 5_000_000}},
	}

	// Crowd Out cluster: 18 populated-but-tiny places sprinkled within ~3 km
	// of (40.0, 40.0), plus a 5M city 12 km away. From the cluster center
	// every tiny place outscores nothing, but the city scores
	// 5e6/(12^2+1) ~= 34,722 versus at most 900 for any tiny place — the
	// exact winner is the city, and any 16-nearest pool finds only tiny
	// places. This is the fixture case the retired k=16 pool failed.
	for i := 0; i < 18; i++ {
		fixed = append(fixed, city.SpatialCity{City: city.City{
			Name:       "Crowd Out Hamlet " + itoa3(i),
			Country:    "RC",
			Latitude:   40.0 + 0.03*float64(i+1)/18.0,
			Longitude:  40.0 + 0.02*float64(i)/18.0,
			Population: int32(100 + i*50), // 100..900
		}})
	}
	fixed = append(fixed, city.SpatialCity{City: city.City{
		Name:       "Crowd Out City",
		Country:    "RC",
		Latitude:   40.08,
		Longitude:  40.08, // ~12.3 km northeast of the cluster center
		Population: 5_000_000,
	}})

	cities := make([]city.SpatialCity, 0, len(fixed)+18)
	cities = append(cities, fixed...)

	// Worldwide filler with mixed populations (including zeros) so the
	// search neighborhoods look like the prod index.
	rng := rand.New(rand.NewSource(20261001))
	for i := 0; i < 18; i++ {
		population := int32(0)
		if i%3 != 0 { // a third of the fillers stay population-less
			population = int32(rng.Int31n(3_000_000))
		}
		cities = append(cities, city.SpatialCity{
			City: city.City{
				Name:       "WeightedFiller-" + itoa3(i),
				Country:    "RC",
				Latitude:   rng.Float64()*180 - 90,
				Longitude:  rng.Float64()*360 - 180,
				Population: population,
			},
		})
	}
	return cities
}

// bruteForcePopulationRank is the exact gravity oracle: every city scores
// population / (d*d + 1) with d the haversine km to the query point, and the
// winner is the max score with ties resolved to the smaller distance, then
// the lower city index (mirroring the finder's scan over distance-sorted
// results). It returns the winning city name, the winning score, and the
// runner-up score (math.Inf(-1) for a single-city fixture).
func bruteForcePopulationRank(cities []city.SpatialCity, lat, lon float64) (string, float64, float64) {
	type candidate struct {
		index int
		dist  float64
		score float64
	}
	all := make([]candidate, len(cities))
	for i, c := range cities {
		d := oracleHaversineKm(lat, lon, c.Latitude, c.Longitude)
		all[i] = candidate{index: i, dist: d, score: float64(c.Population) / (d*d + 1.0)}
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].dist != all[b].dist {
			return all[a].dist < all[b].dist
		}
		return all[a].index < all[b].index
	})

	best := all[0]
	second := math.Inf(-1)
	for _, cand := range all[1:] {
		if cand.score > best.score {
			second = best.score
			best = cand
		} else if cand.score > second {
			second = cand.score
		}
	}
	return cities[best.index].Name, best.score, second
}

// checkAgainstWeightedOracle runs every query in both ranking modes and
// compares each with its brute-force oracle: rank=distance against the pure
// nearest-city oracle, rank=population against the gravity-model oracle.
// Score comparisons carry a relative tolerance because the finder measures
// distance via the s2 angle while the oracle uses haversine (agreement to
// under a meter, per the distance oracle), which perturbs scores in the far
// decimals.
func checkAgainstWeightedOracle(t *testing.T, finder *S2Finder, cities []city.SpatialCity, queries []struct{ lat, lon float64 }) {
	t.Helper()
	const relativeScoreTolerance = 1e-9
	for i, query := range queries {
		// Distance mode must keep matching the pure-distance oracle.
		gotDistance, gotDistanceKm, err := finder.NearestPlace(query.lat, query.lon, RankDistance)
		require.NoError(t, err, "query %d (%.6f, %.6f): distance rank failed", i, query.lat, query.lon)

		bestName, bestDist, secondDist := bruteForceNearest(cities, query.lat, query.lon)
		trueDist := oracleHaversineKm(query.lat, query.lon, gotDistance.Latitude, gotDistance.Longitude)
		assert.InDelta(t, trueDist, gotDistanceKm, tieToleranceKm,
			"query %d (%.6f, %.6f): distance rank returned %q at %.6f km, haversine says %.6f km",
			i, query.lat, query.lon, gotDistance.Name, gotDistanceKm, trueDist)
		if secondDist-bestDist > tieToleranceKm {
			assert.Equal(t, bestName, gotDistance.Name,
				"query %d (%.6f, %.6f): distance rank unique winner mismatch: got %q, want %q",
				i, query.lat, query.lon, gotDistance.Name, bestName)
		}

		// Population mode must match the gravity oracle.
		gotWeighted, gotWeightedKm, err := finder.NearestPlace(query.lat, query.lon, RankPopulation)
		require.NoError(t, err, "query %d (%.6f, %.6f): population rank failed", i, query.lat, query.lon)
		require.NotNil(t, gotWeighted)

		wantName, bestScore, secondScore := bruteForcePopulationRank(cities, query.lat, query.lon)

		// The reported distance must be the great-circle distance to the
		// returned (winning) city, within 1 m.
		trueWeightedDist := oracleHaversineKm(query.lat, query.lon, gotWeighted.Latitude, gotWeighted.Longitude)
		assert.InDelta(t, trueWeightedDist, gotWeightedKm, tieToleranceKm,
			"query %d (%.6f, %.6f): population rank returned %q at %.6f km, haversine says %.6f km",
			i, query.lat, query.lon, gotWeighted.Name, gotWeightedKm, trueWeightedDist)

		// The returned city's own oracle score must be within floating-point
		// noise of the oracle winner's score.
		gotScore := float64(gotWeighted.Population) / (trueWeightedDist*trueWeightedDist + 1.0)
		assert.GreaterOrEqual(t, gotScore, bestScore*(1-relativeScoreTolerance),
			"query %d (%.6f, %.6f): population rank returned %q (score %.6f), oracle winner %q scores %.6f",
			i, query.lat, query.lon, gotWeighted.Name, gotScore, wantName, bestScore)

		// When the gravity winner is unique (runner-up strictly behind beyond
		// the tolerance), the returned city must be exactly that winner.
		if secondScore < bestScore*(1-relativeScoreTolerance) {
			assert.Equal(t, wantName, gotWeighted.Name,
				"query %d (%.6f, %.6f): population rank unique winner mismatch: got %q, want %q",
				i, query.lat, query.lon, gotWeighted.Name, wantName)
		}
	}
}

// TestNearestPlacePopulationRankMatchesOracle is the correctness oracle for
// population-weighted ranking. It checks NearestPlace(RankPopulation)
// against the exact all-cities gravity oracle, so any truncation or radius
// bug shows up as a winner mismatch. The fixture guarantees several
// discriminating queries: the village cluster (the distance winner is a tiny
// place while a real city takes the gravity crown), the Crowd Out cluster
// (18 tiny places closer than the winning city — the retired top-16 pool
// implementation failed this), and exact mega-city coordinates (the decay
// must beat raw population). A test cannot pass by accident through an
// all-zero-population fixture.
func TestNearestPlacePopulationRankMatchesOracle(t *testing.T) {
	cities := weightedCitySet()

	type q = struct{ lat, lon float64 }
	queries := []q{
		{20.0000, 20.0000}, // exactly at Village: distance=Village, gravity=Near City
		{20.0005, 20.0005}, // between Village and Zero Pop Neighbor
		{20.0010, 20.0010}, // exactly at Zero Pop Neighbor
		{20.0300, 20.0300}, // midpoint Village/Near City
		{20.0600, 20.0600}, // exactly at Near City
		{21.5000, 21.5000}, // exactly at Mega City
		{20.7800, 20.7800}, // between Near City and Mega City
		// Crowd Out cluster: 18 tiny places nearer than the 5M city — the
		// gravity winner sits beyond any 16-nearest pool.
		{40.0000, 40.0000}, // cluster center
		{40.0300, 40.0200}, // inside the cluster, off-center
		{40.0800, 40.0800}, // exactly at Crowd Out City
	}

	rng := rand.New(rand.NewSource(20261002))
	for i := 0; i < 200; i++ {
		queries = append(queries, q{rng.Float64()*180 - 90, rng.Float64()*360 - 180})
	}

	t.Run("BuildIndex", func(t *testing.T) {
		finder, err := BuildIndex(cities, &config.S2{})
		require.NoError(t, err)
		checkAgainstWeightedOracle(t, finder, cities, queries)
	})

	t.Run("DeserializeIndex", func(t *testing.T) {
		source, err := BuildIndex(cities, &config.S2{})
		require.NoError(t, err)

		tmpfile, err := os.CreateTemp("", "s2oraclew_*.gob")
		require.NoError(t, err)
		defer func() { _ = os.Remove(tmpfile.Name()) }()
		require.NoError(t, source.SerializeIndex(tmpfile.Name()))

		finder, err := DeserializeIndex(tmpfile.Name())
		require.NoError(t, err)
		checkAgainstWeightedOracle(t, finder, cities, queries)
	})
}

// TestNearestPlaceDistanceRankMatchesMaxResultsOne pins the equivalence the
// distance path owes the API: whatever mechanism NearestPlace uses for
// RankDistance, its city and distance must be exactly what the historical
// MaxResults(1) query issued verbatim here returns. (An earlier
// implementation shared a multi-result pool fetch between both modes and
// relied on results[0] — provably equivalent, but it inherited the full-scan
// cost of multi-result queries in this golang/geo version; this test guards
// whichever mechanism ships.) The randomized points are also checked against
// the brute-force oracle by the suite above.
func TestNearestPlaceDistanceRankMatchesMaxResultsOne(t *testing.T) {
	cities := oracleCitySet()
	finder, err := BuildIndex(cities, &config.S2{})
	require.NoError(t, err)

	rng := rand.New(rand.NewSource(20261003))
	for i := 0; i < 500; i++ {
		lat := rng.Float64()*180 - 90
		lon := rng.Float64()*360 - 180

		// The exact query NearestPlace issued before ranking existed.
		oldQuery := s2.NewClosestEdgeQuery(finder.Index, s2.NewClosestEdgeQueryOptions().MaxResults(1))
		oldResults := oldQuery.FindEdges(s2.NewMinDistanceToPointTarget(
			s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lon))))
		require.NotEmpty(t, oldResults, "query %d: MaxResults(1) returned nothing", i)
		oldIndex := oldResults[0].EdgeID()
		require.Less(t, int(oldIndex), len(finder.Cities), "query %d: edge %d out of range", i, oldIndex)
		oldCity := finder.Cities[oldIndex]
		oldDistanceKm := oldResults[0].Distance().Angle().Radians() * earthRadiusKm

		got, gotDistanceKm, err := finder.NearestPlace(lat, lon, RankDistance)
		require.NoError(t, err, "query %d (%.6f, %.6f)", i, lat, lon)

		// Same distance to floating-point noise: both derive from the s2
		// angle to the winning point, so any real difference means a
		// different winner. Only an exactly tied distance (duplicate
		// coordinates hit by a random float query — probability ~0) may
		// legitimately return a different city.
		assert.InDelta(t, oldDistanceKm, gotDistanceKm, 1e-12,
			"query %d (%.6f, %.6f): distance rank returned %q at %.9f km, MaxResults(1) returned %q at %.9f km",
			i, lat, lon, got.Name, gotDistanceKm, oldCity.Name, oldDistanceKm)
		if math.Abs(oldDistanceKm-gotDistanceKm) > 1e-12 {
			assert.Equal(t, oldCity.Name, got.Name,
				"query %d (%.6f, %.6f): distance rank returned %q, MaxResults(1) returned %q",
				i, lat, lon, got.Name, oldCity.Name)
		}
	}
}
