package name

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
)

// gateCities builds count unique-name cities in one country. Unique names
// matter: the lazy BK-tree build cost scales with the number of distinct
// names, and this file needs a build that runs long enough (~1s at 200K on
// Apple silicon) to overlap with concurrent exact lookups.
func gateCities(count int) []city.SpatialCity {
	cities := make([]city.SpatialCity, count)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      fmt.Sprintf("GateCity%06d", i),
				Country:   "GC",
				Latitude:  1,
				Longitude: 1,
			},
		}
	}
	return cities
}

// waitFuzzyBuilt drives the lazy fuzzy state machine to fuzzyBuilt. The
// build runs in a background goroutine, so any test that needs the built
// index after triggering it (directly or via a lookup) waits here. The
// ensureFuzzyBuilt nudge matters: a build discarded because AddCity raced
// its snapshot resets to fuzzyNotBuilt and only a fresh call restarts it.
// Must run on the test/benchmark goroutine (it calls tb.Fatal).
func waitFuzzyBuilt(tb testing.TB, nf *Finder) {
	tb.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for nf.fuzzyState.Load() != fuzzyBuilt {
		switch nf.fuzzyState.Load() {
		case fuzzyDisabled:
			tb.Fatal("fuzzy index reached fuzzyDisabled (FuzzyMaxNames gate or build failure); wanted fuzzyBuilt")
		}
		if time.Now().After(deadline) {
			tb.Fatal("fuzzy index did not reach fuzzyBuilt within 60s")
		}
		nf.ensureFuzzyBuilt()
		time.Sleep(2 * time.Millisecond)
	}
}

// TestCityByNameBuildDoesNotBlockExactLookups proves the lazy n-gram build no
// longer runs in the triggering request. The typo lookup that wins the CAS
// spawns the build in a background goroutine and itself returns on the
// degraded exact-only path — a fast nil instead of a 30-90 s hang — while
// the main goroutine hammers exact lookups and records the worst latency.
//
// The assertions are relative (trigger and worst exact well under half the
// build's wall time) so they hold on fast and slow machines alike. A
// regression to the synchronous build fails the trigger-elapsed assertion:
// the triggering lookup would block for (nearly) the whole build.
func TestCityByNameBuildDoesNotBlockExactLookups(t *testing.T) {
	const nameCount = 200_000
	finder := BuildIndex(gateCities(nameCount))

	buildStart := time.Now()
	triggerDone := make(chan struct {
		elapsed time.Duration
		got     *city.City
	}, 1)
	go func() {
		start := time.Now()
		// Guaranteed miss: exact phase fails, phases 2/3 would need the
		// fuzzy index — which is only starting to build. The triggering
		// query must return the exact-only nil now, not wait out the build.
		got := finder.CityByName("zzz-definitely-not-a-city", "GC")
		triggerDone <- struct {
			elapsed time.Duration
			got     *city.City
		}{time.Since(start), got}
	}()

	// Exact lookups run across the whole build window; the loop ends when
	// the background build lands.
	var worstExact atomic.Int64
	deadline := time.Now().Add(30 * time.Second) // hard guard: fail, never hang
	for finder.fuzzyState.Load() != fuzzyBuilt {
		if time.Now().After(deadline) {
			t.Fatal("hard timeout: the background build did not finish within 30s")
		}

		start := time.Now()
		if got := finder.CityByName("GateCity000042", "GC"); got == nil {
			t.Fatal("exact lookup must keep hitting during the build")
		}
		if d := int64(time.Since(start)); d > worstExact.Load() {
			worstExact.Store(d)
		}
	}
	buildElapsed := time.Since(buildStart)

	// The triggering query must have returned (with the degraded nil) well
	// before the build finished.
	select {
	case trig := <-triggerDone:
		if trig.got != nil {
			t.Fatalf("the triggering query must return the exact-only nil, got %q", trig.got.Name)
		}
		if trig.elapsed >= buildElapsed/2 {
			t.Fatalf("the triggering lookup blocked on the build: %v vs build wall time %v (must be well under half)",
				trig.elapsed, buildElapsed)
		}
		if worst := time.Duration(worstExact.Load()); worst >= buildElapsed/2 {
			t.Fatalf("exact lookups stalled behind the build: worst exact lookup %v vs build wall time %v (must be well under half)",
				worst, buildElapsed)
		}
		t.Logf("build wall time: %v; triggering lookup: %v; worst exact lookup during build: %v",
			buildElapsed, trig.elapsed, time.Duration(worstExact.Load()))
	case <-time.After(5 * time.Second):
		t.Fatal("the triggering lookup did not return within 5s of the build completing")
	}

	// The background build did land: a typo query now resolves through the
	// finished structure.
	if got := finder.CityByName("GateCitt000042", "GC"); got == nil || got.Name != "GateCity000042" {
		t.Fatalf("post-build typo lookup: got %+v, want GateCity000042", got)
	}
}

