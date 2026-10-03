package name

import (
	"context"
	"log"
)

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
// built, or disabled when the index exceeds Options.FuzzyMaxNames. It is safe
// to call from any lookup path — the common case is one atomic load — and no
// caller ever waits on a build or even on the index lock: the goroutine that
// wins the fuzzyNotBuilt -> fuzzyBuilding CAS spawns buildFuzzyIndex and
// returns immediately, and every other caller sees fuzzyBuilding and returns.
// The triggering lookup then takes the degraded exact-only path concurrent
// lookups always did, and every later fuzzy lookup observes the finished
// index once the build lands.
//
// Everything that reads the index — the key-total gate, the name snapshot —
// runs inside the build goroutine, so the CAS is the only work on the
// caller's path and exactly one snapshot is ever taken per attempt.
//
// A finder that has never held a name has nothing to index: a build would
// settle straight back to not-built, so every miss on an empty finder would
// spawn a goroutine for nothing. hasKeys (one more atomic load, no lock)
// skips it; the first AddCity or load that brings a key re-arms the build.
func (nf *Finder) ensureFuzzyBuilt() {
	if nf.fuzzyState.Load() != fuzzyNotBuilt || !nf.hasKeys.Load() {
		return
	}
	if nf.fuzzyState.CompareAndSwap(fuzzyNotBuilt, fuzzyBuilding) {
		// The goroutine owns the state machine from here (building ->
		// built/notBuilt/disabled). A discarded build resets to
		// fuzzyNotBuilt and is retried by the next ensureFuzzyBuilt call —
		// WarmFuzzy gives initializers an explicit way to trigger that retry.
		go nf.buildFuzzyIndex()
	}
}

// snapshotNames takes the build's consistent view of the index under the
// read lock: the key total gates the build and seeds the commit check, the
// name list seeds the n-gram index. Over Options.FuzzyMaxNames it returns
// ok=false after moving the state to the terminal fuzzyDisabled (logged
// exactly once, because only the goroutine that won the fuzzyBuilding CAS
// gets here).
func (nf *Finder) snapshotNames() (names []string, totalKeys int, ok bool) {
	nf.mutex.RLock()
	defer nf.mutex.RUnlock()
	nf.fuzzyStats.snapshots.Add(1)
	totalKeys = nf.totalIndexKeys()
	if totalKeys > nf.opts.FuzzyMaxNames {
		log.Printf("name index has %d keys over fuzzy threshold %d; fuzzy matching disabled (exact-only) — raise Options.FuzzyMaxNames to override",
			totalKeys, nf.opts.FuzzyMaxNames)
		nf.settleFuzzy(fuzzyDisabled) // after the log: a settled state implies the line is out
		return nil, totalKeys, false
	}
	return nf.namesFromIndex(), totalKeys, true
}

// buildFuzzyIndex is the background half of ensureFuzzyBuilt. It runs in its
// own goroutine, terminates on its own after exactly one build attempt, and
// never holds nf.mutex across the construction.
//
// Lock-free construction of an immutable structure: readers can neither
// observe a half-built index nor be blocked by the build.
//
// A panic anywhere in the attempt is contained here: the build goroutine has
// no caller to propagate to, so an uncontained one would take the whole
// process down for what is only a best-effort enhancement. The state moves
// to the terminal fuzzyDisabled (a build that panicked once would panic
// again) and the panic is logged.
func (nf *Finder) buildFuzzyIndex() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("fuzzy index build panicked; fuzzy matching disabled (exact-only): %v", r)
			nf.settleFuzzy(fuzzyDisabled)
		}
	}()
	names, totalKeys, ok := nf.snapshotNames()
	if !ok {
		return
	}
	index, err := buildNGramIndex(names)
	if err != nil {
		// Terminal disable, not a retry: the corpus cannot be indexed within
		// int32 CSR offsets, so every rebuild would fail identically. Mirrors
		// the Options.FuzzyMaxNames disable — exact-only from here on. Only
		// the goroutine that won the fuzzyBuilding CAS reaches this point, so
		// the log fires exactly once.
		log.Printf("fuzzy n-gram index build failed; fuzzy matching disabled (exact-only): %v", err)
		nf.settleFuzzy(fuzzyDisabled)
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
		nf.settleFuzzy(fuzzyBuilt)
	} else {
		// Uncommitted (AddCity raced the snapshot) or empty index: the next
		// fuzzy attempt retries with fresher data.
		nf.settleFuzzy(fuzzyNotBuilt)
	}
}

// settleFuzzy moves the state out of fuzzyBuilding (to built, disabled, or
// back to not-built) and releases every WaitFuzzy waiter. The state is stored
// before the broadcast, so a waiter that fetched the channel and still sees
// fuzzyBuilding is guaranteed a later close.
func (nf *Finder) settleFuzzy(state int32) {
	nf.fuzzyState.Store(state)
	nf.fuzzyWaitMu.Lock()
	if nf.fuzzySettled != nil {
		close(nf.fuzzySettled)
		nf.fuzzySettled = nil
	}
	nf.fuzzyWaitMu.Unlock()
}

// settledChan returns the channel the next settleFuzzy closes.
func (nf *Finder) settledChan() <-chan struct{} {
	nf.fuzzyWaitMu.Lock()
	defer nf.fuzzyWaitMu.Unlock()
	if nf.fuzzySettled == nil {
		nf.fuzzySettled = make(chan struct{})
	}
	return nf.fuzzySettled
}

// WaitFuzzy blocks until no fuzzy build is in flight — the state is not
// building — or ctx is done, returning ctx's error in that case. It starts
// no build: on a finder that was never warmed it returns at once. The
// settled state may be built, disabled, or, when a build was discarded
// because AddCity raced it (or the index was empty), not-built; check
// FuzzyBuildState for which. Intended for tests, orderly shutdown and
// initializers that want to gate readiness on the typo index.
func (nf *Finder) WaitFuzzy(ctx context.Context) error {
	for {
		settled := nf.settledChan() // before the state check: see settleFuzzy
		if nf.fuzzyState.Load() != fuzzyBuilding {
			return nil
		}
		select {
		case <-settled:
		case <-ctx.Done():
			return ctx.Err()
		}
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
// without blocking the caller: it never waits on the index lock, a build, or
// the name snapshot, all of which belong to the background goroutine. It is
// idempotent and safe to call from any state: with the index already built,
// building, or disabled it is a no-op (one atomic load), and on a fresh index
// it claims the build with one CAS and spawns the goroutine (see
// ensureFuzzyBuilt for the state machine). Over Options.FuzzyMaxNames the
// state reads building for a moment before the goroutine settles it at
// disabled.
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
