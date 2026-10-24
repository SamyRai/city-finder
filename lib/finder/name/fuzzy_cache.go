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
	// partial marks a result whose n-gram walk hit Options.FuzzyMaxCandidates.
	// The truncated walk is deterministic for an unchanged index, so the
	// partial answer is as repeatable as a complete one; the flag exists so a
	// hit can keep counting as a budget trip.
	partial   bool
	timestamp time.Time
	// bytes is this entry's estimated retained size (see fuzzyEntryBytes),
	// kept so eviction can subtract exactly what insertion added.
	bytes int
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
	// maxFuzzyCacheBytes bounds the cache's estimated retained size. The
	// entry cap alone does not: one complete d2 result for a short query can
	// hold tens of thousands of candidates, so 10k entries could pin gigabytes.
	// 64 MiB is ~2% of the 3 GiB process budget the index sizing targets
	// (docs/design/index-format-v2.md). Typical entries (a d1 result is a
	// handful of candidates, a few hundred bytes) never reach it, so ordinary
	// traffic stays bound by the entry cap and only pathologically large
	// results are bound by this one.
	maxFuzzyCacheBytes = 64 << 20
	// fuzzyEntryOverhead is the fixed per-entry cost: the map slot, the
	// fuzzySearchResult, the list element and the key's string header.
	// Deliberately rounded up; the estimate only needs to be proportional.
	fuzzyEntryOverhead = 192
	// fuzzyStringHeader is the per-candidate cost inside the retained slice.
	fuzzyStringHeader = 16
)

// fuzzyEntryBytes estimates the memory a cache entry keeps alive: the cloned
// query bytes, the fixed overhead, and one string header per slot of the
// candidate slice's capacity (the backing array is retained whole, not just
// its length). Candidate bytes are NOT counted: every candidate is a name
// already owned by the index (or the overflow list), so the cache shares those
// bytes instead of copying them.
func fuzzyEntryBytes(key fuzzyCacheKey, candidates []string) int {
	return len(key.query) + fuzzyEntryOverhead + fuzzyStringHeader*cap(candidates)
}

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

// getCachedFuzzySearch is the result-only form of getCachedFuzzySearchGated
// for callers that do not care whether the admission gate refused the walk
// (a refusal reads as no candidates).
func (nf *Finder) getCachedFuzzySearch(query string, maxDistance int) []string {
	candidates, _ := nf.getCachedFuzzySearchGated(query, maxDistance)
	return candidates
}