// TestFuzzyDisabledOverThreshold covers the FuzzyMaxNames gate: an index over
// the threshold must never build the n-gram structure, must log the disable
// exactly once, must serve typo lookups as fast nils, must keep exact lookups
// working, and must leave the fuzzy cache empty (no pollution from
// not-ready results).
func TestFuzzyDisabledOverThreshold(t *testing.T) {
	orig := FuzzyMaxNames
	FuzzyMaxNames = 4 // fuzzyFixtureCities has 5 (country,name) keys
	t.Cleanup(func() { FuzzyMaxNames = orig })

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	finder := BuildIndex(fuzzyFixtureCities())

	start := time.Now()
	if got := finder.CityByName("Pars", "FR"); got != nil {
		t.Fatalf("typo lookup over the threshold must return nil, got %q", got.Name)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("disabled typo lookup must be a fast nil, took %v", elapsed)
	}

	// A second (and further) lookups must not re-log or change behavior.
	if got := finder.CityByName("Pariis", "FR"); got != nil {
		t.Fatalf("second typo lookup over the threshold must return nil, got %q", got.Name)
	}

	finder.mutex.RLock()
	ngrams := finder.ngrams
	finder.mutex.RUnlock()
	if ngrams != nil {
		t.Fatalf("no n-gram build may run over the threshold: structure is %v", ngrams != nil)
	}
	if st := finder.fuzzyState.Load(); st != fuzzyDisabled {
		t.Fatalf("fuzzy state = %d, want fuzzyDisabled (%d)", st, fuzzyDisabled)
	}

	if n := strings.Count(logBuf.String(), "fuzzy matching disabled"); n != 1 {
		t.Fatalf("disable must be logged exactly once, got %d lines in %q", n, logBuf.String())
	}

	// Exact lookups are completely unaffected.
	if got := finder.CityByName("Paris", "FR"); got == nil || got.Name != "Paris" {
		t.Fatalf("exact lookup must keep working with fuzzy disabled, got %+v", got)
	}

	// Direct fuzzy callers also get uncached nils (no cache pollution).
	if got := finder.getCachedFuzzySearch("Pars", 1); got != nil {
		t.Fatalf("getCachedFuzzySearch over the threshold must return nil, got %v", got)
	}
	finder.cacheMutex.RLock()
	cacheSize := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()
	if cacheSize != 0 {
		t.Fatalf("disabled fuzzy lookups must not cache results, cache has %d entries", cacheSize)
	}

	// Concurrent exact lookups during typo misses must not block: the
	// disabled path never takes the write lock.
	stop := make(chan struct{})
	missDone := make(chan struct{})
	go func() {
		defer close(missDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			finder.CityByName(fmt.Sprintf("Typooo%03d", i%50), "FR")
		}
	}()
	var worstExact atomic.Int64
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		s := time.Now()
		if finder.CityByName("Paris", "FR") == nil {
			t.Fatal("exact lookup must keep hitting alongside disabled typo misses")
		}
		if d := int64(time.Since(s)); d > worstExact.Load() {
			worstExact.Store(d)
		}
	}
	close(stop)
	<-missDone
	if worst := time.Duration(worstExact.Load()); worst > 50*time.Millisecond {
		t.Fatalf("exact lookups stalled behind a disabled typo miss: worst %v", worst)
	}
}

