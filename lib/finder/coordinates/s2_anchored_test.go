package coordinates

import (
	"math"
	"math/rand"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/golang/geo/s2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anchoredOceanFixture builds a deterministic synthetic world of 200k cities
// clustered on seven continent-like blobs plus a small mid-Pacific island
// sprinkle, with a Zipf-like population ladder (37M down to ~1k, >topPopulationK
// positives so the top-K table is full and popK is a mid-tier population). It
// exists to benchmark and oracle-check the mid-ocean population-ranked query
// class: cheap discs certify nothing over open ocean, so the query must resolve
// through whatever strategy follows the 250 km disc.
func anchoredOceanFixture(t testing.TB) []city.SpatialCity {
	t.Helper()

	const totalCities = 200_000
	continentCenters := []struct{ lat, lon, latSpread, lonSpread, share float64 }{
		{45, -100, 12, 25, 0.16}, // North America
		{-15, -60, 10, 16, 0.10}, // South America
		{50, 15, 8, 18, 0.13},    // Europe
		{5, 20, 15, 20, 0.14},    // Africa
		{35, 90, 12, 25, 0.36},   // Asia
		{-25, 135, 8, 14, 0.09},  // Australia
		{80, 0, 6, 30, 0.02},     // High Arctic
	}

	rng := rand.New(rand.NewSource(20261005))
	cities := make([]city.SpatialCity, 0, totalCities)

	// The population ladder: a handful of megacities, a long tail of small
	// places. pop_i = max(1000, 37M / (i+1)^0.8); the 4096th entry lands near
	// 42k, a plausible popK. The first city of each continent-sized cluster
	// index gets the next ladder value, so megacities spread across continents.
	nextPopulation := func(i int) int32 {
		return int32(math.Max(1000, 37_000_000/math.Pow(float64(i+1), 0.8)))
	}

	remaining := totalCities
	for _, c := range continentCenters {
		n := int(float64(totalCities) * c.share)
		if n > remaining {
			n = remaining
		}
		for j := 0; j < n; j++ {
			cities = append(cities, city.SpatialCity{City: city.City{
				Name:       "Fixture City",
				Country:    "FC",
				Latitude:   math.Max(-89.9, math.Min(89.9, c.lat+rng.NormFloat64()*c.latSpread)),
				Longitude:  math.Max(-179.9, math.Min(179.9, c.lon+rng.NormFloat64()*c.lonSpread)),
				Population: nextPopulation(len(cities)),
			}})
		}
		remaining -= n
		if remaining <= 0 {
			break
		}
	}
	// Island sprinkle well inside the South Pacific gyre: the nearest land to
	// the benchmark query, mirroring the GeoNames norm of tiny island entries.
	for j := 0; j < remaining && j < 64; j++ {
		cities = append(cities, city.SpatialCity{City: city.City{
			Name:       "Fixture Atoll",
			Country:    "FC",
			Latitude:   -10 + rng.NormFloat64()*2,
			Longitude:  -135 + rng.NormFloat64()*2,
			Population: int32(1000 + rng.Int31n(4000)),
		}})
	}
	return cities
}

// anchoredOceanQueries returns the ocean-class queries for the fixture: the
// mid-Pacific gyre, the South Indian Ocean, and one populated control point.
func anchoredOceanQueries() []struct{ lat, lon float64 } {
	type q = struct{ lat, lon float64 }
	return []q{
		{0.0, -140.0}, // mid-Pacific gyre
		{-45.0, 80.0}, // South Indian Ocean
		{35.0, 100.0}, // populated control (Asia cluster)
	}
}

// TestAnchoredOceanFixtureMatchesBruteForce runs the population-rank oracle
// over the 200k synthetic world: every ocean-class query (and the populated
// control) must return the exact brute-force gravity winner.
func TestAnchoredOceanFixtureMatchesBruteForce(t *testing.T) {
	cities := anchoredOceanFixture(t)
	finder, err := BuildIndex(cities)
	require.NoError(t, err)
	require.Len(t, finder.topPopulations, topPopulationK, "fixture must fill the top-K table")

	const relativeScoreTolerance = 1e-9
	for i, query := range anchoredOceanQueries() {
		got, gotDist, err := finder.NearestPlace(query.lat, query.lon, RankPopulation)
		require.NoError(t, err, "query %d (%.6f, %.6f)", i, query.lat, query.lon)

		wantName, bestScore, secondScore := bruteForcePopulationRank(cities, query.lat, query.lon)
		trueDist := oracleHaversineKm(query.lat, query.lon, got.Latitude, got.Longitude)
		assert.InDelta(t, trueDist, gotDist, tieToleranceKm,
			"query %d (%.6f, %.6f): returned %q at %.6f km, haversine says %.6f km",
			i, query.lat, query.lon, got.Name, gotDist, trueDist)
		gotScore := float64(got.Population) / (trueDist*trueDist + 1.0)
		assert.GreaterOrEqual(t, gotScore, bestScore*(1-relativeScoreTolerance),
			"query %d (%.6f, %.6f): returned %q (pop %d, score %.6f), oracle winner %q scores %.6f",
			i, query.lat, query.lon, got.Name, got.Population, gotScore, wantName, bestScore)
		if secondScore < bestScore*(1-relativeScoreTolerance) {
			assert.Equal(t, wantName, got.Name,
				"query %d (%.6f, %.6f): unique gravity winner mismatch: got %q, want %q",
				i, query.lat, query.lon, got.Name, wantName)
		}
	}
}

// TestPopulationRankAnchoredMegacityAcrossOcean pins the anchored step on a
// small fixture whose brute-force winner is a top-K megacity an ocean away
// from the query: the nearby 250 km disc holds only scoreless ocean (a tiny
// island whose score cannot certify), so the strategy that follows the cheap
// tiers must find the distant megacity. The brute-force oracle verifies the
// winner; the observation hook verifies the query resolved at a data-derived
// anchored disc radius (not on the 1250/6250 escalation ladder, not the
// terminal unbounded scan).
func TestPopulationRankAnchoredMegacityAcrossOcean(t *testing.T) {
	cities := []city.SpatialCity{
		// A tiny island 111 km from the query: score 2000/(111^2+1) ~ 0.16,
		// far under every certification bound at 10/50/250 km.
		{City: city.City{Name: "Lonely Atoll", Latitude: 1.0, Longitude: -140.0, Population: 2_000}},
		// The brute-force winner: a 40M megacity ~5560 km away scoring
		// 40M/(5560^2+1) ~ 1.29, dwarfing the atoll.
		{City: city.City{Name: "Tokyo-Scale Megacity", Latitude: 35.0, Longitude: 140.0, Population: 40_000_000}},
		// A second megacity farther out: must lose to the nearer one.
		{City: city.City{Name: "Cairo-Scale Megacity", Latitude: 27.0, Longitude: 30.0, Population: 22_000_000}},
	}
	finder, err := BuildIndex(cities)
	require.NoError(t, err)

	const queryLat, queryLon = 0.0, -139.0
	result, radiusKm, err := finder.nearestByPopulation(
		s2.PointFromLatLng(s2.LatLngFromDegrees(queryLat, queryLon)))
	require.NoError(t, err)
	winnerIndex := int(result.EdgeID())
	require.GreaterOrEqual(t, winnerIndex, 0)
	require.Less(t, winnerIndex, len(finder.Cities))
	winner := finder.Cities[winnerIndex]

	// Exactness against the brute-force oracle.
	wantName, wantScore, _ := bruteForcePopulationRank(cities, queryLat, queryLon)
	assert.Equal(t, "Tokyo-Scale Megacity", winner.Name)
	assert.Equal(t, wantName, winner.Name, "anchored step returned a non-optimal city")
	distanceKm := result.Distance().Angle().Radians() * earthRadiusKm
	gotScore := float64(winner.Population) / (distanceKm*distanceKm + 1.0)
	assert.InDelta(t, wantScore, gotScore, wantScore*1e-9)

	// The anchored disc: radius derived from the megacity math (max of the
	// challenger radius and the winner's own distance, at least 250 km), NOT a
	// ladder value (1250, 6250) and NOT the terminal sentinel.
	assert.GreaterOrEqual(t, radiusKm, 250.0, "anchored disc cannot be smaller than the last cheap tier")
	assert.NotEqual(t, 1250.0, radiusKm, "must not resolve on the 1250 km escalation tier")
	assert.NotEqual(t, 6250.0, radiusKm, "must not resolve on the 6250 km escalation tier")
	assert.Less(t, radiusKm, maxSearchRadiusKm, "the terminal unbounded iteration must not run")

	// The public API agrees (it runs the same path).
	got, _, err := finder.NearestPlace(queryLat, queryLon, RankPopulation)
	require.NoError(t, err)
	assert.Equal(t, winner.Name, got.Name)
}

// TestPopulationRankAnchoredTieTableAndDiscCity exercises a bitwise score tie
// between a top-K table city and a crowded-out non-table city at identical
// coordinates: the table holds the topPopulationK largest populations with
// ties broken by city index ascending, so of two equal-population twins only
// the lower-indexed one enters the table. Both must be scored by whichever
// strategy runs, and the tie must resolve exactly like the brute-force oracle
// (smaller distance first, then lower city index) — here to the lower-indexed
// twin. The fixture keeps certification failing through the 250 km disc (the
// popK ceiling at 250 km tops the twins' score), forcing the anchored step.
func TestPopulationRankAnchoredTieTableAndDiscCity(t *testing.T) {
	const twinPopulation = int32(63_000_000)
	const twinLat, twinLon = 0.0, -137.0 // ~300 km from the query; outside the 250 km disc

	// 4095 far-away giants at the twin population fill the table; the twins
	// (one table, one crowded out) sit at identical coordinates near the query.
	cities := make([]city.SpatialCity, 0, topPopulationK+2)
	for i := 0; i < topPopulationK-1; i++ {
		cities = append(cities, city.SpatialCity{City: city.City{
			Name:       "Giant " + itoa3(i%1000) + "-" + itoa3(i/1000),
			Country:    "GI",
			Latitude:   60,
			Longitude:  120,
			Population: twinPopulation,
		}})
	}
	// Twin A (lower index) and Twin B (higher index) at the identical point.
	// The table's index-ascending tie-break admits Twin A and crowds out
	// Twin B, who is a non-table city.
	cities = append(cities, city.SpatialCity{City: city.City{
		Name: "Twin A", Country: "TW", Latitude: twinLat, Longitude: twinLon, Population: twinPopulation,
	}})
	cities = append(cities, city.SpatialCity{City: city.City{
		Name: "Twin B", Country: "TW", Latitude: twinLat, Longitude: twinLon, Population: twinPopulation,
	}})
	// A hamlet AT the query point keeps the cheap discs non-empty but unable
	// to certify: its score 500 stays under popK/(250^2+1) ~ 1008.
	cities = append(cities, city.SpatialCity{City: city.City{
		Name: "Ocean Hamlet", Country: "TW", Latitude: 0.0, Longitude: -140.0, Population: 500,
	}})

	finder, err := BuildIndex(cities)
	require.NoError(t, err)
	require.Len(t, finder.topPopulations, topPopulationK)
	assert.EqualValues(t, twinPopulation, finder.topPopulations[topPopulationK-1].population)

	const queryLat, queryLon = 0.0, -140.0
	result, radiusKm, err := finder.nearestByPopulation(
		s2.PointFromLatLng(s2.LatLngFromDegrees(queryLat, queryLon)))
	require.NoError(t, err)
	winner := finder.Cities[result.EdgeID()]

	// Deterministic tie resolution: identical coordinates and population make
	// the scores bitwise equal; the ordering resolves to the lower index,
	// exactly what bruteForcePopulationRank's (distance, index) sort produces.
	wantName, _, _ := bruteForcePopulationRank(cities, queryLat, queryLon)
	assert.Equal(t, "Twin A", winner.Name, "tie must resolve to the lower-indexed twin")
	assert.Equal(t, wantName, winner.Name, "tie resolution disagrees with the brute-force oracle")

	// The anchored step ran: a data-derived radius off the escalation ladder.
	assert.NotEqual(t, 1250.0, radiusKm)
	assert.NotEqual(t, 6250.0, radiusKm)
	assert.Less(t, radiusKm, maxSearchRadiusKm)

	got, _, err := finder.NearestPlace(queryLat, queryLon, RankPopulation)
	require.NoError(t, err)
	assert.Equal(t, winner.Name, got.Name)
}