// getCachedFuzzySearchGated performs fuzzy search with caching. Every result
// enters the cache, empty ones and budget-truncated (partial) ones included.
// An entry is served only while it is younger than fuzzyCacheTTL and no name
// was added since it was computed (fuzzyGen), so AddCity is visible on the
// very next search, cached query or not. A cache hit never takes a gate slot.
//
// admitted is false only when the cache missed and the
// Options.FuzzyMaxConcurrent gate refused the walk; the result is then nil
// and nothing is cached, so the query is searched normally once a slot frees.
func (nf *Finder) getCachedFuzzySearchGated(query string, maxDistance int) (candidates []string, admitted bool) {
	cacheKey := fuzzyCacheKey{query: query, maxDistance: maxDistance}
	// Read before the search: a name added while it runs leaves the entry
	// stale on arrival (conservative), never current while missing the name.
	gen := nf.fuzzyGen.Load()

	nf.cacheMutex.RLock()
	if cached, exists := nf.fuzzyCache[cacheKey]; exists && cached.gen == gen {
		// Check if cache is still valid (not too old)
		if time.Since(cached.timestamp) < fuzzyCacheTTL {
			nf.cacheMutex.RUnlock()
			if cached.partial {
				// A partial hit is still a budget-truncated answer: keep the
				// metric counting answers, not walks. The one-time log
				// already fired on the walk that populated the entry.
				nf.fuzzyStats.trips.Add(1)
			}
			return cached.candidates, true
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
		return nil, true
	}

	// Admission: a walk is the expensive part. Over the limit, refuse rather
	// than queue (see Options.FuzzyMaxConcurrent); the caller degrades to
	// exact-only.
	limit := nf.opts.FuzzyMaxConcurrent
	if !nf.fuzzyStats.acquireFuzzySlot(limit) {
		return nil, false
	}
	defer nf.fuzzyStats.releaseFuzzySlot(limit)

	// Search under the read lock: AddCity appends to the overflow list and
	// may swap the n-gram pointer under the write lock, so an unlocked
	// search would race with concurrent additions.
	candidates, truncated := nf.fuzzyCandidates(query, maxDistance)

	// Cache honesty: a budget-truncated result IS cached, but flagged partial
	// and still counted as a trip on every hit. The truncated walk is
	// deterministic for an unchanged index (see Options.FuzzyMaxCandidates),
	// so the partial answer is the one a fresh walk would return again; caching
	// it stops a degenerate query from re-paying the full budget on every
	// repeat. The generation check still drops it when a name is added.

	// Empty complete results are cached too: the generation check keeps a
	// cached miss from hiding a name AddCity adds later, so a repeated
	// no-hit query (the cheapest one to send) no longer re-walks every time.
	cacheKey.query = strings.Clone(query)
	nf.cacheMutex.Lock()
	nf.storeFuzzyCacheLocked(cacheKey, candidates, truncated, gen, time.Now())
	nf.cacheMutex.Unlock()

	return candidates, true
}

// storeFuzzyCacheLocked inserts (or replaces) one cache entry and keeps the
// cache bounded by both maxFuzzyCacheEntries and maxFuzzyCacheBytes. The
// caller must hold cacheMutex for writing.
//
// Entries are never refreshed in place — a key is only (re)written after a
// miss — so insertion order is age order, and fuzzyCacheAge's front is always
// the oldest live entry. That makes every eviction rule O(1) per removal:
// expired entries are dropped from the front, then the oldest entry while the
// cache is at its entry cap or the new entry would push it past the byte
// budget. The previous implementation scanned the whole map (twice) on every
// insert at capacity, which at the 10k cap cost more than the fuzzy search it
// was caching under a churning typo workload (see
// BenchmarkCityByNameFuzzy/cache-churn).
//
// An entry that alone exceeds the byte budget is not stored: admitting it
// would evict the entire cache for one answer that is itself over budget.
func (nf *Finder) storeFuzzyCacheLocked(key fuzzyCacheKey, candidates []string, partial bool, gen uint64, now time.Time) {
	if prev, exists := nf.fuzzyCache[key]; exists {
		// An expired entry being recomputed: drop its stale age slot.
		nf.removeFuzzyCacheEntryLocked(key, prev)
	}
	size := fuzzyEntryBytes(key, candidates)
	if size > maxFuzzyCacheBytes {
		return
	}
	for front := nf.fuzzyCacheAge.Front(); front != nil; front = nf.fuzzyCacheAge.Front() {
		oldestKey := front.Value.(fuzzyCacheKey)
		oldest := nf.fuzzyCache[oldestKey]
		expired := now.Sub(oldest.timestamp) >= fuzzyCacheTTL
		if !expired && len(nf.fuzzyCache) < maxFuzzyCacheEntries && nf.fuzzyCacheBytes+size <= maxFuzzyCacheBytes {
			break
		}
		nf.removeFuzzyCacheEntryLocked(oldestKey, oldest)
	}
	nf.fuzzyCache[key] = &fuzzySearchResult{
		candidates: candidates,
		partial:    partial,
		timestamp:  now,
		gen:        gen,
		bytes:      size,
		age:        nf.fuzzyCacheAge.PushBack(key),
	}
	nf.fuzzyCacheBytes += size
}

// removeFuzzyCacheEntryLocked drops one entry from the map, the age list and
// the byte total together, so the three never drift. The caller must hold
// cacheMutex for writing.
func (nf *Finder) removeFuzzyCacheEntryLocked(key fuzzyCacheKey, entry *fuzzySearchResult) {
	nf.fuzzyCacheAge.Remove(entry.age)
	delete(nf.fuzzyCache, key)
	nf.fuzzyCacheBytes -= entry.bytes
}