// TestFuzzyMissDuringBuildIsNotCached pins the building-window contract: while
// another goroutine is building the tree, fuzzy misses return nil WITHOUT
// caching the empty result (the 1h TTL would pin it past the build
// completing), and once the build lands the same query resolves normally and
// caching resumes.
func TestFuzzyMissDuringBuildIsNotCached(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())

	// Simulate the building window: the state machine is held in
	// fuzzyBuilding, exactly as a concurrent builder would hold it.
	finder.fuzzyState.Store(fuzzyBuilding)

	if got := finder.CityByName("Pars", "FR"); got != nil {
		t.Fatalf("during a build the fuzzy phases must report not-ready, got %q", got.Name)
	}
	if got := finder.getCachedFuzzySearch("Paars", 2); got != nil {
		t.Fatalf("direct fuzzy search during a build must report not-ready, got %v", got)
	}

	finder.cacheMutex.RLock()
	size := len(finder.fuzzyCache)
	_, pars1 := finder.fuzzyCache["Pars_1"]
	_, pars2 := finder.fuzzyCache["Pars_2"]
	_, paars2 := finder.fuzzyCache["Paars_2"]
	finder.cacheMutex.RUnlock()
	if size != 0 || pars1 || pars2 || paars2 {
		t.Fatalf("not-ready misses must not be cached: size=%d pars1=%v pars2=%v paars2=%v", size, pars1, pars2, paars2)
	}

	// The build completes: retrigger it and wait out the background build,
	// then the same query must resolve and repopulate the cache.
	finder.fuzzyState.Store(fuzzyNotBuilt)
	finder.CityByName("Pars", "FR") // retriggers the background build
	waitFuzzyBuilt(t, finder)
	if got := finder.CityByName("Pars", "FR"); got == nil || got.Name != "Paris" {
		t.Fatalf("after the build the same query must hit, got %+v", got)
	}
	if st := finder.fuzzyState.Load(); st != fuzzyBuilt {
		t.Fatalf("state after the build = %d, want fuzzyBuilt (%d)", st, fuzzyBuilt)
	}
	finder.cacheMutex.RLock()
	_, pars1 = finder.fuzzyCache["Pars_1"]
	finder.cacheMutex.RUnlock()
	if !pars1 {
		t.Fatal("post-build fuzzy results must be cached again")
	}
}

