package coordinates

import (
	"fmt"
	"testing"
)

// BenchmarkNearestPlaceWithAdmin measures the include=admin read path
// (NearestPlaceWithAdmin) over the same 100K distinct-point fixture as
// BenchmarkNearestPlaceDistinctPoints, with admin codes on every city so the
// attribution pays its real table lookups and a small Admin1Names map so the
// name resolution hits. The delta against the distance-ranked distinct-points
// benchmark is the attribution overhead measured in production terms (~0 at
// full scale, v1.1 validation).
//
// The population sub-run covers the land-shaped population query on a
// uniformly populated world (every city populated, so the escalation ladder
// certifies early) — the complement to BenchmarkNearestByPopulationOceanQuery,
// which measures the far-from-land worst case.
func BenchmarkNearestPlaceWithAdmin(b *testing.B) {
	cities := generateDistinctCities(100000)
	names := make(map[string]string)
	for i := range cities {
		cities[i].Admin1Code = fmt.Sprintf("%02d", i%100)
		cities[i].Admin2Code = fmt.Sprintf("%03d", i%1000)
		cities[i].Population = int32(1000 + i%90000)
		names[fmt.Sprintf("TC.%02d", i%100)] = fmt.Sprintf("Region %d", i%100)
	}
	finder, err := BuildIndex(cities)
	if err != nil {
		b.Fatal(err)
	}
	finder.Admin1Names = names

	points := []struct{ lat, lon float64 }{
		{37.7749, -122.4194}, // San Francisco
		{40.7128, -74.0060},  // New York
		{51.5074, -0.1278},   // London
	}

	for _, mode := range []struct {
		name string
		rank Rank
	}{
		{"distance", RankDistance},
		{"population", RankPopulation},
	} {
		b.Run(mode.name, func(b *testing.B) {
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				p := points[i%len(points)]
				_, _, admin, err := finder.NearestPlaceWithAdmin(p.lat, p.lon, mode.rank)
				if err != nil {
					b.Fatalf("lookup failed: %v", err)
				}
				if admin.Admin1Code == "" {
					b.Fatal("attribution must resolve on this fixture")
				}
				i++
			}
		})
	}
}
