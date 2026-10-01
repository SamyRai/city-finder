package name

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unique"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/agnivade/levenshtein"
	"github.com/klauspost/compress/zstd"
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

// fuzzySearchResult caches fuzzy search results to avoid repeated computations
type fuzzySearchResult struct {
	candidates []string
	timestamp  time.Time
}

const (
	// fuzzyCacheTTL is how long a fuzzy result stays valid on read.
	fuzzyCacheTTL = time.Hour
	// maxFuzzyCacheEntries bounds the fuzzy result cache. It is keyed by raw
	// user input, so without a cap remote queries could grow it without
	// limit (the TTL only skips entries on read, it never evicts).
	maxFuzzyCacheEntries = 10000
)

// FuzzyMaxNames is the maximum number of (country, name) keys an index may
// contain before the lazy n-gram fuzzy build is refused. Over it, fuzzy
// matching is disabled for that Finder's lifetime and lookups degrade to
// exact-only. The gate exists because the build is O(total name runes) and
// leaves a >1 GiB resident structure at prod scale — a bound so the
// initializer can never be surprised by an index far larger than anything
// measured.
//
// The gate counts total (country, name) keys — sum of the per-country map
// lengths — not unique names. That over-approximates the distinct-name set
// (a name indexed in N countries counts N times), which is intentional: the
// count is O(#countries) with zero allocations, and fuzzy matching is a
// best-effort enhancement, never a correctness requirement.
//
// Default rationale (measured on Apple silicon, Oct 2026 GeoNames): at the
// prod scale of 18,698,093 keys / 17,727,652 distinct names the lazy
// n-gram build takes ~94 s (lock-free; only the one lookup that triggered
// it waits), the structure holds 1.18 GiB resident, and typo queries run at
// distance-1 p50 2.6 ms and distance-2 p50 20.6 ms / p99 749 ms — inside
// the <50 ms p50 target with memory under the 3 GiB budget. The default
// therefore sits above prod with ~34% key headroom. See
// docs/design/index-format-v2.md for the full scale table.
//
// Override it BEFORE the first fuzzy lookup on any Finder: the decision is
// evaluated once per index and is terminal, and the variable is read without
// synchronization.
var FuzzyMaxNames = 25_000_000

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

// Finder is a struct that contains the data for city name lookups
// Struct field ordering optimized for memory alignment (Go 1.22+ best practice):
// - Pointers and maps first (8 bytes on 64-bit)
// - Bools and small fields last (1 byte, but padding matters)
type Finder struct {
	InvertedIndex map[string]map[string][]*city.City // Inverted index for city name lookups by country
	ngrams        *ngramIndex                        // Immutable q-gram fuzzy index (lazy-built, never serialized)
	fuzzyOverflow []string                           // Names added after the last fuzzy build; linearly scanned until the next build folds them in
	fuzzyCache    map[string]*fuzzySearchResult      // Cache for fuzzy search results
	cacheMutex    sync.RWMutex                       // Mutex for fuzzy search cache
	mutex         sync.RWMutex                       // Mutex for thread-safe operations
	fuzzyState    atomic.Int32                       // Lazy fuzzy-index state (fuzzyNotBuilt*, above); runtime-only, not serialized
}

// Memory pools removed - they were causing excessive memory usage

// estimateCapacity analyzes the dataset to estimate optimal capacity for data structures
func estimateCapacity(cities []city.SpatialCity) (countries, names int) {
	// Sample the dataset to estimate sizes
	sampleSize := len(cities)
	if sampleSize > 10000 {
		sampleSize = 10000
	}

	countrySet := make(map[string]bool, 300)
	nameSet := make(map[string]bool, sampleSize*2)

	// Sample cities to estimate unique counts
	for i := 0; i < sampleSize; i++ {
		city := &cities[i]
		countrySet[city.Country] = true
		nameSet[city.Name] = true
		for _, alt := range city.AltNames {
			nameSet[alt] = true
		}
	}

	// Scale estimates based on sample
	scale := float64(len(cities)) / float64(sampleSize)
	estimatedCountries := int(float64(len(countrySet)) * scale * 1.2) // 20% overhead
	estimatedNames := int(float64(len(nameSet)) * scale * 1.5)        // 50% overhead for names

	// Ensure minimums
	if estimatedCountries < 300 {
		estimatedCountries = 300
	}
	if estimatedNames < 100000 {
		estimatedNames = 100000
	}

	return estimatedCountries, estimatedNames
}

