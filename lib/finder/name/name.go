package name

import (
	"bufio"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unique"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/util"
	"os"
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

// cityPointerPool reuses City pointers to reduce GC pressure
// Following Go 1.24+ best practices: pre-allocate capacity and reset on Get
// AltNames removed from City struct for memory optimization
var cityPointerPool = sync.Pool{
	New: func() interface{} {
		return &city.City{}
	},
}

// getCityFromPool gets a city from pool and resets it (Go 1.24+ best practice)
// AltNames removed from City struct for memory optimization
func getCityFromPool() *city.City {
	c := cityPointerPool.Get().(*city.City)
	// Reset all fields
	c.Latitude = 0
	c.Longitude = 0
	c.Name = ""
	c.Country = ""
	return c
}

// putCityToPool returns a city to pool after resetting (Go 1.24+ best practice)
// AltNames removed from City struct for memory optimization
func putCityToPool(c *city.City) {
	// Reset state before returning to pool
	c.Latitude = 0
	c.Longitude = 0
	c.Name = ""
	c.Country = ""
	cityPointerPool.Put(c)
}

// slicePool reuses slices to reduce allocations
var slicePool = sync.Pool{
	New: func() interface{} {
		return make([]*city.City, 0, 8) // Pre-allocate capacity
	},
}

// getSliceFromPool gets a slice from pool and resets it
func getSliceFromPool() []*city.City {
	s := slicePool.Get().([]*city.City)
	return s[:0] // Reset length, keep capacity
}

// putSliceToPool returns a slice to pool
func putSliceToPool(s []*city.City) {
	slicePool.Put(s[:0]) // Reset length before returning
}

// Finder is a struct that contains the data for city name lookups
// Struct field ordering optimized for memory alignment (Go 1.22+ best practice):
// - Pointers and maps first (8 bytes on 64-bit)
// - Bools and small fields last (1 byte, but padding matters)
type Finder struct {
	InvertedIndex map[string]map[string][]*city.City // Inverted index for city name lookups by country
	BKTree        *util.BKTree                       // BK-tree for fuzzy city name matching (lazy-loaded)
	fuzzyCache    map[string]*fuzzySearchResult      // Cache for fuzzy search results
	allNames      []string                           // All unique names for lazy BK-tree building
	cacheMutex    sync.RWMutex                       // Mutex for fuzzy search cache
	mutex         sync.RWMutex                       // Mutex for thread-safe operations
	isBKTreeBuilt bool                               // Flag to track if BK-tree has been built
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
		BKTree:        util.NewBKTree(),
		fuzzyCache:    make(map[string]*fuzzySearchResult, 100), // Pre-allocate cache capacity
		isBKTreeBuilt: false,
		allNames:      make([]string, 0, names),
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

// optimizeMemoryLayout performs final optimizations for better cache performance
func (nf *Finder) optimizeMemoryLayout() {
	// Pre-shrink maps to reduce memory overhead
	for _, countryMap := range nf.InvertedIndex {
		for name, cityList := range countryMap {
			// Shrink slice to exact size
			if len(cityList) != cap(cityList) {
				newList := make([]*city.City, len(cityList))
				copy(newList, cityList)
				countryMap[name] = newList
			}
		}
	}
}

// getMemoryUsageMB returns current memory usage in MB
func getMemoryUsageMB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.Alloc) / 1024 / 1024
}

// BuildIndexStreaming creates a name index from an io.Reader, processing cities one by one
// This uses minimal memory and is suitable for very large datasets
func BuildIndexStreaming(reader io.Reader) (*Finder, error) {
	finder := NewFinderWithCapacity(300, 100000)

	scanner := bufio.NewScanner(reader)
	lineNumber := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineNumber++

		// Skip empty lines and comments
		if len(strings.TrimSpace(line)) == 0 || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse the city data (simplified parsing for demonstration)
		// In a real implementation, you'd use the full CSV parsing logic
		fields := strings.Split(line, "\t")
		if len(fields) < 19 { // GeoNames format has 19+ fields
			continue
		}

		// Extract basic city information
		spatialCity := &city.SpatialCity{
			City: city.City{
				Name:      fields[1],
				Latitude:  parseFloat(fields[4]),
				Longitude: parseFloat(fields[5]),
				Country:   fields[8],
			},
		}

		// Process alternate names if available
		if len(fields) > 3 && fields[3] != "" {
			spatialCity.AltNames = strings.Split(fields[3], ",")
		}

		// Add city to index immediately (streaming approach)
		finder.addCityStreaming(spatialCity)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading stream: %v", err)
	}

	// Collect names for lazy BK-tree building (but don't build it yet)
	finder.collectAllNames()

	return finder, nil
}

