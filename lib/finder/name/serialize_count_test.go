package name

import (
	"fmt"
	"io"
	"log"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/indexfile"
	"github.com/stretchr/testify/require"
)

// countFixture builds n cities spread over countries country codes.
// dupNames collapses the names to that many distinct values (0 = all unique),
// alts adds that many alt names per city.
func countFixture(n, countries, dupNames, alts int) []city.SpatialCity {
	out := make([]city.SpatialCity, n)
	for i := range out {
		id := i
		if dupNames > 0 {
			id = i % dupNames
		}
		sc := city.SpatialCity{City: city.City{
			Name:       fmt.Sprintf("Springfield Town %d", id),
			Country:    fmt.Sprintf("C%02d", i%countries),
			Latitude:   float64(i%160) - 80,
			Longitude:  float64(i%340) - 170,
			Population: int32(i),
		}}
		for a := 0; a < alts; a++ {
			sc.AltNames = append(sc.AltNames, fmt.Sprintf("Alt %d of %d", a, i))
		}
		out[i] = sc
	}
	return out
}

// payloadStats serializes cities (optionally sharing the city table), reads
// the file back through indexfile and returns the header count with the
// decompressed payload and file sizes.
func payloadStats(t *testing.T, cities []city.SpatialCity, shared bool) (count int, stats indexfile.Stats) {
	t.Helper()
	f := BuildIndex(cities)
	if shared {
		table := make([]city.City, len(cities))
		for i := range cities {
			table[i] = cities[i].City
		}
		require.NoError(t, f.ShareCities(table))
	}
	r, err := indexfile.Open(serializeToTemp(t, f))
	require.NoError(t, err)
	defer func() { _ = r.Close() }()
	var h indexHeader
	require.NoError(t, r.Header(&h))
	var p nameIndexPayloadV3
	require.NoError(t, r.Payload(&p))
	return h.Count, r.Stats()
}

// TestHeaderCountDoesNotBoundPayload documents why the name index declares no
// indexfile.EntryBounds (CITY-35). Its header Count is the number of
// COUNTRIES (len(Refs)), not names or cities, while the payload scales with
// the city table and the id lists. Decompressed bytes per header entry span
// orders of magnitude with the country count of the same data, so a per-entry
// byte budget (the S2/postal approach) would either reject a legitimate
// one-country index or, if loose enough to admit it, bound nothing for the
// usual ~250-country index. The test fails if the header ever starts to
// describe the payload, which is the moment to revisit the decision.
func TestHeaderCountDoesNotBoundPayload(t *testing.T) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(old) })

	const n = 60_000
	cases := []struct {
		name      string
		cities    []city.SpatialCity
		countries int
		shared    bool
	}{
		{"unique/250 countries", countFixture(n, 250, 0, 0), 250, false},
		{"unique/1 country", countFixture(n, 1, 0, 0), 1, false},
		{"duplicate names/1 country", countFixture(n, 1, 50, 0), 1, false},
		{"alt names/1 country", countFixture(n, 1, 0, 3), 1, false},
		{"shared table/1 country", countFixture(n, 1, 0, 0), 1, true},
	}
	perEntry := map[string]int64{}
	for _, tc := range cases {
		count, stats := payloadStats(t, tc.cities, tc.shared)
		require.Equal(t, tc.countries, count, "%s: header Count is the country count", tc.name)
		perEntry[tc.name] = stats.PayloadBytes / int64(count)
		t.Logf("%s: count=%d payload=%d B file=%d B => %d payload B per header entry, %.2f header entries per file byte",
			tc.name, count, stats.PayloadBytes, stats.FileBytes, perEntry[tc.name], float64(count)/float64(stats.FileBytes))
	}

	// Same 60k unique cities, one country vs 250: payload per header entry
	// differs by orders of magnitude and the one-country case is far past the
	// 2 KiB per entry the other indexes budget.
	one, many := perEntry["unique/1 country"], perEntry["unique/250 countries"]
	require.Greater(t, one, int64(2<<10), "a legitimate one-country index exceeds a 2 KiB/entry budget")
	require.Greater(t, one/many, int64(50), "payload per header entry is not stable across equivalent data")
}
