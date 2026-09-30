package name

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
)

// fuzzyFixtureCities returns a small city set with Levenshtein-distinct names
// so fuzzy neighborhoods do not overlap between countries.
func fuzzyFixtureCities() []city.SpatialCity {
	return []city.SpatialCity{
		{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}},
		{City: city.City{Name: "London", Country: "GB", Latitude: 51.50, Longitude: -0.12}},
		{City: city.City{Name: "Berlin", Country: "DE", Latitude: 52.52, Longitude: 13.40}},
		{City: city.City{Name: "Tokyo", Country: "JP", Latitude: 35.68, Longitude: 139.69}},
		{City: city.City{Name: "Madrid", Country: "ES", Latitude: 40.42, Longitude: -3.70}},
	}
}

// TestCityByNameFuzzyRevived proves fuzzy search works on an index produced by
// BuildIndex (which intentionally skips collectAllNames): a distance-1 typo is
// resolved by the phase-2 fuzzy pass and a distance-2 typo by the phase-3 pass,
// while the country filter still rejects cross-country matches.
func TestCityByNameFuzzyRevived(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())

	// Phase 2: distance-1 typo ("Pars" -> "Paris").
	got := finder.CityByName("Pars", "FR")
	if got == nil || got.Name != "Paris" {
		t.Fatalf("distance-1 fuzzy: CityByName(\"Pars\", \"FR\") = %+v, want Paris", got)
	}

	// Phase 3: distance-2 typo ("Paars" -> "Paris"); nothing is within
	// distance 1 of the query, so phase 2 must come up empty.
	got = finder.CityByName("Paars", "FR")
	if got == nil || got.Name != "Paris" {
		t.Fatalf("distance-2 fuzzy: CityByName(\"Paars\", \"FR\") = %+v, want Paris", got)
	}

	// The fuzzy candidate set is global, but resolution is per-country:
	// "Pars" must not resolve to anything in DE.
	if got := finder.CityByName("Pars", "DE"); got != nil {
		t.Fatalf("CityByName(\"Pars\", \"DE\") = %q, want nil", got.Name)
	}
}

// TestCityByNameNoSelfDeadlock constructs the exact deadlock trigger state:
// phase 2 produces a non-empty candidate list (a distance-1 tree hit) that does
// not resolve in the requested country, and the (name, 2) cache key is cold,
// forcing phase 3 to take the write lock while the phase-2 read lock is (pre
// fix) still held via defer.
func TestCityByNameNoSelfDeadlock(t *testing.T) {
	finder := NewNameFinder()
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}})
	finder.AddCity(city.SpatialCity{City: city.City{Name: "London", Country: "GB", Latitude: 51.50, Longitude: -0.12}})

	done := make(chan *city.City, 1)
	go func() {
		done <- finder.CityByName("Pars", "XX") // XX has no indexed names
	}()

	select {
	case got := <-done:
		if got != nil {
			t.Fatalf("expected nil for unknown country, got %q", got.Name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CityByName self-deadlocked: read lock held across phase-3 cache miss (5s timeout)")
	}
}

// TestCityByNameConcurrentMixedRace hammers CityByName from 8 goroutines with
// a mix of exact hits, typos (fuzzy path, cold cache keys), and guaranteed
// misses. Run under -race this must stay clean: the lazy tree build, the tree
// searches, and the fuzzy cache are all shared mutable state.
func TestCityByNameConcurrentMixedRace(t *testing.T) {
	cities := make([]city.SpatialCity, 200)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:      fmt.Sprintf("RaceCity%03d", i),
				Country:   fmt.Sprintf("RC%d", i%7),
				Latitude:  float64(i % 90),
				Longitude: float64(i % 180),
			},
		}
	}
	finder := BuildIndex(cities)

	var wg sync.WaitGroup
	var hits int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch (w + i) % 3 {
				case 0: // exact hit
					if finder.CityByName(fmt.Sprintf("RaceCity%03d", i), fmt.Sprintf("RC%d", i%7)) != nil {
						atomic.AddInt64(&hits, 1)
					}
				case 1: // typo, never resolves in RC9 -> full fuzzy path
					finder.CityByName(fmt.Sprintf("RaceCitt%03d", i), "RC9")
				default: // guaranteed miss with a distinct cache key each time
					finder.CityByName(fmt.Sprintf("zz-no-such-city-%d-%d", w, i), "ZZ")
				}
			}
		}(w)
	}
	wg.Wait()
	assert.Positive(t, atomic.LoadInt64(&hits), "exact-match lookups must keep working")
}

