package name

import (
	"container/list"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"unique"

	"github.com/SamyRai/cityFinder/lib/city"
)

// internString uses Go 1.23's unique package for efficient string interning
// This provides automatic cleanup and better memory management than custom implementation
func internString(s string) string {
	if s == "" {
		return s
	}
	handle := unique.Make(s)
	return handle.Value()
}

// nameTable is one country's exact-lookup structure: the country's distinct
// names sorted ascending, with each name's city ids grouped CSR-style
// between adjacent start offsets. It replaces the nested
// map[string][]*city.City the index grew out of: at prod scale (~18.7M
// (country, name) keys) the nested map's headers, buckets, and pointer
// slices dwarfed the payload they carried, while three flat arrays cost
// ~16 B per name header + 4 B per offset + 4 B per reference.
//
// A table is immutable once built (BuildIndex's flatten or
// DeserializeIndex); names added afterwards live in the Finder's overflow,
// so no field here is ever mutated under a lock and concurrent readers need
// no synchronization of their own beyond the Finder's RWMutex.
type nameTable struct {
	names  []string // sorted ascending, one entry per distinct (country, name) key
	starts []int32  // len(names)+1; the ids of names[i] are ids[starts[i]:starts[i+1]]
	ids    []int32  // CSR city ids (row numbers, see cityTable), homonyms in load order
}

// lookup binary-searches name in the sorted table and returns the CSR slice
// of city ids stored under it. A name indexed with zero references reports
// found with an empty slice, mirroring the present-but-empty key the nested
// map carried.
func (t *nameTable) lookup(name string) ([]int32, bool) {
	i := sort.SearchStrings(t.names, name)
	if i < len(t.names) && t.names[i] == name {
		return t.ids[t.starts[i]:t.starts[i+1]], true
	}
	return nil, false
}

// Finder is a struct that contains the data for city name lookups.
// Struct field ordering optimized for memory alignment (Go 1.22+ best practice):
// - Pointers and maps first (8 bytes on 64-bit)
// - Bools and small fields last (1 byte, but padding matters)
//
// The exact-lookup index is flat: one nameTable per country (~250 keys at
// prod scale, so a map here costs nothing), one city table every id resolves
// through (see cityTable: ids are input row numbers, and the table can be the
// S2 index's own, so a city exists once per process), and a small unsorted
// overflow for names added after construction (AddCity and friends) that is
// consulted after the sorted-table miss. Every name of a city stores the
// same row id, so all of them resolve to the same *city.City.
type Finder struct {
	countries     map[string]*nameTable                // per-country exact-lookup tables
	cities        cityTable                            // every id in a table or the overflow resolves here
	overflow      map[string]map[string][]int32        // post-construction additions: country -> name -> city ids into cities
	ngrams        *ngramIndex                          // Immutable q-gram fuzzy index (lazy-built, never serialized)
	fuzzyOverflow []string                             // Names added after the last fuzzy build; linearly scanned until the next build folds them in
	fuzzyCache    map[fuzzyCacheKey]*fuzzySearchResult // Cache for fuzzy search results
	fuzzyCacheAge list.List                            // fuzzyCache keys, oldest insertion first (values are fuzzyCacheKey)
	fuzzyGen      atomic.Uint64                        // bumped whenever a name becomes fuzzy-visible after construction; invalidates cached results
	cacheMutex    sync.RWMutex                         // Mutex for fuzzy search cache and fuzzyCacheAge
	mutex         sync.RWMutex                         // Mutex for thread-safe operations
	opts          Options                              // Fuzzy limits, fixed at construction; read without locking
	fuzzyStats    fuzzyStats                           // Per-Finder fuzzy search diagnostics (budget trips); safe for concurrent use
	fuzzyState    atomic.Int32                         // Lazy fuzzy-index state (fuzzyNotBuilt*, above); runtime-only, not serialized
}

// Memory pools removed - they were causing excessive memory usage

// NewNameFinder creates a new NameFinder instance with default capacity.
// opts optionally overrides the fuzzy limits (see Options).
func NewNameFinder(opts ...Options) *Finder {
	return NewFinderWithCapacity(300, opts...)
}

// NewFinderWithCapacity creates a new NameFinder with a pre-allocated
// countries-map capacity. (The former names parameter was removed with the
// nested-map index — the flat tables are sized exactly at build time.) opts
// optionally overrides the fuzzy limits (see Options).
func NewFinderWithCapacity(countries int, opts ...Options) *Finder {
	return &Finder{
		opts:       resolveOptions(opts),
		countries:  make(map[string]*nameTable, countries),
		fuzzyCache: make(map[fuzzyCacheKey]*fuzzySearchResult, 100), // Pre-allocate cache capacity
	}
}

