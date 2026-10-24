package name

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFuzzyTruncatedWalkIsDeterministic pins the precondition for caching a
// budget-truncated result: for an unchanged index, the same query truncates at
// the same posting entry and returns the same partial matches, every time.
// The walk order is a total order (list length, then gram id) and the budget
// counts raw entries, so nothing in it depends on map order or timing.
func TestFuzzyTruncatedWalkIsDeterministic(t *testing.T) {
	index, err := buildNGramIndex(budgetTestCorpus())
	require.NoError(t, err)
	index.budget = 40

	for _, q := range []string{"BudgCity00042", "sa00", "Pars", "lo"} {
		for _, d := range []int{1, 2} {
			want, wantTrunc := index.search(q, d)
			for i := 0; i < 20; i++ {
				got, gotTrunc := index.search(q, d)
				assert.Equal(t, want, got, "query %q d=%d run %d", q, d, i)
				assert.Equal(t, wantTrunc, gotTrunc, "query %q d=%d run %d", q, d, i)
			}
		}
	}
}

// TestFuzzyTruncatedRepeatsHitCache is the CITY-12 repeat-cost regression: a
// query that trips the candidate budget used to be recomputed on every call
// (N identical calls = N full walks, 0 cache entries). It must be cached once,
// served from the cache afterwards, and every served answer must still count
// as a budget trip so the metric keeps meaning "truncated searches answered".
func TestFuzzyTruncatedRepeatsHitCache(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)

	finder.cacheMutex.RLock()
	base := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()

	setIndexBudget(finder, 0)
	const calls = 5
	for i := 0; i < calls; i++ {
		finder.getCachedFuzzySearch("Pars", 1)
	}

	finder.cacheMutex.RLock()
	added := len(finder.fuzzyCache) - base
	finder.cacheMutex.RUnlock()
	assert.Equal(t, 1, added, "a truncated result must be cached once")
	assert.EqualValues(t, calls, finder.FuzzyBudgetTrips(), "every truncated answer, cached or not, is a trip")
}
