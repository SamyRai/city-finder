package name

import (
	"container/list"
	"strings"
	"time"
)

// fuzzyCacheKey identifies one cached fuzzy search. A struct key costs no
// formatting on the lookup path; the query is cloned on insert only (callers
// may pass strings backed by reused request buffers).
type fuzzyCacheKey struct {
	query       string
	maxDistance int
}

// fuzzySearchResult caches fuzzy search results to avoid repeated computations
type fuzzySearchResult struct {
	candidates []string
	timestamp  time.Time
	// gen is Finder.fuzzyGen as read before the search that produced this
	// entry; a hit requires it to still be current, so a name added since
	// (AddCity) is never hidden behind a cached answer.
	gen uint64
	// age is this entry's element in Finder.fuzzyCacheAge, which orders
	// entries oldest-first so eviction is O(1) instead of a full-map scan.
	age *list.Element
}

const (
	// fuzzyCacheTTL is how long a fuzzy result stays valid on read.
	fuzzyCacheTTL = time.Hour
	// maxFuzzyCacheEntries bounds the fuzzy result cache. It is keyed by raw
	// user input, so without a cap remote queries could grow it without
	// limit (the TTL only skips entries on read, it never evicts).
	maxFuzzyCacheEntries = 10000
)

// fuzzyCandidates returns the names within maxDistance of query, from the
// immutable n-gram index plus the post-build overflow list, and whether the
// result is partial (the n-gram walk hit the Options.FuzzyMaxCandidates cap). The
// caller must handle caching; this is the uncached core of
// getCachedFuzzySearch.
//
// The budget scopes to the n-gram walk only: the overflow list is a small
// linear scan bounded by the number of post-build AddCity names (no
// production callers today), so it runs in full even when the walk truncates
// — a truncated query still rescues typos of recently added names.
//
// The read lock is scoped around the candidate generation: AddCity mutates
// the overflow list and can swap nf.ngrams under the write lock, so an
// unlocked read would race. Concurrent readers — the common case at query
// time — do not block each other.
func (nf *Finder) fuzzyCandidates(query string, maxDistance int) ([]string, bool) {
	nf.mutex.RLock()
	candidates, truncated := nf.ngrams.search(query, maxDistance)
	overflow := nf.fuzzyOverflow
	nf.mutex.RUnlock()

	if len(overflow) == 0 {
		return candidates, truncated
	}
	// The overflow scan reuses the same banded, allocation-free checker the
	// n-gram verify step uses (volumes here are tiny, but one distance
	// implementation across the package keeps behavior uniform).
	var check levenshteinChecker
	check.prepare(query)
	for _, name := range overflow {
		if check.atMost(name, maxDistance) {
			candidates = append(candidates, name)
		}
	}
	return candidates, truncated
}

// getCachedFuzzySearch performs fuzzy search with caching. Every complete
// result enters the cache, empty ones included; truncated results
// (Options.FuzzyMaxCandidates tripped) are computed fresh on every call. An entry is
// served only while it is younger than fuzzyCacheTTL and no name was added
// since it was computed (fuzzyGen), so AddCity is visible on the very next
// search, cached query or not.
func (nf *Finder) getCachedFuzzySearch(query string, maxDistance int) []string {
	cacheKey := fuzzyCacheKey{query: query, maxDistance: maxDistance}
	// Read before the search: a name added while it runs leaves the entry
	// stale on arrival (conservative), never current while missing the name.
	gen := nf.fuzzyGen.Load()

	nf.cacheMutex.RLock()
	if cached, exists := nf.fuzzyCache[cacheKey]; exists && cached.gen == gen {
		// Check if cache is still valid (not too old)
		if time.Since(cached.timestamp) < fuzzyCacheTTL {
			nf.cacheMutex.RUnlock()
			return cached.candidates
		}
	}
	nf.cacheMutex.RUnlock()

	// Ensure the fuzzy index is built before searching. While a build is in
	// flight (or matching is disabled over the Options.FuzzyMaxNames threshold)
	// there is no index to search: return no candidates for this one request
	// and do NOT cache the empty result — the 1h TTL would pin it long past
	// the build completing.
	nf.ensureFuzzyBuilt()
	if nf.fuzzyState.Load() != fuzzyBuilt {
		return nil
	}

	// Search under the read lock: AddCity appends to the overflow list and
	// may swap the n-gram pointer under the write lock, so an unlocked
	// search would race with concurrent additions.
	candidates, truncated := nf.fuzzyCandidates(query, maxDistance)

	// Cache honesty: a budget-truncated result is partial for this query.
	// Never write it to the cache — the 1h TTL would pin it and serve it to
	// later identical queries as if complete. Exclusion (rather than a
	// truncated-tag on entries) is the simpler correct option: the only cost
	// is that a repeated degenerate query re-pays the (now budget-bounded)
	// search, and raising Options.FuzzyMaxCandidates immediately takes effect for
	// fresh searches instead of waiting out stale tagged entries.
	if truncated {
		return candidates
	}

	// Empty complete results are cached too: the generation check keeps a
	// cached miss from hiding a name AddCity adds later, so a repeated
	// no-hit query (the cheapest one to send) no longer re-walks every time.
	cacheKey.query = strings.Clone(query)
	nf.cacheMutex.Lock()
	nf.storeFuzzyCacheLocked(cacheKey, candidates, gen, time.Now())
	nf.cacheMutex.Unlock()

	return candidates
}

// storeFuzzyCacheLocked inserts (or replaces) one cache entry and keeps the
// cache bounded at maxFuzzyCacheEntries. The caller must hold cacheMutex for
// writing.
//
// Entries are never refreshed in place — a key is only (re)written after a
// miss — so insertion order is age order, and fuzzyCacheAge's front is always
// the oldest live entry. That makes both eviction rules O(1) per removal:
// expired entries are dropped from the front, then the oldest entry if the
// cache is still at its cap. The previous implementation scanned the whole
// map (twice) on every insert at capacity, which at the 10k cap cost more
// than the fuzzy search it was caching under a churning typo workload (see
// BenchmarkCityByNameFuzzy/cache-churn).
func (nf *Finder) storeFuzzyCacheLocked(key fuzzyCacheKey, candidates []string, gen uint64, now time.Time) {
	if prev, exists := nf.fuzzyCache[key]; exists {
		// An expired entry being recomputed: drop its stale age slot.
		nf.fuzzyCacheAge.Remove(prev.age)
		delete(nf.fuzzyCache, key)
	}
	for front := nf.fuzzyCacheAge.Front(); front != nil; front = nf.fuzzyCacheAge.Front() {
		oldestKey := front.Value.(fuzzyCacheKey)
		expired := now.Sub(nf.fuzzyCache[oldestKey].timestamp) >= fuzzyCacheTTL
		if !expired && len(nf.fuzzyCache) < maxFuzzyCacheEntries {
			break
		}
		nf.fuzzyCacheAge.Remove(front)
		delete(nf.fuzzyCache, oldestKey)
	}
	nf.fuzzyCache[key] = &fuzzySearchResult{
		candidates: candidates,
		timestamp:  now,
		gen:        gen,
		age:        nf.fuzzyCacheAge.PushBack(key),
	}
}