// NewNameFinder creates a new NameFinder instance with default capacity
func NewNameFinder() *Finder {
	return NewFinderWithCapacity(300, 100000)
}

// NewFinderWithCapacity creates a new NameFinder with pre-allocated capacity
// Uses Go 1.22+ best practice: pre-allocate map capacity to avoid resizing
func NewFinderWithCapacity(countries, names int) *Finder {
	return &Finder{
		InvertedIndex: make(map[string]map[string][]*city.City, countries),
		fuzzyCache:    make(map[string]*fuzzySearchResult, 100), // Pre-allocate cache capacity
	}
}

// processBatchStreamlined processes a batch of cities with minimal overhead for bulk loading
func (nf *Finder) processBatchStreamlined(cities []city.SpatialCity) {
	// Direct processing without temporary arrays or memory pools
	// This reduces memory allocations and function call overhead
	// Cache country map to reduce lookups
	var cachedCountryMap map[string][]*city.City
	var cachedCountry string

	for i := range cities {
		spatialCity := &cities[i]
		// Copy the City value to its own heap allocation and index that.
		// Pointing the index at &spatialCity.City would be an interior pointer
		// into the loader-owned []SpatialCity backing array, pinning the whole
		// array (~80 B per city) for the index's lifetime even though only the
		// 48 B City values are needed. The string fields stay shared with the
		// loader slice, so the copy itself is cheap.
		cityCopy := spatialCity.City
		cityPtr := &cityCopy

		// Use string interning for memory optimization
		internedCountry := internString(spatialCity.Country)
		internedPrimaryName := internString(spatialCity.Name)

		// Cache country map lookup to reduce map access overhead
		if internedCountry != cachedCountry {
			cachedCountry = internedCountry
			var exists bool
			cachedCountryMap, exists = nf.InvertedIndex[internedCountry]
			if !exists {
				cachedCountryMap = make(map[string][]*city.City, 1000)
				nf.InvertedIndex[internedCountry] = cachedCountryMap
			}
		}

		// Process primary name with cached country map
		nf.addNameToIndexWithMap(cachedCountryMap, internedPrimaryName, cityPtr)

		// Process alternate names with interning
		for _, altName := range spatialCity.AltNames {
			internedAltName := internString(altName)
			nf.addNameToIndexWithMap(cachedCountryMap, internedAltName, cityPtr)
		}
	}
}

