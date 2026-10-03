package name

import (
	"runtime"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settledGoroutines returns the goroutine count once it has stopped moving,
// so goroutines still winding down from earlier tests do not skew a delta.
func settledGoroutines() int {
	n := runtime.NumGoroutine()
	for range 50 {
		time.Sleep(10 * time.Millisecond)
		m := runtime.NumGoroutine()
		if m == n {
			return n
		}
		n = m
	}
	return n
}

// TestEmptyFinderMissesSpawnNoGoroutines pins that a finder that never held a
// name does not start a fuzzy build per miss: the build would find nothing
// and settle straight back to not-built. CityByName already returns before
// the fuzzy machinery for a country without names, so the lookups that do
// reach it (the cached fuzzy search, and WarmFuzzy from an initializer that
// calls it repeatedly) are driven directly.
func TestEmptyFinderMissesSpawnNoGoroutines(t *testing.T) {
	nf := NewNameFinder()
	before := settledGoroutines()
	for range 1000 {
		assert.Nil(t, nf.CityByName("Nowhere", "XX"))
		assert.Nil(t, nf.getCachedFuzzySearch("Nowhere", 2))
		nf.WarmFuzzy()
	}
	assert.LessOrEqual(t, settledGoroutines(), before, "misses on an empty finder must not leave goroutines behind")
	assert.EqualValues(t, fuzzyNotBuilt, nf.FuzzyBuildState())
	assert.Zero(t, nf.fuzzyStats.snapshots.Load(), "no snapshot is taken for an empty index")
}

// TestFirstCityRearmsFuzzyBuildOnEmptyFinder pins the other side: the skip
// ends as soon as the finder holds a name.
func TestFirstCityRearmsFuzzyBuildOnEmptyFinder(t *testing.T) {
	nf := NewNameFinder()
	nf.WarmFuzzy()
	require.EqualValues(t, fuzzyNotBuilt, nf.FuzzyBuildState())

	nf.AddCity(city.SpatialCity{City: city.City{Name: "Paris", Country: "FR"}})
	waitFuzzyBuilt(t, nf)
	assert.NotNil(t, nf.CityByName("Parris", "FR"), "typo lookup answers once the index exists")
}
