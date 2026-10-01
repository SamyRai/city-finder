package name

import (
	"fmt"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
)

// TestCityByNamePhases exercises the three lookup phases and the country
// filter end to end: phase 1 exact inverted-index hit, phase 2 fuzzy with
// Levenshtein distance 1, phase 3 fuzzy with distance 2, and the per-country
// resolution that rejects candidates indexed under other countries.
func TestCityByNamePhases(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities()) // Paris/FR, London/GB, Berlin/DE, Tokyo/JP, Madrid/ES
	// Fuzzy phases need the built index; warm up (background build) first.
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)

	wantName := func(s string) *string { return &s }
	cases := []struct {
		desc          string
		name, country string
		want          *string // nil means CityByName must return nil
	}{
		{"phase 1 exact match", "Paris", "FR", wantName("Paris")},
		{"phase 1 exact match other country", "London", "GB", wantName("London")},
		{"phase 1 miss on unknown name", "Atlantis", "FR", nil},
		{"phase 2 resolves a distance-1 typo", "Pars", "FR", wantName("Paris")},
		{"phase 2 resolves a case-only typo (distance 1)", "paris", "FR", wantName("Paris")},
		{"phase 3 resolves a distance-2 typo", "Paars", "FR", wantName("Paris")},
		{"phase 3 resolves a distance-2 deletion typo", "Prs", "FR", wantName("Paris")},
		{"distance-3 typo is beyond fuzzy reach", "Pariissxx", "FR", nil},
		{"country filter rejects a distance-1 candidate from another country", "Pars", "DE", nil},
		{"country filter rejects an exact name from another country", "London", "FR", nil},
		{"unknown country code", "Paris", "XX", nil},
		{"empty country code", "Paris", "", nil},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := finder.CityByName(tc.name, tc.country)
			if tc.want == nil {
				assert.Nil(t, got, "CityByName(%q, %q) must not resolve", tc.name, tc.country)
				return
			}
			if !assert.NotNil(t, got, "CityByName(%q, %q) must resolve", tc.name, tc.country) {
				return
			}
			assert.Equal(t, *tc.want, got.Name)
		})
	}
}

// TestFuzzyCacheHitMissExpiry covers the fuzzy result cache lifecycle: a cold
// query populates the cache, a warm query is served from the cache without
// recomputation, and an entry older than fuzzyCacheTTL is recomputed.
func TestFuzzyCacheHitMissExpiry(t *testing.T) {
	finder := NewNameFinder()
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}})
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)

	// Miss: the cold query computes candidates and caches them.
	got := finder.getCachedFuzzySearch("Pars", 1)
	assert.Contains(t, got, "Paris")

	finder.cacheMutex.RLock()
	entry, cached := finder.fuzzyCache["Pars_1"]
	finder.cacheMutex.RUnlock()
	if !assert.True(t, cached, "cold lookup must populate the cache") {
		return
	}

	// Hit: a poisoned cached value must be served verbatim (no recompute).
	finder.cacheMutex.Lock()
	entry.candidates = []string{"CACHE-HIT-SENTINEL"}
	finder.cacheMutex.Unlock()
	got = finder.getCachedFuzzySearch("Pars", 1)
	assert.Equal(t, []string{"CACHE-HIT-SENTINEL"}, got, "warm lookup must be served from the cache")

	// Expiry: an entry older than the TTL is recomputed, not served.
	finder.cacheMutex.Lock()
	entry.timestamp = time.Now().Add(-fuzzyCacheTTL - time.Second)
	finder.cacheMutex.Unlock()
	got = finder.getCachedFuzzySearch("Pars", 1)
	assert.NotEqual(t, []string{"CACHE-HIT-SENTINEL"}, got, "expired entry must be recomputed")
	assert.Contains(t, got, "Paris")
}

// TestFuzzyCacheEvictionAtCapOldest drives the cache to its cap with fresh
// entries and then issues one more distinct query. Nothing has expired at
// that point, so eviction must fall through to dropping the oldest entry
// while the cache stays bounded at the cap. Every query must MATCH an
// indexed name: empty results are not cached (a later AddCity must become
// visible to the same query), so only matching fillers exercise eviction.
func TestFuzzyCacheEvictionAtCapOldest(t *testing.T) {
	cities := make([]city.SpatialCity, 0, maxFuzzyCacheEntries+1)
	for i := 0; i < maxFuzzyCacheEntries; i++ {
		cities = append(cities, city.SpatialCity{
			City: city.City{Name: fmt.Sprintf("fill%05d", i), Country: "FC", Latitude: 1, Longitude: 1},
		})
	}
	cities = append(cities, city.SpatialCity{City: city.City{Name: "overflow", Country: "FC", Latitude: 1, Longitude: 1}})
	finder := BuildIndex(cities)
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)

	for i := 0; i < maxFuzzyCacheEntries; i++ {
		finder.getCachedFuzzySearch(fmt.Sprintf("fill%05d", i), 1)
	}

	finder.cacheMutex.RLock()
	size := len(finder.fuzzyCache)
	_, oldestPresent := finder.fuzzyCache["fill00000_1"]
	finder.cacheMutex.RUnlock()
	assert.Equal(t, maxFuzzyCacheEntries, size, "cache must reach but not exceed the cap")
	assert.True(t, oldestPresent, "first inserted entry must still be present before overflow")

	finder.getCachedFuzzySearch("overflow", 1)

	finder.cacheMutex.RLock()
	size = len(finder.fuzzyCache)
	_, oldestPresent = finder.fuzzyCache["fill00000_1"]
	_, newestPresent := finder.fuzzyCache["overflow_1"]
	finder.cacheMutex.RUnlock()
	assert.Equal(t, maxFuzzyCacheEntries, size, "cache must stay at the cap after overflow")
	assert.False(t, oldestPresent, "oldest entry must be evicted when no entry is expired")
	assert.True(t, newestPresent, "the overflowing entry must be cached")
}