// processBatchConcurrent processes cities concurrently using worker pools
// Uses lock-free per-worker indices that are merged at the end to minimize contention
func (nf *Finder) processBatchConcurrent(cities []city.SpatialCity, numWorkers int) {
	// Divide cities into chunks for parallel processing
	chunkSize := (len(cities) + numWorkers - 1) / numWorkers
	if chunkSize < 1000 {
		// For small chunks, sequential is faster
		nf.processBatchStreamlined(cities)
		return
	}

	// Create worker channels
	type workItem struct {
		cities []city.SpatialCity
	}
	workChan := make(chan workItem, numWorkers)
	mergeChan := make(chan map[string]map[string][]*city.City, numWorkers)
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker builds a partial index (lock-free)
			partialIndex := make(map[string]map[string][]*city.City, 300)

			for work := range workChan {
				// Process chunk with local index
				for j := range work.cities {
					spatialCity := &work.cities[j]
					// Heap-copy the City value instead of indexing an interior
					// pointer into the loader slice; see the note in
					// processBatchStreamlined for why pinning must be avoided.
					cityCopy := spatialCity.City
					cityPtr := &cityCopy

					// Use string interning for memory optimization
					internedCountry := internString(spatialCity.Country)
					internedPrimaryName := internString(spatialCity.Name)

					// Get or create country map in local index
					countryMap, exists := partialIndex[internedCountry]
					if !exists {
						countryMap = make(map[string][]*city.City, 1000)
						partialIndex[internedCountry] = countryMap
					}

					// Add primary name
					nf.addNameToIndexWithMap(countryMap, internedPrimaryName, cityPtr)

					// Add alternate names with interning
					for _, altName := range spatialCity.AltNames {
						internedAltName := internString(altName)
						nf.addNameToIndexWithMap(countryMap, internedAltName, cityPtr)
					}
				}
			}

			// Send partial index for merging (non-blocking)
			mergeChan <- partialIndex
		}()
	}

	// Start merger goroutine to combine partial indices
	var mergeWg sync.WaitGroup
	mergeWg.Add(1)
	go func() {
		defer mergeWg.Done()
		// Collect all partial indices
		partialIndices := make([]map[string]map[string][]*city.City, 0, numWorkers)
		for i := 0; i < numWorkers; i++ {
			partialIndices = append(partialIndices, <-mergeChan)
		}

		// Merge all partial indices into main index (single lock acquisition)
		nf.mutex.Lock()
		for _, partialIndex := range partialIndices {
			for country, countryMap := range partialIndex {
				mainCountryMap, exists := nf.InvertedIndex[country]
				if !exists {
					nf.InvertedIndex[country] = countryMap
				} else {
					// Merge maps efficiently
					for name, cityList := range countryMap {
						mainCityList, exists := mainCountryMap[name]
						if !exists {
							mainCountryMap[name] = cityList
						} else {
							// Pre-allocate merged slice to avoid multiple reallocations
							merged := make([]*city.City, len(mainCityList), len(mainCityList)+len(cityList))
							copy(merged, mainCityList)
							mainCountryMap[name] = append(merged, cityList...)
						}
					}
				}
			}
		}
		nf.mutex.Unlock()
	}()

	// Send work chunks to workers
	for i := 0; i < len(cities); i += chunkSize {
		end := i + chunkSize
		if end > len(cities) {
			end = len(cities)
		}
		workChan <- workItem{cities: cities[i:end]}
	}
	close(workChan)

	// Wait for all workers to complete
	wg.Wait()
	close(mergeChan)

	// Wait for merger to complete
	mergeWg.Wait()
}

// getMemoryUsageMB returns current memory usage in MB
func getMemoryUsageMB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.Alloc) / 1024 / 1024
}