// TestAddCityDuringBuildIsNotLost guards the lock-free build's commit check.
// A name added while a build is in flight must end up fuzzy-searchable no
// matter which side of the snapshot/commit it lands on: without the
// key-count guard at commit time, the snapshot-completed structure would be
// swapped in wholesale and silently drop every name AddCity added after the
// snapshot — lost to fuzzy search forever, since a committed build is never
// rebuilt.
func TestAddCityDuringBuildIsNotLost(t *testing.T) {
	const nameCount = 100_000
	finder := BuildIndex(gateCities(nameCount))

	// Trigger the build; it now runs in the background, so the trigger
	// returns immediately and the AddCity loop below races the actual
	// build window (snapshot vs commit key-count check).
	finder.CityByName("zzz-definitely-not-a-city", "GC") // triggers the background build

	// Continuously add fresh names while the build runs, bounded so the loop
	// terminates even if the build is instant on future hardware.
	var added []string
	for i := 0; i < 5000 && finder.fuzzyState.Load() != fuzzyBuilt; i++ {
		name := fmt.Sprintf("LateCitt%04d", i)
		finder.AddCity(city.SpatialCity{City: city.City{Name: name, Country: "GC", Latitude: 2, Longitude: 2}})
		added = append(added, name)
	}

	// Force any discarded build to be retried and settle into the built
	// state, then verify every concurrently added name is fuzzy-searchable.
	// (A build that lost the snapshot race resets to fuzzyNotBuilt and only
	// a fresh ensureFuzzyBuilt call — which waitFuzzyBuilt supplies —
	// restarts it.)
	waitFuzzyBuilt(t, finder)

	step := len(added) / 200
	if step < 1 {
		step = 1
	}
	nChecked := 0
	for i := 0; i < len(added); i += step {
		// A distance-1 typo of the added name must resolve to it: names
		// that made it into the committed build are found via the n-gram
		// index, names that arrived after the commit via the overflow list.
		typo := "LateCiti" + added[i][len("LateCitt"):]
		got, _ := finder.fuzzyCandidates(typo, 1)
		if !containsName(got, added[i]) {
			t.Fatalf("name %q added during the build window is not fuzzy-searchable: the lock-free swap or the overflow list dropped it (candidates: %d)", added[i], len(got))
		}
		nChecked++
	}
	t.Logf("verified %d of %d concurrently added names are fuzzy-searchable", nChecked, len(added))
}

// containsName reports whether s is in names.
func containsName(names []string, s string) bool {
	for _, n := range names {
		if n == s {
			return true
		}
	}
	return false
}

// TestWarmFuzzyIdempotentNonBlocking pins the WarmFuzzy contract: the call
// arranges the background build and returns immediately (well under the
// build's wall time), further calls are no-ops that leave the built state
// intact, and on an empty index it settles back to fuzzyNotBuilt (nothing to
// build; later AddCity growth can still trigger a fresh build).
func TestWarmFuzzyIdempotentNonBlocking(t *testing.T) {
	finder := BuildIndex(gateCities(50_000)) // build measurably outlasts the call

	start := time.Now()
	finder.WarmFuzzy()
	callElapsed := time.Since(start)
	if st := finder.fuzzyState.Load(); st != fuzzyBuilding && st != fuzzyBuilt {
		t.Fatalf("after WarmFuzzy the state must be fuzzyBuilding (or already fuzzyBuilt), got %d", st)
	}

	waitFuzzyBuilt(t, finder)
	buildElapsed := time.Since(start)
	if callElapsed >= buildElapsed/2 {
		t.Fatalf("WarmFuzzy blocked on the build: call took %v vs build wall time %v (must be well under half)",
			callElapsed, buildElapsed)
	}
	t.Logf("WarmFuzzy call: %v; background build wall time: %v", callElapsed, buildElapsed)

	// Idempotent: calls from the built state are no-ops.
	for i := 0; i < 3; i++ {
		finder.WarmFuzzy()
	}
	if st := finder.fuzzyState.Load(); st != fuzzyBuilt {
		t.Fatalf("WarmFuzzy after built must be a no-op, state = %d", st)
	}
	if got := finder.CityByName("GateCitt000042", "GC"); got == nil || got.Name != "GateCity000042" {
		t.Fatalf("typo lookup after warm-up: got %+v, want GateCity000042", got)
	}

	// Empty index: the background build yields no structure and the state
	// settles back to fuzzyNotBuilt — warm-up must not wedge it in
	// fuzzyBuilding or claim fuzzyBuilt.
	empty := NewNameFinder()
	empty.WarmFuzzy()
	deadline := time.Now().Add(10 * time.Second)
	for empty.fuzzyState.Load() == fuzzyBuilding {
		if time.Now().After(deadline) {
			t.Fatal("empty-finder warm-up stuck in fuzzyBuilding")
		}
		time.Sleep(time.Millisecond)
	}
	if st := empty.fuzzyState.Load(); st != fuzzyNotBuilt {
		t.Fatalf("empty finder warm-up must settle back to fuzzyNotBuilt, got %d", st)
	}
}

