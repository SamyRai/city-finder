package name

import "log"

// FuzzyMaxNames is the maximum number of (country, name) keys an index may
// contain before the lazy n-gram fuzzy build is refused. Over it, fuzzy
// matching is disabled for that Finder's lifetime and lookups degrade to
// exact-only. The gate exists because the build is O(total name runes) and
// leaves a >1 GiB resident structure at prod scale — a bound so the
// initializer can never be surprised by an index far larger than anything
// measured.
//
// The gate counts total (country, name) keys — the sum of the per-country
// sorted-table name counts plus the overflow keys — not unique names. That
// over-approximates the distinct-name set (a name indexed in N countries
// counts N times), which is intentional: the count is O(#countries) with
// zero allocations, and fuzzy matching is a best-effort enhancement, never a
// correctness requirement.
//
// Default rationale (measured on Apple silicon, Oct 2026 GeoNames): at the
// prod scale of 18,698,093 keys / 17,727,652 distinct names the lazy
// n-gram build takes ~94 s (lock-free, in a background goroutine — the
// triggering lookup returns exact-only rather than waiting it out), the
// structure holds 1.18 GiB resident, and typo queries run at
// distance-1 p50 2.6 ms and distance-2 p50 20.6 ms / p99 749 ms — inside
// the <50 ms p50 target with memory under the 3 GiB budget. The default
// therefore sits above prod with ~34% key headroom. See
// docs/design/index-format-v2.md for the full scale table.
//
// Override it BEFORE the first fuzzy lookup on any Finder: the decision is
// evaluated once per index and is terminal, and the variable is read without
// synchronization.
var FuzzyMaxNames = 25_000_000

// FuzzyMaxCandidates caps the posting-list work a single fuzzy search may
// perform: the number of posting entries the rarest-lists walk may read
// before it stops and returns the results verified so far, best-effort, for
// that one query. It is the v1.1 clamp on the fuzzy tail: at prod scale
// (17.73M names), distance-2 queries whose length/gram filters degenerate —
// short or common-gram queries — were measured walking multi-million-entry
// posting lists for up to ~8 s; the budget bounds that walk per query (the
// default to a few seconds at prod, tighter budgets to tens of
// milliseconds — see the measured sweep in docs/design/index-format-v2.md).
//
// Semantics when the cap trips mid-query: no error, no panic — the search
// returns the matches already verified (every returned name is a true
// Levenshtein match; the cap can only lose results, never fabricate them),
// the result is excluded from the fuzzy cache so it is never served to a
// later query as if complete, and FuzzyBudgetTrips() increments. Exact
// (phase-1) lookups never consult this path and are unaffected.
//
// Default rationale (Apple silicon, Oct 2026 GeoNames, 17.73M names; the
// standard typo workload = 1k real names with 1–2 edits, the adversarial
// workload = 1k one-to-three-rune queries): the standard workload's largest
// posting walk is 3,539,399–3,632,442 entries across two independent 1k
// samples (d2; ~531k at d1) while the adversarial workload's is 4,000,118 —
// the two tails overlap, so no budget can both keep the standard workload
// 100% complete AND clamp the adversarial p99 to ~100 ms; a ~500k budget
// measured 415 ms adversarial d2 p99 but truncated 7.1% of standard d2
// queries. The default sits above the standard workload's observed maximum
// (0/1000 truncated at both distances, in both samples) and is deliberately
// tight rather than generous: every trip is loudly observable via
// FuzzyBudgetTrips() and the one-time log, so an operator with a heavier
// typo workload can raise it on evidence, while a generous default would
// silently weaken the only bound on degenerate queries. Its value at
// today's data is the worst-case guarantee — no fuzzy query exceeds 4M
// posting entries of work — not a p99 improvement; see
// docs/design/index-format-v2.md for the full measured budget sweep. At
// corpora of 1M names and below the cap is dormant (largest measured walk
// 225,469 entries).
//
// Override it BEFORE concurrent fuzzy searches run: the variable is read
// once per search without synchronization, identical to FuzzyMaxNames.
// Negative disables the cap (v1.0 behavior: unbounded walk, full
// completeness up to the q-gram boundary, unbounded tail).
var FuzzyMaxCandidates = 4_000_000

// fuzzyState values track the lazy fuzzy index. The state moves not-built ->
// building -> built, or not-built -> disabled once the FuzzyMaxNames gate
// trips. Both terminal states are sticky for the Finder's lifetime, with one
// exception: a build over an empty index yields no structure and returns to
// not-built so later AddCity growth can trigger a fresh build.
const (
	fuzzyNotBuilt int32 = iota // zero value: no build has run yet
	fuzzyBuilding              // one goroutine is building the n-gram index lock-free
	fuzzyBuilt                 // n-gram index is built and searchable
	fuzzyDisabled              // index over FuzzyMaxNames; exact-only for life
)

