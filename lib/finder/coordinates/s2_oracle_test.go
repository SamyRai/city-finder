package coordinates

import (
	"math"
	"math/rand"
	"os"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
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
		got, gotDist, err := finder.NearestPlace(query.lat, query.lon)
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