// addCityStreaming adds a city to the index with minimal memory allocation (for streaming)
// Uses improved sync.Pool pattern with proper reset (Go 1.24+ best practice)
func (nf *Finder) addCityStreaming(spatialCity *city.SpatialCity) {
	// Get a pooled city pointer with proper reset
	cityPtr := getCityFromPool()
	*cityPtr = spatialCity.City

	country := spatialCity.Country
	name := spatialCity.Name

	// Process primary name
	nf.addNameToIndexDirect(country, name, cityPtr)

	// Process alternate names
	for _, altName := range spatialCity.AltNames {
		nf.addNameToIndexDirect(country, altName, cityPtr)
	}

	// Note: We don't return to pool here since cityPtr is stored in the index
	// Pool is only for temporary use, not for long-lived references
}

// parseFloat is a helper for parsing coordinates in streaming mode
func parseFloat(s string) float64 {
	if val, err := strconv.ParseFloat(s, 64); err == nil {
		return val
	}
	return 0.0
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

	// Skip expensive collectAllNames() during bulk loading - do it lazily if needed
	// Skip optimizeMemoryLayout() as it adds significant overhead for minimal benefit

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
	for _, name := range names {
		nf.mutex.Lock()
		if _, exists := nf.InvertedIndex[spatialCity.Country]; !exists {
			nf.InvertedIndex[spatialCity.Country] = make(map[string][]*city.City)
		}
		nf.InvertedIndex[spatialCity.Country][name] = append(nf.InvertedIndex[spatialCity.Country][name], &spatialCity.City)
		nf.BKTree.Add(name)
		nf.mutex.Unlock()
	}
}

// addCityUnsafe adds a city to the NameFinder without mutex locking (for internal use during building)
func (nf *Finder) addCityUnsafe(spatialCity city.SpatialCity) {
	// Create a single city pointer to avoid multiple allocations
	cityPtr := &spatialCity.City

	// Process primary name
	nf.addNameToIndexDirect(spatialCity.Country, spatialCity.Name, cityPtr)

	// Process alternate names
	for _, altName := range spatialCity.AltNames {
		nf.addNameToIndexDirect(spatialCity.Country, altName, cityPtr)
	}
}