// ensureFuzzyBuilt lazily brings the fuzzy index toward a terminal state:
// built, or disabled when the index exceeds FuzzyMaxNames. It is safe to
// call from any lookup path — the common case is one atomic load — and no
// caller ever waits on a build: the goroutine that wins the fuzzyNotBuilt ->
// fuzzyBuilding CAS only ARRANGES the work, spawning buildFuzzyIndex in a
// background goroutine and returning immediately. The triggering lookup then
// takes the same degraded exact-only path concurrent lookups always did
// (previously it blocked for the full 30-90 s in-request build), and every
// later fuzzy lookup observes the finished index once the build lands.
//
// The build itself deliberately holds NO lock. Only the name snapshot (RLock)
// and the final pointer swap (Lock, microseconds) take the mutex, so exact
// lookups keep flowing while the n-gram index is under construction.
func (nf *Finder) ensureFuzzyBuilt() {
	switch nf.fuzzyState.Load() {
	case fuzzyBuilt, fuzzyDisabled, fuzzyBuilding:
		return
	}

	// Snapshot under the read lock: the key total gates the build, the name
	// list seeds it, and both must come from one consistent view of the index.
	nf.mutex.RLock()
	totalKeys := nf.totalIndexKeys()
	if totalKeys > FuzzyMaxNames {
		nf.mutex.RUnlock()
		// Terminal, logged exactly once by the goroutine that flips the state.
		if nf.fuzzyState.CompareAndSwap(fuzzyNotBuilt, fuzzyDisabled) {
			log.Printf("name index has %d keys over fuzzy threshold %d; fuzzy matching disabled (exact-only) — set name.FuzzyMaxNames to override",
				totalKeys, FuzzyMaxNames)
		}
		return
	}
	names := nf.namesFromIndex()
	nf.mutex.RUnlock()

	if !nf.fuzzyState.CompareAndSwap(fuzzyNotBuilt, fuzzyBuilding) {
		return // lost the race to another builder; it publishes the index
	}

	// Spawn and return: the goroutine owns the state machine from here
	// (building -> built/notBuilt/disabled), and no lookup path ever blocks
	// on it. A discarded build resets to fuzzyNotBuilt and is retried by the
	// next ensureFuzzyBuilt call — WarmFuzzy gives initializers an explicit
	// way to trigger that retry loop.
	go nf.buildFuzzyIndex(names, totalKeys)
}

// buildFuzzyIndex is the background half of ensureFuzzyBuilt; it runs in its
// own goroutine on the snapshot (names, totalKeys) the triggering caller
// took. The goroutine is owned by the Finder, terminates on its own after
// exactly one build, and never holds nf.mutex across the construction.
//
// Lock-free construction of an immutable structure: readers can neither
// observe a half-built index nor be blocked by the build.
func (nf *Finder) buildFuzzyIndex(names []string, totalKeys int) {
	index, err := buildNGramIndex(names)
	if err != nil {
		// Terminal disable, not a retry: the corpus cannot be indexed within
		// int32 CSR offsets, so every rebuild would fail identically. Mirrors
		// the FuzzyMaxNames disable above — exact-only from here on. Only the
		// goroutine that won the fuzzyBuilding CAS reaches this point, so the
		// log fires exactly once.
		nf.fuzzyState.Store(fuzzyDisabled)
		log.Printf("fuzzy n-gram index build failed; fuzzy matching disabled (exact-only): %v", err)
		return
	}

	// Commit under the write lock. The index only ever grows — every
	// insertion path appends under this same lock, nothing removes — so an
	// unchanged key total since the snapshot means the name list is still
	// complete and the swap cannot drop a concurrently added name. If
	// AddCity did land mid-build, discard this structure and reset: the
	// next fuzzy attempt rebuilds from the now-larger index.
	nf.mutex.Lock()
	committed := nf.totalIndexKeys() == totalKeys
	if committed {
		nf.ngrams = index
		nf.fuzzyOverflow = nil // the snapshot already covered these names
	}
	nf.mutex.Unlock()

	// Mark built only when the structure actually holds names. An empty
	// index legitimately stays unbuilt so later AddCity growth can trigger a
	// build.
	if committed && len(names) > 0 {
		nf.fuzzyState.Store(fuzzyBuilt)
	} else {
		// Uncommitted (AddCity raced the snapshot) or empty index: the next
		// fuzzy attempt retries with fresher data.
		nf.fuzzyState.Store(fuzzyNotBuilt)
	}
}

// FuzzyBuildState reports the fuzzy index state for operators: 0 = not
// built, 1 = building, 2 = built, 3 = disabled (corpus over FuzzyMaxNames,
// or the n-gram build refused the corpus). Intended for health/metrics
// surfaces; the numeric values mirror the unexported state constants.
func (nf *Finder) FuzzyBuildState() int32 {
	return nf.fuzzyState.Load()
}

// WarmFuzzy triggers the lazy fuzzy (n-gram) index build in the background
// without blocking the caller. It is idempotent and safe to call from any
// state: with the index already built, building, or disabled it is a no-op
// (one atomic load), and on a fresh index it snapshots the inverted index
// and hands the construction to a background goroutine (see
// ensureFuzzyBuilt for the state machine).
//
// Call it once after initialization so the first user typo query does not
// fall into the degraded exact-only window: until the background build
// lands, fuzzy lookups return exact-only results. At production scale
// (Oct 2026 GeoNames, ~18.7M keys) the build takes ~94 s single-threaded
// and the finished structure adds ~1.2 GiB resident on top of the inverted
// index — schedule the warm-up (and the memory) accordingly.
func (nf *Finder) WarmFuzzy() {
	nf.ensureFuzzyBuilt()
}
