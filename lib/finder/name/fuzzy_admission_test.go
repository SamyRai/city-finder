package name

import (
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedFinder builds the fuzzy fixture finder with the given walk limit and
// waits for its fuzzy index.
func gatedFinder(t *testing.T, limit int) *Finder {
	t.Helper()
	finder := BuildIndex(fuzzyFixtureCities(), Options{
		FuzzyMaxNames:      DefaultFuzzyMaxNames,
		FuzzyMaxCandidates: DefaultFuzzyMaxCandidates,
		FuzzyMaxConcurrent: limit,
	})
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)
	return finder
}

// TestFuzzyGateDegradesToExactOnly saturates the gate deterministically by
// holding every slot, and proves a typo lookup that would otherwise resolve
// degrades to the exact-only result (nil), is counted once, caches nothing,
// and resolves normally as soon as a slot frees.
func TestFuzzyGateDegradesToExactOnly(t *testing.T) {
	const limit = 2
	finder := gatedFinder(t, limit)

	for i := 0; i < limit; i++ {
		require.True(t, finder.fuzzyStats.acquireFuzzySlot(limit), "slot %d", i)
	}

	// Exact lookups never consult the gate.
	require.NotNil(t, finder.CityByName("Paris", "FR"))
	assert.Zero(t, finder.FuzzyGateRejections())

	assert.Nil(t, finder.CityByName("Pars", "FR"), "a saturated gate must degrade to exact-only")
	assert.EqualValues(t, 1, finder.FuzzyGateRejections(), "one lookup, one rejection (no second walk for d2)")
	finder.cacheMutex.RLock()
	cached := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()
	assert.Zero(t, cached, "a refused walk must not cache an empty answer")

	finder.fuzzyStats.releaseFuzzySlot(limit)
	got := finder.CityByName("Pars", "FR")
	require.NotNil(t, got, "a freed slot admits the lookup again")
	assert.Equal(t, "Paris", got.Name)
	assert.EqualValues(t, 1, finder.FuzzyGateRejections())
	assert.EqualValues(t, limit-1, finder.fuzzyStats.gateInflight.Load(), "the lookup released its own slot")
}

// TestFuzzyGateCacheHitsBypass proves a cached query is answered even with
// every slot held: only a walk needs admission.
func TestFuzzyGateCacheHitsBypass(t *testing.T) {
	const limit = 1
	finder := gatedFinder(t, limit)
	require.NotNil(t, finder.CityByName("Pars", "FR"), "warm the cache")

	require.True(t, finder.fuzzyStats.acquireFuzzySlot(limit))
	got := finder.CityByName("Pars", "FR")
	require.NotNil(t, got, "a cache hit needs no slot")
	assert.Equal(t, "Paris", got.Name)
	assert.Zero(t, finder.FuzzyGateRejections())
}

// TestFuzzyGateZeroIsUnlimited pins the Options contract: zero (and any
// literal that predates the field) means no admission limit, and
// DefaultOptions sets the limit to GOMAXPROCS.
func TestFuzzyGateZeroIsUnlimited(t *testing.T) {
	assert.Equal(t, runtime.GOMAXPROCS(0), DefaultOptions().FuzzyMaxConcurrent)

	for _, limit := range []int{0, -1} {
		var s fuzzyStats
		for i := 0; i < 1000; i++ {
			require.True(t, s.acquireFuzzySlot(limit))
		}
		assert.Zero(t, s.gateRejections.Load())
		assert.Zero(t, s.gateInflight.Load(), "unlimited gates track nothing")
	}
}

// TestFuzzyGateConcurrentLookups hammers a one-slot gate with distinct typo
// queries under -race: every lookup either runs or is counted as rejected,
// the in-flight count never leaks, and no answer is wrong.
func TestFuzzyGateConcurrentLookups(t *testing.T) {
	finder := gatedFinder(t, 1)

	const workers, perWorker = 8, 40
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// A unique query per call keeps every lookup a cache miss.
				c := finder.CityByName(fmt.Sprintf("Pars%d_%d", w, i), "FR")
				if c != nil && c.Name != "Paris" {
					panic("wrong city " + c.Name)
				}
			}
		}(w)
	}
	wg.Wait()

	assert.Zero(t, finder.fuzzyStats.gateInflight.Load(), "every acquired slot must be released")
	assert.LessOrEqual(t, finder.FuzzyGateRejections(), uint64(workers*perWorker))
}
