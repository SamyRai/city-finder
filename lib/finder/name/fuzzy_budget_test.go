package name

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setIndexBudget changes a built Finder's per-search posting budget. The
// budget lives on the immutable n-gram index, so swapping it takes the write
// lock that serializes against searches.
func setIndexBudget(f *Finder, budget int) {
	f.mutex.Lock()
	f.ngrams.budget = budget
	f.mutex.Unlock()
}

// budgetTestCorpus returns a corpus large enough that its posting walks span
// a wide range of entry counts (rare grams on the numbered names, ubiquitous
// grams on the shared-prefix names), so small budgets trip on some queries
// and not others.
func budgetTestCorpus() []string {
	corpus := append([]string{}, ngramCorpus()...)
	for i := 0; i < 2000; i++ {
		corpus = append(corpus, fmt.Sprintf("BudgCity%05d", i))
	}
	// A block of names sharing every gram class with short queries: the
	// sentinel-gram lists these queries walk are exactly this block plus the
	// numbered block's stragglers.
	for _, p := range []string{"sa", "mar", "val", "san", "lo", "be", "to", "pa"} {
		for i := 0; i < 200; i++ {
			corpus = append(corpus, fmt.Sprintf("%s%03d", p, i))
		}
	}
	return corpus
}

// TestFuzzyBudgetZeroReturnsEmpty pins the degenerate budget: zero allows no
// posting-entry work at all, so every walk over a non-empty index truncates
// immediately and returns no matches — deterministically, without error or
// panic. An empty query (no grams) still returns empty without truncating.
func TestFuzzyBudgetZeroReturnsEmpty(t *testing.T) {
	index, err := buildNGramIndex(ngramCorpus())
	require.NoError(t, err)
	index.budget = 0

	got, truncated := index.search("Paris", 1)
	assert.Empty(t, got, "budget 0 must surface no candidates")
	assert.True(t, truncated, "a non-empty walk under budget 0 must report truncation")

	got, truncated = index.search("", 2)
	assert.Empty(t, got)
	assert.False(t, truncated, "the empty query has no walk to truncate")
}

// TestFuzzyBudgetTinyIsDeterministicAndSound pins the tiny-budget contract:
// repeated identical searches return the identical partial result, every
// returned name is a verified true match (the cap can lose results, never
// fabricate them), and the partial result is a subset of the unlimited
// result.
func TestFuzzyBudgetTinyIsDeterministicAndSound(t *testing.T) {
	index, err := buildNGramIndex(budgetTestCorpus())
	require.NoError(t, err)

	queries := []string{"Paris", "Pars", "Londin", "Saint", "BudgCity00042", "sa01", "zzz"}
	for _, budget := range []int{1, 2, 7, 50, 500} {
		index.budget = budget
		for _, q := range queries {
			first, firstTrunc := index.search(q, 2)
			second, secondTrunc := index.search(q, 2)
			assert.Equal(t, first, second, "budget %d query %q: partial results must be deterministic", budget, q)
			assert.Equal(t, firstTrunc, secondTrunc, "budget %d query %q: truncation flag must be deterministic", budget, q)
			for _, m := range first {
				assert.LessOrEqualf(t, refLevenshtein(q, m), 2,
					"budget %d query %q: fabricated match %q", budget, q, m)
			}
		}
	}

	// Subset property against the unlimited walk, over a query sweep that
	// includes guaranteed trips (tiny budgets) and non-trips.
	index.budget = -1
	full := make(map[string][]string, len(queries))
	for _, q := range queries {
		matches, _ := index.search(q, 2)
		full[q] = matches
	}
	for _, budget := range []int{1, 7, 50} {
		index.budget = budget
		for _, q := range queries {
			partial, _ := index.search(q, 2)
			fullSet := make(map[string]struct{}, len(full[q]))
			for _, m := range full[q] {
				fullSet[m] = struct{}{}
			}
			for _, m := range partial {
				assert.Containsf(t, fullSet, m, "budget %d query %q: partial match %q not in unlimited result", budget, q, m)
			}
		}
	}
}

// TestFuzzyBudgetDefaultNeverTripsAtSmallScale asserts the default budget is
// far above small-scale walks: neither the corpus queries nor the package
// counter move.
func TestFuzzyBudgetDefaultNeverTripsAtSmallScale(t *testing.T) {
	require.Positive(t, DefaultFuzzyMaxCandidates, "default budget must be a positive cap")
	index, err := buildNGramIndex(budgetTestCorpus())
	require.NoError(t, err)

	before := index.stats.trips.Load()
	for _, q := range []string{"Paris", "Londinium", "BudgCity01234", "sa199", "zzz"} {
		for _, d := range []int{1, 2} {
			got, truncated := index.search(q, d)
			assert.Falsef(t, truncated, "search(%q, %d) must not trip the default budget", q, d)
			if q == "Paris" && d == 1 {
				assert.Contains(t, got, "Paris")
			}
		}
	}
	assert.Equal(t, before, index.stats.trips.Load(), "no default-budget search at this scale may count a trip")
}

// TestFuzzyBudgetTripCounterAndOneTimeLog pins the observability contract:
// every truncated search increments the Finder's trip counter exactly once, and
// the summary log fires exactly once for the first trip — never per query.
func TestFuzzyBudgetTripCounterAndOneTimeLog(t *testing.T) {
	index, err := buildNGramIndex(budgetTestCorpus())
	require.NoError(t, err)

	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	index.budget = 5
	before := index.stats.trips.Load()
	const trips = 10
	for i := 0; i < trips; i++ {
		_, truncated := index.search(fmt.Sprintf("BudgCity%05d", i), 2)
		require.Truef(t, truncated, "trip %d must truncate", i)
	}
	assert.Equal(t, before+trips, index.stats.trips.Load(), "one counter increment per truncated search")

	// The very first trip logged once; later trips stay silent.
	if n := strings.Count(logBuf.String(), "candidate budget"); n != 1 {
		t.Fatalf("budget trip must be logged exactly once, got %d lines in %q", n, logBuf.String())
	}

	// Untruncated searches do not count or log.
	index.budget = -1
	after := index.stats.trips.Load()
	index.search("BudgCity00001", 2)
	assert.Equal(t, after, index.stats.trips.Load())
	assert.Equal(t, 1, strings.Count(logBuf.String(), "candidate budget"))
}