// TestFuzzyCacheIsBounded drives more distinct miss queries than the cache cap
// and asserts the cache never grows past it. The literal mirrors
// maxFuzzyCacheEntries in name.go (kept as a literal so this test also compiles
// against the unfixed tree, where it must fail).
func TestFuzzyCacheIsBounded(t *testing.T) {
	const intendedCap = 10000

	finder := NewNameFinder()
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}})

	for i := 0; i < intendedCap+500; i++ {
		finder.getCachedFuzzySearch(fmt.Sprintf("zz%d", i), 2)
	}

	finder.cacheMutex.RLock()
	size := len(finder.fuzzyCache)
	finder.cacheMutex.RUnlock()
	assert.LessOrEqual(t, size, intendedCap, "fuzzyCache must be bounded, got %d entries", size)
}

// TestSerializeDeserializeFuzzyRoundTrip round-trips an index whose BK-tree
// was built before serialization and requires fuzzy search to survive.
func TestSerializeDeserializeFuzzyRoundTrip(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())

	// Populate the BK-tree before serializing.
	if got := finder.CityByName("Pars", "FR"); got == nil || got.Name != "Paris" {
		t.Fatalf("pre-serialize fuzzy lookup failed: %+v", got)
	}

	tmpfile, err := os.CreateTemp("", "name_fuzzy_roundtrip_*.gob")
	assert.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	assert.NoError(t, finder.SerializeIndex(tmpfile.Name()))

	restored, err := DeserializeIndex(tmpfile.Name())
	assert.NoError(t, err)

	got := restored.CityByName("Pars", "FR")
	if got == nil || got.Name != "Paris" {
		t.Fatalf("post-deserialize fuzzy lookup: %+v, want Paris", got)
	}
}

// TestDeserializeRevivesEmptyBuiltTree simulates files written by builds whose
// fuzzy path never ran: the persisted state claims isBKTreeBuilt=true while the
// tree is empty and allNames is empty. Deserialization must not keep that
// dead-tree state, or fuzzy search stays exact-only forever.
func TestDeserializeRevivesEmptyBuiltTree(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())
	finder.mutex.Lock()
	finder.isBKTreeBuilt = true // the lie persisted by the unfixed build path
	finder.mutex.Unlock()

	tmpfile, err := os.CreateTemp("", "name_revive_*.gob")
	assert.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	assert.NoError(t, finder.SerializeIndex(tmpfile.Name()))

	restored, err := DeserializeIndex(tmpfile.Name())
	assert.NoError(t, err)

	got := restored.CityByName("Pars", "FR")
	if got == nil || got.Name != "Paris" {
		t.Fatalf("fuzzy lookup after deserializing an empty built tree: %+v, want Paris", got)
	}
}

// TestDeserializeGarbageTailReturnsError checks that a decode error in the
// lazy-load trailer (here: a value of the wrong gob type where allNames is
// expected) is reported instead of silently swallowed.
func TestDeserializeGarbageTailReturnsError(t *testing.T) {
	finder := NewNameFinder()
	finder.AddCity(city.SpatialCity{City: city.City{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}})

	buf := new(bytes.Buffer)
	enc := gob.NewEncoder(buf)
	assert.NoError(t, enc.Encode(finder.InvertedIndex))
	assert.NoError(t, enc.Encode(finder.BKTree))
	assert.NoError(t, enc.Encode(finder.isBKTreeBuilt))
	assert.NoError(t, enc.Encode(12345)) // wrong type where []string is expected

	tmpfile, err := os.CreateTemp("", "name_corrupt_*.gob")
	assert.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()
	assert.NoError(t, os.WriteFile(tmpfile.Name(), buf.Bytes(), 0o600))

	_, err = DeserializeIndex(tmpfile.Name())
	assert.Error(t, err, "a malformed trailer must surface as an error, not be swallowed")
	assert.ErrorIs(t, err, ErrCorruptIndex, "a malformed trailer is corruption and must be rebuildable")
}