// BuildIndex creates a name index from city data using highly optimized concurrent batch processing
// Uses Go 1.22+ optimizations: concurrent processing, better capacity estimation, and memory efficiency
func BuildIndex(cities []city.SpatialCity) *Finder {
	fmt.Printf("Building name index with %d cities using concurrent batch processing\n", len(cities))
	start := time.Now()

	// Improved capacity estimation based on actual data patterns (Go 1.22+ best practice)
	estimatedCountries, estimatedNames := estimateCapacity(cities)
	finder := NewFinderWithCapacity(estimatedCountries, estimatedNames)

	// Use concurrent processing for better CPU utilization
	numWorkers := runtime.NumCPU()
	if numWorkers > 8 {
		numWorkers = 8 // Cap at 8 to avoid excessive contention
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	// For small datasets, use sequential processing to avoid overhead
	if len(cities) < 10000 {
		finder.processBatchStreamlined(cities)
	} else {
		// Concurrent processing for larger datasets
		finder.processBatchConcurrent(cities, numWorkers)
	}

	// The fuzzy n-gram index is derived lazily from the inverted index on
	// first fuzzy lookup; the bulk build path does not pre-compute it.

	// Quick final cleanup
	runtime.GC()

	totalDuration := time.Since(start)
	throughput := float64(len(cities)) / totalDuration.Seconds()
	fmt.Printf("Name index built in %v (throughput: %.0f cities/sec, %.1f MB memory)\n",
		totalDuration, throughput, float64(getMemoryUsageMB()))

	return finder
}

// AddCity adds a city to the NameFinder (thread-safe)
func (nf *Finder) AddCity(spatialCity city.SpatialCity) {
	names := append(spatialCity.AltNames, spatialCity.Name)
	nf.mutex.Lock()
	for _, name := range names {
		if _, exists := nf.InvertedIndex[spatialCity.Country]; !exists {
			nf.InvertedIndex[spatialCity.Country] = make(map[string][]*city.City)
		}
		nf.InvertedIndex[spatialCity.Country][name] = append(nf.InvertedIndex[spatialCity.Country][name], &spatialCity.City)
		// The n-gram index is an immutable CSR that cannot take incremental
		// inserts, so names arriving after the last fuzzy build land in a
		// small overflow list that fuzzy searches scan linearly for the
		// Finder's lifetime — once fuzzyBuilt is terminal there is no later
		// rebuild to fold them into (AddCity has no production callers
		// today; results stay correct because the overflow is always
		// scanned). Before the first build the append is harmless: the
		// build's name snapshot supersedes it.
		nf.fuzzyOverflow = append(nf.fuzzyOverflow, name)
	}
	nf.mutex.Unlock()
}

// addNameToIndexDirect adds a single name-city pair to the index with minimal overhead
// Uses Go 1.22+ best practice: pre-allocate map capacity to avoid resizing
func (nf *Finder) addNameToIndexDirect(country, name string, cityPtr *city.City) {
	countryMap, exists := nf.InvertedIndex[country]
	if !exists {
		// Pre-allocate with reasonable capacity (Go 1.22+ best practice)
		countryMap = make(map[string][]*city.City, 1000)
		nf.InvertedIndex[country] = countryMap
	}

	cityList, exists := countryMap[name]
	if !exists {
		// Pre-allocate slice with small initial capacity (Go 1.22+ best practice)
		// Most city names are unique, so small capacity is appropriate
		cityList = make([]*city.City, 0, 4)
		countryMap[name] = cityList
	}

	// Append the city pointer
	countryMap[name] = append(cityList, cityPtr)
}

// addNameToIndexWithMap adds a name-city pair using a pre-fetched country map
// This reduces map lookups in the hot path
func (nf *Finder) addNameToIndexWithMap(countryMap map[string][]*city.City, name string, cityPtr *city.City) {
	cityList, exists := countryMap[name]
	if !exists {
		// Pre-allocate slice with small initial capacity
		cityList = make([]*city.City, 0, 4)
		countryMap[name] = cityList
	}

	// Append the city pointer
	countryMap[name] = append(cityList, cityPtr)
}

// namesFromIndex returns the union of every indexed name across all countries.
// The inverted index is the source of truth — every insertion path (batch
// build, AddCity) writes it — so this is always the complete name set.
func (nf *Finder) namesFromIndex() []string {
	total := 0
	for _, countryMap := range nf.InvertedIndex {
		total += len(countryMap)
	}

	nameSet := make(map[string]struct{}, total)
	for _, countryMap := range nf.InvertedIndex {
		for name := range countryMap {
			nameSet[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	return names
}

// totalIndexKeys returns the total number of (country, name) keys across the
// inverted index — the gate metric for FuzzyMaxNames. O(#countries) map-len
// additions, no allocations. The caller must hold nf.mutex (read or write):
// AddCity and the concurrent batch merger mutate the inner maps under the
// write lock.
func (nf *Finder) totalIndexKeys() int {
	total := 0
	for _, countryMap := range nf.InvertedIndex {
		total += len(countryMap)
	}
	return total
}

// ensureFuzzyBuilt lazily brings the fuzzy index to a terminal state: built,
// or disabled when the index exceeds FuzzyMaxNames. It is safe to call from
// any lookup path — the common case is one atomic load — and a build already
// in progress is neither waited on nor duplicated: concurrent callers simply
// observe a not-ready index for that one request.
//
// The build itself deliberately holds NO lock. Only the name snapshot (RLock)
// and the final pointer swap (Lock, microseconds) take the mutex, so exact
// lookups keep flowing while the n-gram index is under construction.
func (nf *Finder) ensureFuzzyBuilt() {
	switch nf.fuzzyState.Load() {
	case fuzzyBuilt, fuzzyDisabled:
		return
	case fuzzyBuilding:
		// Another goroutine is mid-build. Waiting is what stalled lookups
		// in the BK-tree era; duplicating the work doubles it. Report
		// not-ready instead.
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

	// Lock-free construction of an immutable structure: readers can neither
	// observe a half-built index nor be blocked by the build.
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
	// index legitimately stays unbuilt so later AddCity growth can trigger
	// a build.
	if committed && len(names) > 0 {
		nf.fuzzyState.Store(fuzzyBuilt)
	} else {
		// Uncommitted (AddCity raced the snapshot) or empty index: the next
		// fuzzy attempt retries with fresher data.
		nf.fuzzyState.Store(fuzzyNotBuilt)
	}
}

// fuzzyCandidates returns every name within maxDistance of query, from the
// immutable n-gram index plus the post-build overflow list. The caller must
// handle caching; this is the uncached core of getCachedFuzzySearch.
//
// The read lock is scoped around the candidate generation: AddCity mutates
// the overflow list and can swap nf.ngrams under the write lock, so an
// unlocked read would race. Concurrent readers — the common case at query
// time — do not block each other.
func (nf *Finder) fuzzyCandidates(query string, maxDistance int) []string {
	nf.mutex.RLock()
	candidates := nf.ngrams.search(query, maxDistance)
	overflow := nf.fuzzyOverflow
	nf.mutex.RUnlock()

	if len(overflow) == 0 {
		return candidates
	}
	for _, name := range overflow {
		if levenshtein.ComputeDistance(query, name) <= maxDistance {
			candidates = append(candidates, name)
		}
	}
	return candidates
}

// getCachedFuzzySearch performs fuzzy search with caching
func (nf *Finder) getCachedFuzzySearch(query string, maxDistance int) []string {
	cacheKey := fmt.Sprintf("%s_%d", query, maxDistance)

	nf.cacheMutex.RLock()
	if cached, exists := nf.fuzzyCache[cacheKey]; exists {
		// Check if cache is still valid (not too old)
		if time.Since(cached.timestamp) < fuzzyCacheTTL {
			nf.cacheMutex.RUnlock()
			return cached.candidates
		}
	}
	nf.cacheMutex.RUnlock()

	// Ensure the fuzzy index is built before searching. While a build is in
	// flight (or matching is disabled over the FuzzyMaxNames threshold)
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
	candidates := nf.fuzzyCandidates(query, maxDistance)

	// Cache the result
	nf.cacheMutex.Lock()
	if len(nf.fuzzyCache) >= maxFuzzyCacheEntries {
		nf.evictFuzzyCacheLocked()
	}
	nf.fuzzyCache[cacheKey] = &fuzzySearchResult{
		candidates: candidates,
		timestamp:  time.Now(),
	}
	nf.cacheMutex.Unlock()

	return candidates
}

// evictFuzzyCacheLocked makes room for one new cache entry. Expired entries
// are dropped first; if the cache is still at capacity the oldest entry is
// evicted (linear scan, bounded by maxFuzzyCacheEntries). The caller must
// hold cacheMutex for writing.
func (nf *Finder) evictFuzzyCacheLocked() {
	now := time.Now()
	for key, cached := range nf.fuzzyCache {
		if now.Sub(cached.timestamp) >= fuzzyCacheTTL {
			delete(nf.fuzzyCache, key)
		}
	}
	if len(nf.fuzzyCache) < maxFuzzyCacheEntries {
		return
	}

	oldestKey := ""
	var oldest time.Time
	found := false
	for key, cached := range nf.fuzzyCache {
		if !found || cached.timestamp.Before(oldest) {
			oldestKey, oldest, found = key, cached.timestamp, true
		}
	}
	if found {
		delete(nf.fuzzyCache, oldestKey)
	}
}

// CityByName finds the coordinates of a city by its name using hybrid search strategy
func (nf *Finder) CityByName(name string, countryCode string) *city.City {
	// Phase 1: Try exact match first (fastest)
	nf.mutex.RLock()
	if cities, exists := nf.InvertedIndex[countryCode][name]; exists && len(cities) > 0 {
		nf.mutex.RUnlock()
		return cities[0]
	}
	nf.mutex.RUnlock()

	// Phases 2/3: approximate matching as a pre-filter, then full fuzzy. Both
	// are cheap no-ops unless the fuzzy index is actually built: over the
	// FuzzyMaxNames threshold matching is disabled (exact-only) for this
	// Finder's lifetime, and while another goroutine is still building the
	// index this lookup reports not-ready rather than waiting. Either way no
	// fuzzy work runs and no empty result pollutes the cache.
	nf.ensureFuzzyBuilt()
	if nf.fuzzyState.Load() == fuzzyBuilt {
		fastCandidates := nf.getCachedFuzzySearch(name, 1) // Use tighter threshold for speed

		// Every read lock is scoped tightly around the map lookups only: holding
		// one across getCachedFuzzySearch deadlocks, because a cold cache takes
		// the write lock inside ensureFuzzyBuilt while this goroutine still
		// holds the read lock (RWMutex self-deadlock).
		if len(fastCandidates) > 0 {
			nf.mutex.RLock()
			for _, candidate := range fastCandidates {
				if cities, exists := nf.InvertedIndex[countryCode][candidate]; exists && len(cities) > 0 {
					nf.mutex.RUnlock()
					return cities[0]
				}
			}
			nf.mutex.RUnlock()
		}

		// Phase 3: Fall back to full fuzzy search with distance 2
		fullCandidates := nf.getCachedFuzzySearch(name, 2)

		if len(fullCandidates) > 0 {
			nf.mutex.RLock()
			for _, candidate := range fullCandidates {
				if cities, exists := nf.InvertedIndex[countryCode][candidate]; exists && len(cities) > 0 {
					nf.mutex.RUnlock()
					return cities[0]
				}
			}
			nf.mutex.RUnlock()
		}
	}

	return nil
}

// indexHeader is the first value written into the serialized stream. It lets
// DeserializeIndex reject files written by an incompatible build (magic or
// version mismatch) up front, with an error that tells the caller to rebuild
// instead of failing halfway through a half-understood payload.
type indexHeader struct {
	Magic   string
	Version uint32
	Count   int
}

const (
	// nameIndexMagic identifies name index files.
	nameIndexMagic = "CFNAMEIDX"
	// nameIndexVersion is the current on-disk format version.
	//
	// v2 replaced the gob-encoded map[string]map[string][]*city.City (which
	// duplicates every City value per reference — 1.6 GB and ~54 s of warm
	// start at Oct-2026 GeoNames scale) with a distinct-city table plus int32
	// references. It also dropped the BK-tree/isBKTreeBuilt/allNames trailer:
	// those are lazy-build runtime state. A v1 file fails the version check
	// below with ErrCorruptIndex; the initializer rebuilds it from source.
	nameIndexVersion = uint32(2)
)

// ErrCorruptIndex reports an index file that cannot be trusted: truncated,
// undecodable, or written by an incompatible format version. The initializer
// treats it (and only it) as rebuildable; any other error — a wrapped fs
// error from an unreadable file, for example — is fatal.
var ErrCorruptIndex = errors.New("name index file is corrupt or incompatible")

// nameIndexPayloadV2 is the v2 on-disk payload: every distinct city exactly
// once, plus the inverted index reduced to int32 city ids.
//
// Cities[i] is city id i. Refs maps country -> name -> ids into Cities, in the
// same order the in-memory index held the pointers. Serializing ids instead of
// pointers is what makes pointer sharing survive a round trip: gob has no
// pointer identity, so v1's struct-per-reference encoding ballooned the file
// and forced the initializer to decode 35M City values for 13.47M distinct
// cities.
type nameIndexPayloadV2 struct {
	Cities []city.City
	Refs   map[string]map[string][]int32
}

// The v2 payload stream is zstd-framed: the file layout is
//
//	gob(indexHeader)                      // uncompressed, so version checks
//	                                     // (including v1 rejection) happen
//	                                     // before any decompression
//	zstd-frame(gob(nameIndexPayloadV2))   // one frame, SpeedFastest, CRC
//
// zstd is what brings the file under the 800 MB sprint gate: the raw gob
// payload measures 1,019,093,377 B at prod scale (546 MB city table + 318 MB
// reference keys + 155 MB ids), 560 MB compressed. The frame occupies the
// rest of the file; a truncated or corrupted frame, or a payload that is not
// a valid frame at all, decodes to ErrCorruptIndex exactly like a malformed
// raw gob stream did.
const (
	// nameIndexZstdLevel trades compression ratio for encode/decode speed:
	// SpeedFastest yields 559.9 MiB at prod scale (30% under the gate) with
	// ~1.8 s decompress; SpeedDefault would save ~55 MB more but cost decode
	// time on every warm start.
	nameIndexZstdLevel = zstd.SpeedFastest
)

// nameIndexZstdCRC enables the per-frame checksum: corruption inside an
// otherwise structurally valid frame is then detected deterministically by
// the decoder instead of surfacing as garbage that happens to fail (or not
// fail) the gob layer.
const nameIndexZstdCRC = true

// decodeZstdFrame decompresses one complete zstd frame with a per-call
// decoder that is closed immediately afterwards. A shared package-level
// decoder was measured to retain ~900 MB of internal window/worker buffers
// after decoding the prod-scale frame (the decoder caches them for reuse);
// warm start is a once-per-boot operation, so paying decoder setup (µs) to
// release that memory is strictly better. DecodeAll itself is stateless.
func decodeZstdFrame(compressed []byte) ([]byte, error) {
	zd, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer zd.Close()
	return zd.DecodeAll(compressed, nil)
}

// SerializeIndex saves the name index to a file.
// The payload is written to a sibling ".part" file first and moved into place
// with os.Rename only after the full stream has been written, so a crash
// mid-write can never leave a truncated file where the index used to be.
//
// The BK-tree, isBKTreeBuilt, and allNames are runtime state and are NOT
// written: v2 always rebuilds the fuzzy structure lazily on first lookup. Map
// iteration order makes the file bytes non-deterministic — acceptable, indexes
// are regenerable artifacts gated by count validation and round-trip tests,
// not byte comparison.
func (nf *Finder) SerializeIndex(filepath string) error {
	nf.mutex.Lock()
	payload := nf.buildPayloadV2Locked()
	count := len(nf.InvertedIndex)
	nf.mutex.Unlock()

	partPath := filepath + ".part"
	file, err := os.Create(partPath)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = file.Close()
		_ = os.Remove(partPath)
		return err
	}

	// The header stays uncompressed (see the framing comment above); the
	// payload is one zstd frame. A fresh encoder per call: encoders are not
	// reusable, and serialization is a one-shot init-path operation.
	encoder := gob.NewEncoder(file)
	header := indexHeader{
		Magic:   nameIndexMagic,
		Version: nameIndexVersion,
		Count:   count,
	}
	if err := encoder.Encode(&header); err != nil {
		return fail(err)
	}
	zw, err := zstd.NewWriter(file, zstd.WithEncoderLevel(nameIndexZstdLevel), zstd.WithEncoderCRC(nameIndexZstdCRC))
	if err != nil {
		return fail(err)
	}
	if err := gob.NewEncoder(zw).Encode(&payload); err != nil {
		_ = zw.Close()
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(partPath)
		return err
	}
	if err := os.Rename(partPath, filepath); err != nil {
		_ = os.Remove(partPath)
		return err
	}
	return nil
}

// buildPayloadV2Locked reduces the inverted index to the v2 payload. The
// caller must hold nf.mutex: the index maps are read while ids are assigned by
// pointer identity, so a concurrent AddCity would race the walk.
func (nf *Finder) buildPayloadV2Locked() nameIndexPayloadV2 {
	ids := make(map[*city.City]int32)
	payload := nameIndexPayloadV2{
		Refs: make(map[string]map[string][]int32, len(nf.InvertedIndex)),
	}
	for country, countryMap := range nf.InvertedIndex {
		refs := make(map[string][]int32, len(countryMap))
		for name, cityList := range countryMap {
			idList := make([]int32, len(cityList))
			for i, c := range cityList {
				id, exists := ids[c]
				if !exists {
					// First encounter of this pointer: append the value and
					// remember its id. Two equal City values under different
					// pointers are two entries — identity, not equality.
					id = int32(len(payload.Cities))
					ids[c] = id
					payload.Cities = append(payload.Cities, *c)
				}
				idList[i] = id
			}
			refs[name] = idList
		}
		payload.Refs[country] = refs
	}
	return payload
}

// DeserializeIndex loads the name index from a file.
// The stream layout is gob(indexHeader) followed by one zstd frame holding
// gob(nameIndexPayloadV2) — see the framing comment near nameIndexPayloadV2.
// The header must be readable and compatible (magic and version, including
// the v1 rejection) before any decompression runs; a missing or mismatched
// header yields an error that suggests deleting the file so the index gets
// rebuilt. Every decode failure — bad header, format mismatch, truncated or
// corrupted zstd frame, or malformed payload — wraps ErrCorruptIndex so
// callers can distinguish rebuildable corruption from environmental errors
// (open/close and read failures are returned unwrapped).
//
// Rehydration decodes each City exactly once and points every reference at
// &cities[id] through a pointer table — one pointer store per reference, no
// per-ref struct allocation, and the pre-serialization sharing semantics are
// restored: the city under two names is one pointer again.
func DeserializeIndex(filepath string) (*Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}

	// The bufio.Reader is shared by the header gob decoder and the payload
	// read: gob consumes exactly the header's bytes, and whatever it buffered
	// past them belongs to the zstd frame.
	bufFile := bufio.NewReader(file)
	decoder := gob.NewDecoder(bufFile)
	var header indexHeader
	if err := decoder.Decode(&header); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: name index %s is not a readable versioned index (legacy or corrupt file: %v); delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, err)
	}
	if header.Magic != nameIndexMagic || header.Version != nameIndexVersion {
		_ = file.Close()
		return nil, fmt.Errorf("%w: name index %s format mismatch: got magic %q version %d, want magic %q version %d; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, header.Magic, header.Version, nameIndexMagic, nameIndexVersion)
	}

	// Payload: read the rest of the file (the compressed frame), decompress
	// it whole, then gob-decode from memory. DecodeAll rather than a
	// streaming reader keeps the decompress and gob phases separately
	// measurable — the timing split is logged below.
	readStart := time.Now()
	compressed, err := io.ReadAll(bufFile)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("reading name index payload from %s: %w", filepath, err)
	}
	compressedLen := len(compressed)
	zstdStart := time.Now()
	payloadBytes, err := decodeZstdFrame(compressed)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: name index %s payload is not a decodable zstd frame: %v; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, err)
	}
	zstdDone := time.Now()
	compressed = nil // release the compressed buffer before the gob decode allocates
	gobStart := zstdDone
	var payload nameIndexPayloadV2
	if err := gob.NewDecoder(bytes.NewReader(payloadBytes)).Decode(&payload); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: decoding name index payload from %s: %v", ErrCorruptIndex, filepath, err)
	}
	if header.Count != len(payload.Refs) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: name index %s payload holds %d countries but the header recorded %d; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, len(payload.Refs), header.Count)
	}
	gobDone := time.Now()

	if err := file.Close(); err != nil {
		return nil, err
	}
	log.Printf("name index %s decoded: read %d B in %s, zstd %d->%d B in %s, gob %s",
		filepath, compressedLen, zstdStart.Sub(readStart), compressedLen, len(payloadBytes),
		zstdDone.Sub(zstdStart), gobDone.Sub(gobStart))

	// gob allocates a fresh backing for every decoded string, so each City
	// carries its own copy of a country code shared by millions of cities.
	// One intern pass over the distinct-city table (13.47M calls at prod
	// scale, versus v1's 70M over every reference) collapses those to one
	// backing per country. Names stay per-city: they are mostly distinct, and
	// interning near-unique values only grows the unique-package table.
	internDecodedCountries(payload.Cities)

	// Pointer table + one-pass rehydration. The table is built before the
	// maps so no reference can observe a partially filled entry.
	ptrs := make([]*city.City, len(payload.Cities))
	for i := range payload.Cities {
		ptrs[i] = &payload.Cities[i]
	}

	finder := NewNameFinder()
	for country, refs := range payload.Refs {
		countryMap := make(map[string][]*city.City, len(refs))
		for name, idList := range refs {
			cityList := make([]*city.City, len(idList))
			for i, id := range idList {
				if id < 0 || int(id) >= len(ptrs) {
					return nil, fmt.Errorf("%w: name index %s references city id %d outside the %d-city table; delete the file so the index is rebuilt",
						ErrCorruptIndex, filepath, id, len(ptrs))
				}
				cityList[i] = ptrs[id]
			}
			countryMap[name] = cityList
		}
		finder.InvertedIndex[country] = countryMap
	}

	// Fuzzy state starts fresh: the BK-tree is deliberately not serialized,
	// so the first fuzzy lookup rebuilds it lazily from the index (including
	// the FuzzyMaxNames gate, which disables matching on an over-threshold
	// warm start without ever building).
	return finder, nil
}

// internDecodedCountries interns the Country field of every decoded city.
// gob transmits each string occurrence with its own backing array, so the
// distinct-city table carries ~13.47M copies of ~250 country codes; the pass
// collapses them to one shared backing per country. It must run before the
// finder is shared (DeserializeIndex is the only caller, pre-rehydration).
func internDecodedCountries(cities []city.City) {
	for i := range cities {
		cities[i].Country = internString(cities[i].Country)
	}
}
