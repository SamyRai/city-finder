package name

import "log"

// fuzzyState values track the lazy fuzzy index. The state moves not-built ->
// building -> built, or not-built -> disabled once the Options.FuzzyMaxNames gate
// trips. Both terminal states are sticky for the Finder's lifetime, with one
// exception: a build over an empty index yields no structure and returns to
// not-built so later AddCity growth can trigger a fresh build.
const (
	fuzzyNotBuilt int32 = iota // zero value: no build has run yet
	fuzzyBuilding              // one goroutine is building the n-gram index lock-free
	fuzzyBuilt                 // n-gram index is built and searchable
	fuzzyDisabled              // index over Options.FuzzyMaxNames; exact-only for life
)

// ensureFuzzyBuilt lazily brings the fuzzy index toward a terminal state:
// built, or disabled when the index exceeds Options.FuzzyMaxNames. It is safe to
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
	if totalKeys > nf.opts.FuzzyMaxNames {
		nf.mutex.RUnlock()
		// Terminal, logged exactly once by the goroutine that flips the state.
		if nf.fuzzyState.CompareAndSwap(fuzzyNotBuilt, fuzzyDisabled) {
			log.Printf("name index has %d keys over fuzzy threshold %d; fuzzy matching disabled (exact-only) — raise Options.FuzzyMaxNames to override",
				totalKeys, nf.opts.FuzzyMaxNames)
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
		// the Options.FuzzyMaxNames disable above — exact-only from here on. Only the
		// goroutine that won the fuzzyBuilding CAS reaches this point, so the
		// log fires exactly once.
		nf.fuzzyState.Store(fuzzyDisabled)
		log.Printf("fuzzy n-gram index build failed; fuzzy matching disabled (exact-only): %v", err)
		return
	}

	index.bind(nf.opts.FuzzyMaxCandidates, &nf.fuzzyStats)

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
// built, 1 = building, 2 = built, 3 = disabled (corpus over Options.FuzzyMaxNames,
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