// TestCityByNameUnknownCountrySkipsFuzzy pins the country early-exit: a
// query whose country code has no entries in the name index returns the
// exact-miss nil without triggering ANY fuzzy work — no lazy build (the
// state machine stays fuzzyNotBuilt, no n-gram structure appears), no walk,
// no cache traffic. This covers both a missing country key and an
// existing-but-empty country map.
func TestCityByNameUnknownCountrySkipsFuzzy(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())
	// The existing-but-empty variant: only reachable by direct construction
	// (every public insertion path adds a name in the same critical section).
	finder.mutex.Lock()
	finder.InvertedIndex["QQ"] = map[string][]*city.City{}
	finder.mutex.Unlock()

	for _, country := range []string{"XX", "", "QQ"} {
		if got := finder.CityByName("Pariis", country); got != nil {
			t.Fatalf("CityByName(%q, %q) = %q, want nil via the country early-exit", "Pariis", country, got.Name)
		}
	}

	if st := finder.fuzzyState.Load(); st != fuzzyNotBuilt {
		t.Fatalf("unknown-country typo must not trigger the fuzzy build, state = %d", st)
	}
	finder.mutex.RLock()
	ngrams := finder.ngrams
	finder.mutex.RUnlock()
	if ngrams != nil {
		t.Fatal("unknown-country typo must not build an n-gram structure")
	}
	finder.cacheMutex.RLock()
	cacheSize := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()
	if cacheSize != 0 {
		t.Fatalf("unknown-country typo must not touch the fuzzy cache, got %d entries", cacheSize)
	}

	// Sanity: the early exit is per-country — a fuzzy query for a country
	// WITH entries still resolves normally once the build has run.
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)
	if got := finder.CityByName("Pariis", "FR"); got == nil || got.Name != "Paris" {
		t.Fatalf("existing-country typo must still resolve via fuzzy, got %+v", got)
	}
	if st := finder.fuzzyState.Load(); st != fuzzyBuilt {
		t.Fatalf("warm-up must have built the index, state = %d", st)
	}
}

// TestAddCityVisibleAfterUncachedEmptyMiss is the L1 regression test: an
// empty fuzzy miss must NOT be cached, so a name AddCity adds afterwards is
// visible to the very next identical query (via the always-scanned overflow
// list). Under the old behavior the empty result was pinned for the 1h TTL
// and the repeat kept serving the stale nil. Non-empty results keep being
// cached unchanged.
func TestAddCityVisibleAfterUncachedEmptyMiss(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)

	query := "Springfielm" // distance 1 from "Springfield"; nothing indexed is within distance 2
	if got := finder.CityByName(query, "US"); got != nil {
		t.Fatalf("query with no indexed match must return nil, got %q", got.Name)
	}
	finder.cacheMutex.RLock()
	_, q1 := finder.fuzzyCache[query+"_1"]
	_, q2 := finder.fuzzyCache[query+"_2"]
	finder.cacheMutex.RUnlock()
	if q1 || q2 {
		t.Fatalf("empty fuzzy results must not be cached: %q_1=%v %q_2=%v", query, q1, query, q2)
	}

	finder.AddCity(city.SpatialCity{City: city.City{Name: "Springfield", Country: "US", Latitude: 39.78, Longitude: -89.65}})

	// The same query must now resolve through the overflow scan.
	got := finder.CityByName(query, "US")
	if got == nil || got.Name != "Springfield" {
		t.Fatalf("repeat query after AddCity must find the new name, got %+v", got)
	}

	// Non-empty results are cached as before (unchanged behavior).
	finder.cacheMutex.RLock()
	_, cached := finder.fuzzyCache[query+"_1"]
	finder.cacheMutex.RUnlock()
	if !cached {
		t.Fatal("the now non-empty fuzzy result must be cached")
	}
}