// firstCityExactLocked resolves an exact name to its first city under the
// given country: the sorted table first, the overflow second (the same
// probe order CityByName's exact phase and PrefixNames use). The caller must
// hold nf.mutex for reading.
func (nf *Finder) firstCityExactLocked(countryCode, name string) (*city.City, bool) {
	if t := nf.countries[countryCode]; t != nil {
		if ids, ok := t.lookup(name); ok && len(ids) > 0 {
			return nf.cities.at(ids[0]), true
		}
	}
	if ids, ok := nf.overflow[countryCode][name]; ok && len(ids) > 0 {
		return nf.cities.at(ids[0]), true
	}
	return nil, false
}

// namesFromIndex returns the union of every indexed name across all countries.
// The sorted tables plus the overflow are the source of truth — every
// insertion path (batch build, AddCity) writes one of them — so this is
// always the complete name set.
func (nf *Finder) namesFromIndex() []string {
	total := 0
	for _, t := range nf.countries {
		total += len(t.names)
	}
	for _, countryOverflow := range nf.overflow {
		total += len(countryOverflow)
	}

	nameSet := make(map[string]struct{}, total)
	for _, t := range nf.countries {
		for _, name := range t.names {
			nameSet[name] = struct{}{}
		}
	}
	for _, countryOverflow := range nf.overflow {
		for name := range countryOverflow {
			nameSet[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	// Sorted, so the fuzzy index numbers names deterministically: map
	// iteration order would otherwise leak into search order (and, via the
	// candidate budget, into which matches a truncated search returns).
	slices.Sort(names)
	return names
}

// totalIndexKeys returns the total number of (country, name) keys across the
// sorted tables and the overflow — the gate metric for Options.FuzzyMaxNames.
// O(#countries) additions, no allocations. The caller must hold nf.mutex
// (read or write): AddCity mutates the overflow under the write lock.
func (nf *Finder) totalIndexKeys() int {
	total := 0
	for _, t := range nf.countries {
		total += len(t.names)
	}
	for _, countryOverflow := range nf.overflow {
		total += len(countryOverflow)
	}
	return total
}

// CityByName finds the coordinates of a city by its name using hybrid search strategy
func (nf *Finder) CityByName(name string, countryCode string) *city.City {
	// Phase 1: Try exact match first (fastest): binary search in the
	// country's sorted table, then the post-construction overflow. The same
	// read lock also answers "does this country hold any indexed names at
	// all" for the early exit below.
	nf.mutex.RLock()
	t := nf.countries[countryCode]
	countryOverflow := nf.overflow[countryCode]
	hasEntries := (t != nil && len(t.names) > 0) || len(countryOverflow) > 0
	var exact *city.City
	if hasEntries {
		if t != nil {
			if ids, ok := t.lookup(name); ok && len(ids) > 0 {
				exact = nf.cities.at(ids[0])
			}
		}
		if exact == nil {
			if ids, ok := countryOverflow[name]; ok && len(ids) > 0 {
				exact = nf.cities.at(ids[0])
			}
		}
	}
	nf.mutex.RUnlock()
	if exact != nil {
		return exact
	}

	// Country early-exit: fuzzy candidates only ever resolve per-country
	// (phases 2/3 re-probe the very same country's tables), so when the
	// country holds no indexed names the fuzzy phases cannot produce a hit.
	// Return the exact-miss nil without touching the fuzzy machinery — no
	// lazy n-gram build trigger, no walk, no cache traffic. Without this, a
	// single typo'd query against an unknown country code would pay for (or
	// even kick off) the whole ~94 s prod-scale build.
	if !hasEntries {
		return nil
	}

	// Phases 2/3: approximate matching as a pre-filter, then full fuzzy. Both
	// are cheap no-ops unless the fuzzy index is actually built: over the
	// Options.FuzzyMaxNames threshold matching is disabled (exact-only) for this
	// Finder's lifetime, and while the background build is still running —
	// including the very lookup that triggered it — this lookup reports
	// not-ready rather than waiting. Either way no fuzzy work runs and no
	// empty result pollutes the cache.
	nf.ensureFuzzyBuilt()
	if nf.fuzzyState.Load() == fuzzyBuilt {
		fastCandidates := nf.getCachedFuzzySearch(name, 1) // Use tighter threshold for speed

		// Every read lock is scoped tightly around the table probes only:
		// holding one across getCachedFuzzySearch deadlocks, because a cold
		// cache takes the write lock inside ensureFuzzyBuilt while this
		// goroutine still holds the read lock (RWMutex self-deadlock).
		if len(fastCandidates) > 0 {
			nf.mutex.RLock()
			for _, candidate := range fastCandidates {
				if c, ok := nf.firstCityExactLocked(countryCode, candidate); ok {
					nf.mutex.RUnlock()
					return c
				}
			}
			nf.mutex.RUnlock()
		}

		// Phase 3: Fall back to full fuzzy search with distance 2
		fullCandidates := nf.getCachedFuzzySearch(name, 2)

		if len(fullCandidates) > 0 {
			nf.mutex.RLock()
			for _, candidate := range fullCandidates {
				if c, ok := nf.firstCityExactLocked(countryCode, candidate); ok {
					nf.mutex.RUnlock()
					return c
				}
			}
			nf.mutex.RUnlock()
		}
	}

	return nil
}