// TestFuzzyBudgetExcludesTruncatedFromCache pins the cache-honesty rule at
// the Finder level: a truncated search still returns its partial candidates
// to the caller, but nothing lands in the fuzzy cache; once the budget allows
// the full walk, the same query is served complete and cached; and a cached
// complete entry keeps being served even after the budget drops again (cache
// reads never consult the budget).
func TestFuzzyBudgetExcludesTruncatedFromCache(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())
	// Force the lazy background build; WarmFuzzy does not touch the cache,
	// so no forcing lookup's cache entry can mask the behavior below.
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)

	finder.cacheMutex.RLock()
	baseCache := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()

	setIndexBudget(finder, 0)

	// The truncated d1 search returns partial (here: empty) candidates.
	got := finder.getCachedFuzzySearch("Pars", 1)
	assert.Nil(t, got, "budget 0 surfaces no candidates")
	finder.cacheMutex.RLock()
	cacheSize := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()
	assert.Equal(t, baseCache, cacheSize, "a truncated result must never enter the fuzzy cache")

	// Restore the full budget: the same query completes and caches.
	setIndexBudget(finder, -1)
	got = finder.getCachedFuzzySearch("Pars", 1)
	if assert.NotEmpty(t, got) {
		assert.Contains(t, got, "Paris")
	}
	finder.cacheMutex.RLock()
	_, cached := finder.fuzzyCache[fuzzyCacheKey{"Pars", 1}]
	finder.cacheMutex.RUnlock()
	assert.True(t, cached, "the complete result must be cached")

	// Budget back to zero: the cached complete entry is still served.
	setIndexBudget(finder, 0)
	got = finder.getCachedFuzzySearch("Pars", 1)
	if assert.NotEmpty(t, got, "cache reads must ignore the budget") {
		assert.Contains(t, got, "Paris")
	}
}

// TestFuzzyBudgetOverflowStillScannedWhenTruncated pins the scoping decision:
// the budget caps only the n-gram posting walk. The post-build overflow list
// is a small bounded linear scan, so names added after the build stay
// fuzzy-findable even while the walk is truncated.
func TestFuzzyBudgetOverflowStillScannedWhenTruncated(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())
	finder.WarmFuzzy() // force the lazy background build
	waitFuzzyBuilt(t, finder)

	finder.AddCity(city.SpatialCity{
		City: city.City{Name: "Berlin2", Country: "DE", Latitude: 52.52, Longitude: 13.41},
	})

	finder.cacheMutex.RLock()
	baseCache := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()

	setIndexBudget(finder, 0)
	got := finder.getCachedFuzzySearch("Berlin2", 0)
	assert.Contains(t, got, "Berlin2", "the overflow scan must run in full under a zero budget")

	finder.cacheMutex.RLock()
	cacheSize := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()
	// No forcing lookup ran (WarmFuzzy is cache-free), so the cache holds
	// only what it held before; the truncated query itself must add nothing.
	assert.Equal(t, baseCache, cacheSize, "an n-gram-truncated result stays out of the cache even with overflow hits")
}

// TestFuzzyBudgetConcurrentSearches races truncated searches against each
// other and against the trip counter and one-time log guard: the index is
// immutable, the budget is read once per search, and the shared state is
// exactly the atomics. Run under -race this pins that a search racing another
// is unchanged by the budget.
func TestFuzzyBudgetConcurrentSearches(t *testing.T) {
	index, err := buildNGramIndex(budgetTestCorpus())
	require.NoError(t, err)

	index.budget = 3
	before := index.stats.trips.Load()

	const workers = 8
	var wg sync.WaitGroup
	var trips atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				q := fmt.Sprintf("BudgCity%05d", (w*37+i)%2000)
				got, truncated := index.search(q, 2)
				for _, m := range got {
					if refLevenshtein(q, m) > 2 {
						panic("fabricated match under concurrency")
					}
				}
				if truncated {
					trips.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	// Every trip counted locally must be reflected globally; untruncated
	// searches may also have tripped if their walks exceeded the budget, so
	// the global counter only has a lower bound.
	assert.GreaterOrEqual(t, index.stats.trips.Load()-before, uint64(trips.Load()))
}

// TestFuzzyBudgetTripsArePerFinder pins counter ownership: a truncated search
// on one Finder moves only that Finder's FuzzyBudgetTrips.
func TestFuzzyBudgetTripsArePerFinder(t *testing.T) {
	tripped := BuildIndex(fuzzyFixtureCities())
	quiet := BuildIndex(fuzzyFixtureCities())
	for _, f := range []*Finder{tripped, quiet} {
		f.WarmFuzzy()
		waitFuzzyBuilt(t, f)
	}
	setIndexBudget(tripped, 0)

	require.Nil(t, tripped.getCachedFuzzySearch("Pars", 1))
	assert.EqualValues(t, 1, tripped.FuzzyBudgetTrips())
	assert.Equal(t, []string{"Paris"}, quiet.getCachedFuzzySearch("Pars", 1))
	assert.EqualValues(t, 0, quiet.FuzzyBudgetTrips())
}
