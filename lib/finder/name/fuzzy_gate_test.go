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

// TestCityByNameBuildDoesNotBlockExactLookups proves the lazy n-gram build no
// longer holds nf.mutex for writing while it runs. One goroutine triggers the
// build with a typo lookup (guaranteed miss -> phases 2/3), while the main
// goroutine hammers exact lookups and records the worst latency.
//
// The assertion is relative (worst exact < half the build-triggering lookup's
// total duration) so it holds on fast and slow machines alike.
func TestCityByNameBuildDoesNotBlockExactLookups(t *testing.T) {
	const nameCount = 200_000
	finder := BuildIndex(gateCities(nameCount))

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		// Guaranteed miss: exact phase fails, phases 2/3 trigger the lazy
		// build and search the finished tree.
		finder.CityByName("zzz-definitely-not-a-city", "GC")
		done <- time.Since(start)
	}()

	var worstExact atomic.Int64
	deadline := time.Now().Add(30 * time.Second) // hard guard: fail, never hang
	for {
		select {
		case triggerElapsed := <-done:
			worst := time.Duration(worstExact.Load())
			if worst >= triggerElapsed/2 {
				t.Fatalf("exact lookups stalled behind the BK-tree build: worst exact lookup %v vs build-triggering lookup %v (must be well under half)",
					worst, triggerElapsed)
			}
			t.Logf("build-triggering lookup: %v; worst exact lookup during build: %v", triggerElapsed, worst)
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("hard timeout: the build-triggering lookup did not finish within 30s")
		}

		start := time.Now()
		if got := finder.CityByName("GateCity000042", "GC"); got == nil {
			t.Fatal("exact lookup must keep hitting during the build")
		}
		if d := int64(time.Since(start)); d > worstExact.Load() {
			worstExact.Store(d)
		}
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

	// The build completes: the same query must now resolve and repopulate
	// the cache.
	finder.fuzzyState.Store(fuzzyNotBuilt)
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

	done := make(chan struct{})
	go func() {
		defer close(done)
		finder.CityByName("zzz-definitely-not-a-city", "GC") // triggers the build
	}()

	// Continuously add fresh names while the build runs, bounded so the loop
	// terminates even if the build is instant on future hardware.
	var added []string
	buildDone := false
	for i := 0; i < 5000 && !buildDone; i++ {
		name := fmt.Sprintf("LateCitt%04d", i)
		finder.AddCity(city.SpatialCity{City: city.City{Name: name, Country: "GC", Latitude: 2, Longitude: 2}})
		added = append(added, name)
		select {
		case <-done:
			buildDone = true
		default:
		}
	}
	<-done

	// Force any discarded build to be retried and settle into the built
	// state, then verify every concurrently added name is fuzzy-searchable.
	finder.CityByName("zzz-also-not-a-city", "GC")
	if st := finder.fuzzyState.Load(); st != fuzzyBuilt {
		t.Fatalf("fuzzy state after settling = %d, want fuzzyBuilt (%d)", st, fuzzyBuilt)
	}

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
