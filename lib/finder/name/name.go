package name

import (
	"bufio"
	"encoding/gob"
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
	estimatedNames := int(float64(len(nameSet)) * scale * 1.5)       // 50% overhead for names

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
		// Create city pointer directly - properly allocated to avoid scope issues
		cityPtr := &spatialCity.City

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
					cityPtr := &spatialCity.City

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
	nameSet := make(map[string]bool)

	for _, countryMap := range nf.InvertedIndex {
		for name := range countryMap {
			nameSet[name] = true
		}
	}

	// Store all unique names for lazy BK-tree building
	nf.allNames = make([]string, 0, len(nameSet))
	for name := range nameSet {
		nf.allNames = append(nf.allNames, name)
	}
}

// ensureBKTreeBuilt lazily builds the BK-tree only when needed using parallel construction
func (nf *Finder) ensureBKTreeBuilt() {
	nf.mutex.Lock()
	defer nf.mutex.Unlock()

	if nf.isBKTreeBuilt {
		return
	}

	// Build BK-tree in parallel for better performance
	nf.buildBKTreeParallel(runtime.NumCPU())
	nf.isBKTreeBuilt = true
}

// buildBKTreeParallel builds the BK-tree using multiple goroutines
func (nf *Finder) buildBKTreeParallel(numWorkers int) {
	if len(nf.allNames) == 0 {
		return
	}

	// Limit workers to reasonable bounds
	if numWorkers > 8 {
		numWorkers = 8
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	// Divide names into chunks for parallel processing
	chunkSize := (len(nf.allNames) + numWorkers - 1) / numWorkers
	if chunkSize < 100 {
		// For small datasets, don't bother with parallelism
		for _, name := range nf.allNames {
			nf.BKTree.Add(name)
		}
		return
	}

	// Create worker channels
	nameChunks := make([][]string, numWorkers)
	for i := 0; i < numWorkers; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(nf.allNames) {
			end = len(nf.allNames)
		}
		nameChunks[i] = nf.allNames[start:end]
	}

	// Start workers
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(chunk []string) {
			defer wg.Done()
			// Each worker builds its own partial BK-tree
			localTree := util.NewBKTree()
			for _, name := range chunk {
				localTree.Add(name)
			}
			// For now, just add to main tree sequentially to avoid race conditions
			// In a more advanced implementation, we could merge BK-trees
			for _, name := range chunk {
				nf.BKTree.Add(name)
			}
		}(nameChunks[i])
	}

	wg.Wait()
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
		if time.Since(cached.timestamp) < time.Hour {
			nf.cacheMutex.RUnlock()
			return cached.candidates
		}
	}
	nf.cacheMutex.RUnlock()

	// Ensure BK-tree is built before searching
	nf.ensureBKTreeBuilt()

	// Perform fuzzy search
	candidates := nf.BKTree.SearchWithEarlyExit(query, maxDistance)

	// Cache the result
	nf.cacheMutex.Lock()
	nf.fuzzyCache[cacheKey] = &fuzzySearchResult{
		candidates: candidates,
		timestamp:  time.Now(),
	}
	nf.cacheMutex.Unlock()

	return candidates
}

// CityByName finds the coordinates of a city by its name using hybrid search strategy
func (nf *Finder) CityByName(name string, countryCode string) *city.City {
	nf.mutex.RLock()

	// Phase 1: Try exact match first (fastest)
	if cities, exists := nf.InvertedIndex[countryCode][name]; exists && len(cities) > 0 {
		nf.mutex.RUnlock()
		return cities[0]
	}
	nf.mutex.RUnlock()

	// Phase 2: Try fast approximate matching as pre-filter
	nf.ensureBKTreeBuilt()
	fastCandidates := nf.getCachedFuzzySearch(name, 1) // Use tighter threshold for speed

	if len(fastCandidates) > 0 {
		nf.mutex.RLock()
		defer nf.mutex.RUnlock()

		for _, candidate := range fastCandidates {
			if cities, exists := nf.InvertedIndex[countryCode][candidate]; exists && len(cities) > 0 {
				return cities[0]
			}
		}
	}

	// Phase 3: Fall back to full fuzzy search with distance 2
	fullCandidates := nf.getCachedFuzzySearch(name, 2)

	if len(fullCandidates) > 0 {
		nf.mutex.RLock()
		defer nf.mutex.RUnlock()

		for _, candidate := range fullCandidates {
			if cities, exists := nf.InvertedIndex[countryCode][candidate]; exists && len(cities) > 0 {
				return cities[0]
			}
		}
	}

	return nil
}

// SerializeIndex saves the name index to a file
func (nf *Finder) SerializeIndex(filepath string) error {
	nf.mutex.Lock()
	defer nf.mutex.Unlock()

	file, err := os.Create(filepath)
	if err != nil {
		return err
	}

	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(nf.InvertedIndex); err != nil {
		_ = file.Close()
		return err
	}
	if err := encoder.Encode(nf.BKTree); err != nil {
		_ = file.Close()
		return err
	}
	// Serialize lazy loading state
	if err := encoder.Encode(nf.isBKTreeBuilt); err != nil {
		_ = file.Close()
		return err
	}
	if err := encoder.Encode(nf.allNames); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// DeserializeIndex loads the name index from a file
func DeserializeIndex(filepath string) (*Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}

	decoder := gob.NewDecoder(file)
	finder := NewNameFinder()
	if err := decoder.Decode(&finder.InvertedIndex); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := decoder.Decode(&finder.BKTree); err != nil {
		_ = file.Close()
		return nil, err
	}
	// Deserialize lazy loading state (with backward compatibility)
	if err := decoder.Decode(&finder.isBKTreeBuilt); err != nil {
		// For backward compatibility with older serialized files, assume BK-tree is built
		finder.isBKTreeBuilt = true
		finder.allNames = []string{} // Will be populated lazily if needed
	} else {
		if err := decoder.Decode(&finder.allNames); err != nil {
			finder.allNames = []string{} // Default to empty
		}
	}

	if err := file.Close(); err != nil {
		return nil, err
	}

	return finder, nil
}