// addCityOptimized adds a city to the NameFinder with optimized memory usage and string interning
// Uses Go 1.23's unique package for efficient string interning
func (nf *Finder) addCityOptimized(spatialCity city.SpatialCity) {
	cityPtr := &spatialCity.City

	// Intern strings using Go 1.23's unique package to reduce memory usage
	internedCountry := internString(spatialCity.Country)
	internedPrimaryName := internString(spatialCity.Name)

	// Process primary name
	nf.addNameToIndexDirect(internedCountry, internedPrimaryName, cityPtr)

	// Process alternate names with interning
	for _, altName := range spatialCity.AltNames {
		internedAltName := internString(altName)
		nf.addNameToIndexDirect(internedCountry, internedAltName, cityPtr)
	}
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

// collectAllNames collects all unique names for lazy BK-tree building
func (nf *Finder) collectAllNames() {
	nf.allNames = nf.namesFromIndex()
}

// namesFromIndex returns the union of every indexed name across all countries.
// The inverted index is the source of truth — every insertion path (batch
// build, streaming build, AddCity) writes it — so this is always the complete
// name set, even when allNames is empty (BuildIndex skips collectAllNames) or
// stale (indexes that went through serialization).
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

// ensureBKTreeBuilt lazily builds the BK-tree only when needed. The fast path
// only takes the read lock; the build itself runs under the write lock.
func (nf *Finder) ensureBKTreeBuilt() {
	nf.mutex.RLock()
	built := nf.isBKTreeBuilt
	nf.mutex.RUnlock()
	if built {
		return
	}

	nf.mutex.Lock()
	defer nf.mutex.Unlock()

	if nf.isBKTreeBuilt {
		return
	}

	nf.buildBKTreeSequential()

	// Mark built only when the tree actually holds names. An empty index
	// legitimately stays unbuilt so later AddCity growth can trigger a build.
	if nf.BKTree.Root != nil {
		nf.isBKTreeBuilt = true
	}
}

// buildBKTreeSequential rebuilds the shared BK-tree from the complete name
// set derived from the inverted index. It must be called with nf.mutex held
// for writing (ensureBKTreeBuilt guarantees that).
//
// The previous "parallel" shape spawned workers that each built a throwaway
// local tree and then mutated the shared tree from inside their goroutines —
// unsynchronized concurrent map writes that crash at scale while doing twice
// the Add work. BK-tree insertion is inherently serial (each insert walks the
// shared tree), so a parallel shape that only touched the shared tree after
// wg.Wait() would reduce to exactly this sequential loop.
func (nf *Finder) buildBKTreeSequential() {
	names := nf.namesFromIndex()

	// Rebuild from scratch: names that AddCity already added to the tree
	// incrementally are re-added from the index, which avoids distance-0
	// duplicate nodes from double insertion.
	tree := util.NewBKTree()
	for _, name := range names {
		tree.Add(name)
	}
	nf.BKTree = tree
	nf.allNames = names
}

// buildBKTree ensures the BK-tree is built (for compatibility)
func (nf *Finder) buildBKTree() {
	nf.ensureBKTreeBuilt()
}

// fastApproximateDistance provides a fast approximation of string similarity
// Uses character frequency analysis for O(n) complexity vs O(n*m) for Levenshtein
func fastApproximateDistance(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	// Quick length difference check
	lenDiff := util.Abs(len(a) - len(b))
	if lenDiff > 2 {
		return lenDiff // Too different to be similar
	}

	// Character frequency analysis for common characters
	charCountA := make(map[rune]int)
	charCountB := make(map[rune]int)

	for _, char := range a {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			charCountA[unicode.ToLower(char)]++
		}
	}
	for _, char := range b {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			charCountB[unicode.ToLower(char)]++
		}
	}

	// Calculate character frequency difference
	distance := 0
	for char, countA := range charCountA {
		countB := charCountB[char]
		distance += util.Abs(countA - countB)
	}
	for char, countB := range charCountB {
		if _, exists := charCountA[char]; !exists {
			distance += countB
		}
	}

	// Normalize by string length (shorter strings should have smaller distance)
	maxLen := util.Max(len(a), len(b))
	if maxLen == 0 {
		return 0
	}
	return distance * 10 / maxLen // Scale to be comparable with Levenshtein
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

	// Ensure BK-tree is built before searching
	nf.ensureBKTreeBuilt()

	// Search under the read lock: AddCity mutates the tree under the write
	// lock, so an unlocked search would race with concurrent additions.
	nf.mutex.RLock()
	candidates := nf.BKTree.SearchWithEarlyExit(query, maxDistance)
	nf.mutex.RUnlock()

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

	// Phase 2: Try fast approximate matching as pre-filter
	nf.ensureBKTreeBuilt()
	fastCandidates := nf.getCachedFuzzySearch(name, 1) // Use tighter threshold for speed

	// Every read lock is scoped tightly around the map lookups only: holding
	// one across getCachedFuzzySearch deadlocks, because a cold cache takes
	// the write lock inside ensureBKTreeBuilt while this goroutine still
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
	nameIndexVersion = uint32(1)
)

