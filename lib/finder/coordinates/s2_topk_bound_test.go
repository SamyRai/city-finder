package coordinates

import (
	"math/rand"
	"os"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/golang/geo/s2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTopPopulationsOf pins the construction rules of the top-K population
// table: population > 0 entries only, sorted by population descending, and
// truncated at topPopulationK.
func TestTopPopulationsOf(t *testing.T) {
	t.Run("excludes non-positive populations", func(t *testing.T) {
		cities := []city.City{
			{Name: "A", Latitude: 1, Longitude: 1, Population: 5},
			{Name: "B", Latitude: 2, Longitude: 2, Population: -3},
			{Name: "C", Latitude: 3, Longitude: 3, Population: 0},
			{Name: "D", Latitude: 4, Longitude: 4, Population: 7},
			{Name: "E", Latitude: 5, Longitude: 5, Population: -1},
		}
		table := topPopulationsOf(cities)
		require.Len(t, table, 2)
		assert.EqualValues(t, 7, table[0].population)
		assert.EqualValues(t, 5, table[1].population)
		// The stored point must be the city's indexed location.
		assert.True(t, table[0].point.ApproxEqual(s2.PointFromLatLng(s2.LatLngFromDegrees(4, 4))))
	})

	t.Run("all zero populations yield an empty table", func(t *testing.T) {
		assert.Empty(t, topPopulationsOf([]city.City{
			{Name: "A", Latitude: 1, Longitude: 1},
			{Name: "B", Latitude: 2, Longitude: 2},
		}))
		assert.Empty(t, topPopulationsOf(nil))
	})

	t.Run("weighted fixture: sorted descending, positives only", func(t *testing.T) {
		finder, err := BuildIndex(weightedCitySet(), &config.S2{})
		require.NoError(t, err)

		positives := 0
		for _, c := range finder.Cities {
			if c.Population > 0 {
				positives++
			}
		}
		require.Len(t, finder.topPopulations, positives)
		for i, entry := range finder.topPopulations {
			assert.Greater(t, entry.population, int32(0), "entry %d must carry a positive population", i)
			if i > 0 {
				assert.GreaterOrEqual(t, finder.topPopulations[i-1].population, entry.population,
					"table must be sorted by population descending at %d", i)
			}
		}
		assert.EqualValues(t, finder.maxPopulation, finder.topPopulations[0].population)
	})
}

// TestNearestPlacePopulationRankAllZeroPopulation is the missing oracle for
// the all-zero-population fixture under RankPopulation: with no population
// data anywhere, every gravity score is 0 and the gravity winner must be the
// plain nearest city (the degenerate full-scan path of
// nearestByPopulation). It follows the distance oracle's acceptance rules:
// the returned city is within tieToleranceKm of the brute-force optimum and
// is exactly the brute-force winner whenever that winner is unique.
func TestNearestPlacePopulationRankAllZeroPopulation(t *testing.T) {
	cities := oracleCitySet()
	for i := range cities {
		require.Zero(t, cities[i].Population, "fixture city %q must carry no population", cities[i].Name)
	}
	queries := oracleQueries(cities)

	check := func(t *testing.T, finder *S2Finder) {
		t.Helper()
		for i, query := range queries {
			got, gotDist, err := finder.NearestPlace(query.lat, query.lon, RankPopulation)
			require.NoError(t, err, "query %d (%.6f, %.6f): population rank failed", i, query.lat, query.lon)
			require.NotNil(t, got)

			bestName, bestDist, secondDist := bruteForceNearest(cities, query.lat, query.lon)
			trueDist := oracleHaversineKm(query.lat, query.lon, got.Latitude, got.Longitude)
			assert.InDelta(t, trueDist, gotDist, tieToleranceKm,
				"query %d (%.6f, %.6f): returned %q at %.6f km, haversine says %.6f km",
				i, query.lat, query.lon, got.Name, gotDist, trueDist)
			assert.LessOrEqual(t, trueDist, bestDist+tieToleranceKm,
				"query %d (%.6f, %.6f): returned %q at %.6f km, brute-force nearest is %q at %.6f km",
				i, query.lat, query.lon, got.Name, trueDist, bestName, bestDist)
			if secondDist-bestDist > tieToleranceKm {
				assert.Equal(t, bestName, got.Name,
					"query %d (%.6f, %.6f): unique nearest city mismatch: got %q, want %q",
					i, query.lat, query.lon, got.Name, bestName)
			}
		}
	}

	t.Run("BuildIndex", func(t *testing.T) {
		finder, err := BuildIndex(cities, &config.S2{})
		require.NoError(t, err)
		check(t, finder)
	})

	t.Run("DeserializeIndex", func(t *testing.T) {
		source, err := BuildIndex(cities, &config.S2{})
		require.NoError(t, err)

		tmpfile, err := os.CreateTemp("", "s2oraclezero_*.gob")
		require.NoError(t, err)
		defer func() { _ = os.Remove(tmpfile.Name()) }()
		require.NoError(t, source.SerializeIndex(tmpfile.Name()))

		finder, err := DeserializeIndex(tmpfile.Name())
		require.NoError(t, err)
		check(t, finder)
	})
}

// TestPopulationRankTopKBoundStopsEscalationEarly proves the top-K anytime
// bound shortens escalation where the retired single-max bound could not:
// from a mid-Pacific query the best in-disc score (s* ~ 0.647, a 2k-population
// island 55.6 km away) sits far below maxPopulation/(R^2+1) for every R in
// the ladder — even at the 6250 km disc the old bound is 40M/(6250^2+1) ~
// 1.024 > s*, so the old code necessarily ran the terminal unbounded
// full-sphere scan. The top-K bound instead compares s* against the antipodal
// 40M megacity's exact score (~0.1) and certifies at the 250 km disc, the
// first one that contains the island.
func TestPopulationRankTopKBoundStopsEscalationEarly(t *testing.T) {
	cities := []city.SpatialCity{
		{City: city.City{Name: "Mid-Pacific Island", Latitude: 0.0, Longitude: -139.5, Population: 2_000}},
		{City: city.City{Name: "Antipodal Megacity", Latitude: 0.0, Longitude: 40.0, Population: 40_000_000}},
		// Zero-population fillers (GeoNames norm): score 0, placed well
		// outside every early disc.
		{City: city.City{Name: "Zero Pop Atoll", Latitude: 10.0, Longitude: -150.0}},
		{City: city.City{Name: "Zero Pop Reef", Latitude: -45.0, Longitude: 20.0}},
	}
	finder, err := BuildIndex(cities, &config.S2{})
	require.NoError(t, err)
	require.EqualValues(t, 40_000_000, finder.maxPopulation)

	const queryLat, queryLon = 0.0, -140.0
	result, radiusKm, err := finder.nearestByPopulation(
		s2.PointFromLatLng(s2.LatLngFromDegrees(queryLat, queryLon)))
	require.NoError(t, err)
	winnerIndex := int(result.EdgeID())
	require.GreaterOrEqual(t, winnerIndex, 0)
	require.Less(t, winnerIndex, len(finder.Cities))
	winner := finder.Cities[winnerIndex]

	// (a) Exactness: the early stop must return the brute-force winner.
	wantName, wantScore, _ := bruteForcePopulationRank(cities, queryLat, queryLon)
	assert.Equal(t, "Mid-Pacific Island", winner.Name)
	assert.Equal(t, wantName, winner.Name, "early termination returned a non-optimal city")
	distanceKm := result.Distance().Angle().Radians() * earthRadiusKm
	gotScore := float64(winner.Population) / (distanceKm*distanceKm + 1.0)
	assert.InDelta(t, wantScore, gotScore, wantScore*1e-9)

	// The public API must agree (it runs the same path).
	got, _, err := finder.NearestPlace(queryLat, queryLon, RankPopulation)
	require.NoError(t, err)
	assert.Equal(t, winner.Name, got.Name)

	// (b) Escalation stopped at the 250 km disc — before the 6250 km disc
	// and before maxSearchRadiusKm, the sentinel the terminal unbounded
	// iteration reports. The discs at 10 and 50 km are empty (the island is
	// 55.6 km out), so 250 is the first certified disc on the ladder
	// 10 -> 50 -> 250 -> 1250 -> 6250 -> unbounded.
	assert.Equal(t, 250.0, radiusKm, "the top-K bound must certify at the 250 km disc")
	assert.Less(t, radiusKm, 6250.0, "escalation must stop before the 6250 km disc")
	assert.Less(t, radiusKm, maxSearchRadiusKm, "the terminal unbounded iteration must not run")

	// ...while the retired single-max bound provably could not have stopped
	// at or before the 6250 km disc: the winner's score stays under
	// maxPopulation/(6250^2+1), so that bound would have escalated into the
	// unbounded full-sphere scan.
	oldBoundAt6250 := float64(finder.maxPopulation) / (6250.0*6250.0 + 1.0)
	assert.LessOrEqual(t, gotScore, oldBoundAt6250,
		"fixture drifted: winner score %.6f now beats the old 6250 km bound %.6f, so this test no longer proves early stopping",
		gotScore, oldBoundAt6250)
}

// TestPopulationRankPopKScaleOracle runs the exactness oracle on a fixture
// with MORE than topPopulationK positive populations, the regime where
// cities genuinely sit outside the top-K table and are bounded only by the
// K-th largest population (popK). A bound bug that under-covers excluded
// cities (wrong popK, inverted in/out branch, missing term) surfaces here as
// a winner mismatch against the brute-force gravity oracle.
func TestPopulationRankPopKScaleOracle(t *testing.T) {
	// 4096 giants with distinct populations 36M down to popK = 3.24M fill
	// the entire top-K table; 8 hamlets (populations 900..907) stay outside
	// it and cluster around the primary query point.
	const hamletBaseLat, hamletBaseLon = 0.0, -140.0
	cities := make([]city.SpatialCity, 0, topPopulationK+8)
	for i := 0; i < topPopulationK; i++ {
		cities = append(cities, city.SpatialCity{City: city.City{
			Name:       "Giant " + itoa3(i%1000) + "-" + itoa3(i/1000),
			Country:    "GI",
			Latitude:   0,
			Longitude:  0,
			Population: int32(36_000_000 - 8000*i),
		}})
	}
	rng := rand.New(rand.NewSource(20261004))
	for i := range cities {
		cities[i].Latitude = rng.Float64()*180 - 90
		cities[i].Longitude = rng.Float64()*360 - 180
	}
	for i := 0; i < 8; i++ {
		cities = append(cities, city.SpatialCity{City: city.City{
			Name:       "Hamlet " + itoa3(i),
			Country:    "HM",
			Latitude:   hamletBaseLat + 0.01*float64(i),
			Longitude:  hamletBaseLon + 0.01*float64(i),
			Population: int32(900 + i),
		}})
	}

	finder, err := BuildIndex(cities, &config.S2{})
	require.NoError(t, err)

	// Table derivation checks: exactly K entries, popK = the smallest giant
	// population, no hamlet leaked in.
	require.Len(t, finder.topPopulations, topPopulationK)
	assert.EqualValues(t, 3_240_000, finder.topPopulations[topPopulationK-1].population)
	assert.EqualValues(t, 36_000_000, finder.topPopulations[0].population)

	// The popK term must be live but never exceed the retired single-max
	// bound at any radius — the invariant that makes the new bound strictly
	// at-least-as-eager as the old one.
	target := s2.PointFromLatLng(s2.LatLngFromDegrees(hamletBaseLat, hamletBaseLon))
	for _, radiusKm := range []float64{10, 50, 250, 1250, 6250} {
		bound := finder.populationOutsideBound(radiusKm, target)
		oldBound := float64(finder.maxPopulation) / (radiusKm*radiusKm + 1.0)
		assert.Greater(t, bound, 0.0, "R=%.0f: popK term must be live with a full table", radiusKm)
		assert.LessOrEqual(t, bound, oldBound,
			"R=%.0f: top-K bound %.6f exceeds the single-max bound %.6f it replaces", radiusKm, bound, oldBound)
	}

	type q = struct{ lat, lon float64 }
	queries := []q{{hamletBaseLat, hamletBaseLon}, {hamletBaseLat + 0.04, hamletBaseLon + 0.04}}
	for i := 0; i < 30; i++ {
		queries = append(queries, q{rng.Float64()*180 - 90, rng.Float64()*360 - 180})
	}

	const relativeScoreTolerance = 1e-9
	for i, query := range queries {
		got, gotDist, err := finder.NearestPlace(query.lat, query.lon, RankPopulation)
		require.NoError(t, err, "query %d (%.6f, %.6f)", i, query.lat, query.lon)

		wantName, bestScore, secondScore := bruteForcePopulationRank(cities, query.lat, query.lon)
		trueDist := oracleHaversineKm(query.lat, query.lon, got.Latitude, got.Longitude)
		assert.InDelta(t, trueDist, gotDist, tieToleranceKm,
			"query %d (%.6f, %.6f): returned %q at %.6f km, haversine says %.6f km",
			i, query.lat, query.lon, got.Name, gotDist, trueDist)
		gotScore := float64(got.Population) / (trueDist*trueDist + 1.0)
		assert.GreaterOrEqual(t, gotScore, bestScore*(1-relativeScoreTolerance),
			"query %d (%.6f, %.6f): returned %q (score %.6f), oracle winner %q scores %.6f",
			i, query.lat, query.lon, got.Name, gotScore, wantName, bestScore)
		if secondScore < bestScore*(1-relativeScoreTolerance) {
			assert.Equal(t, wantName, got.Name,
				"query %d (%.6f, %.6f): unique gravity winner mismatch: got %q, want %q",
				i, query.lat, query.lon, got.Name, wantName)
		}
	}
}