// SerializeIndex saves the name index to a file.
// The payload is written to a sibling ".part" file first and moved into place
// with os.Rename only after the full stream has been written, so a crash
// mid-write can never leave a truncated file where the index used to be.
func (nf *Finder) SerializeIndex(filepath string) error {
	nf.mutex.Lock()
	defer nf.mutex.Unlock()

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

	encoder := gob.NewEncoder(file)
	header := indexHeader{
		Magic:   nameIndexMagic,
		Version: nameIndexVersion,
		Count:   len(nf.InvertedIndex),
	}
	if err := encoder.Encode(&header); err != nil {
		return fail(err)
	}
	if err := encoder.Encode(nf.InvertedIndex); err != nil {
		return fail(err)
	}
	if err := encoder.Encode(nf.BKTree); err != nil {
		return fail(err)
	}
	// Serialize lazy loading state
	if err := encoder.Encode(nf.isBKTreeBuilt); err != nil {
		return fail(err)
	}
	if err := encoder.Encode(nf.allNames); err != nil {
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

// DeserializeIndex loads the name index from a file.
// The stream must start with a compatible indexHeader; a missing or mismatched
// header (including legacy pre-header files) yields an error that suggests
// deleting the file so the index gets rebuilt.
func DeserializeIndex(filepath string) (*Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}

	decoder := gob.NewDecoder(file)
	var header indexHeader
	if err := decoder.Decode(&header); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("name index %s is not a readable versioned index (legacy or corrupt file: %v); delete the file so the index is rebuilt", filepath, err)
	}
	if header.Magic != nameIndexMagic || header.Version != nameIndexVersion {
		_ = file.Close()
		return nil, fmt.Errorf("name index %s format mismatch: got magic %q version %d, want magic %q version %d; delete the file so the index is rebuilt",
			filepath, header.Magic, header.Version, nameIndexMagic, nameIndexVersion)
	}

	finder := NewNameFinder()
	if err := decoder.Decode(&finder.InvertedIndex); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := decoder.Decode(&finder.BKTree); err != nil {
		_ = file.Close()
		return nil, err
	}
	// Deserialize lazy loading state. Older files that predate these fields
	// simply end the stream here; that is a legacy file, not corruption. Any
	// other decode error must surface instead of being silently swallowed.
	if err := decoder.Decode(&finder.isBKTreeBuilt); err != nil {
		if !isEndOfStream(err) {
			_ = file.Close()
			return nil, fmt.Errorf("decoding name index build state: %w", err)
		}
		finder.allNames = nil
	} else if err := decoder.Decode(&finder.allNames); err != nil {
		if !isEndOfStream(err) {
			_ = file.Close()
			return nil, fmt.Errorf("decoding name index name list: %w", err)
		}
		finder.allNames = nil
	}

	// Reconcile the built flag with the actual tree state:
	// - a populated tree was built by someone; trust it instead of rebuilding.
	// - a "built" flag over an empty tree (persisted by builds whose fuzzy
	//   path never ran) must be cleared or fuzzy search stays dead forever;
	//   the lazy build now derives the name set from the inverted index.
	if finder.BKTree.Root != nil {
		finder.isBKTreeBuilt = true
	} else if finder.isBKTreeBuilt && len(finder.InvertedIndex) > 0 {
		finder.isBKTreeBuilt = false
	}

	if err := file.Close(); err != nil {
		return nil, err
	}

	// gob allocates a fresh copy of every string occurrence it decodes, so the
	// decoded index duplicates name/country strings per reference. Interning
	// the City fields releases those duplicates (measured: ~16% of the decoded
	// heap on a synthetic 200K-city index); the finder is not shared yet, so
	// no locking is needed.
	finder.internDecodedStrings()

	return finder, nil
}

// isEndOfStream reports whether err indicates the gob stream ended (a legacy
// file without the trailing lazy-load fields) rather than being malformed.
func isEndOfStream(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// internDecodedStrings interns the string data produced by gob decoding so the
// warm-started index shares one backing array per distinct name/country, the
// same way the build path does. It must run before the finder is shared
// (DeserializeIndex is the only caller).
//
// Only the City struct fields (plus the allNames list, usually empty) are
// interned, not the inverted-index map keys: internString registers every
// distinct value in the global unique-package table, and on synthetic
// 50K/200K-city indexes the table growth from interning key-only strings
// (alternate names exist only as map keys) was measured to outweigh the freed
// key duplicates, leaving the final heap slightly LARGER. City-field
// interning is where the measured win lives.
func (nf *Finder) internDecodedStrings() {
	nf.internCityFields()
	for i, name := range nf.allNames {
		nf.allNames[i] = internString(name)
	}
}

// internCityFields interns the Name/Country fields of every decoded City.
// gob transmits each pointer occurrence as a full value, so a city referenced
// under several names decodes to several City values each carrying its own
// copies of the strings; this is where most of the decoded duplication lives.
func (nf *Finder) internCityFields() {
	for _, countryMap := range nf.InvertedIndex {
		for _, cityList := range countryMap {
			for _, c := range cityList {
				c.Name = internString(c.Name)
				c.Country = internString(c.Country)
			}
		}
	}
}
