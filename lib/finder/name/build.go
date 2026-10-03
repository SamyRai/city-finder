package name

import (
	"log"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
)

// estimateCapacity estimates the country count a dataset will index, for
// pre-allocating the countries map. It samples the dataset because the
// estimate only steers one small (~250-entry) map allocation.
// (A former name-count half was removed with the nested-map index: the flat
// tables size themselves exactly during the one-time flatten, so a sampled
// name estimate had nothing left to feed.)
func estimateCapacity(cities []city.SpatialCity) int {
	// Sample the dataset to estimate sizes
	sampleSize := len(cities)
	if sampleSize > 10000 {
		sampleSize = 10000
	}

	countrySet := make(map[string]bool, 300)

	// Sample cities to estimate unique counts
	for i := 0; i < sampleSize; i++ {
		countrySet[cities[i].Country] = true
	}

	// Scale estimates based on sample
	scale := float64(len(cities)) / float64(sampleSize)
	estimatedCountries := int(float64(len(countrySet)) * scale * 1.2) // 20% overhead

	// Ensure minimums
	if estimatedCountries < 300 {
		estimatedCountries = 300
	}

	return estimatedCountries
}

// processBatchStreamlined stages a batch of cities into the nested staging
// map: country -> name -> row ids, where cities[i] has row id base+i. Every
// name of a city (primary and alternates) stores the same row id, in input
// order, so each name's list is in load order.
func processBatchStreamlined(index map[string]map[string][]int32, cities []city.SpatialCity, base int32) {
	var cachedCountryMap map[string][]int32
	var cachedCountry string

	for i := range cities {
		spatialCity := &cities[i]
		id := base + int32(i)

		internedCountry := internString(spatialCity.Country)
		if internedCountry != cachedCountry {
			cachedCountry = internedCountry
			var exists bool
			cachedCountryMap, exists = index[internedCountry]
			if !exists {
				cachedCountryMap = make(map[string][]int32, 1000)
				index[internedCountry] = cachedCountryMap
			}
		}

		addNameToMap(cachedCountryMap, internString(spatialCity.Name), id)
		for _, altName := range spatialCity.AltNames {
			addNameToMap(cachedCountryMap, internString(altName), id)
		}
	}
}

// processBatchConcurrent processes cities concurrently: the input is split
// into contiguous chunks, each worker stages its chunks into a private partial
// index (lock-free), and the partials are merged IN CHUNK ORDER. That order is
// what makes every per-name id list come out in load order — the homonym
// order CityByName's first-id-wins resolution and the on-disk format promise.
// (Merging in worker-completion order, as an earlier version did, made the
// winning homonym vary between identical builds.)
func processBatchConcurrent(index map[string]map[string][]int32, cities []city.SpatialCity, numWorkers int) {
	chunkSize := (len(cities) + numWorkers - 1) / numWorkers
	if chunkSize < 1000 {
		// For small chunks, sequential is faster
		processBatchStreamlined(index, cities, 0)
		return
	}
	numChunks := (len(cities) + chunkSize - 1) / chunkSize

	partials := make([]map[string]map[string][]int32, numChunks)
	chunks := make(chan int, numChunks)
	for c := 0; c < numChunks; c++ {
		chunks <- c
	}
	close(chunks)

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range chunks {
				start := c * chunkSize
				end := min(start+chunkSize, len(cities))
				partial := make(map[string]map[string][]int32, 300)
				processBatchStreamlined(partial, cities[start:end], int32(start))
				partials[c] = partial // distinct index per chunk: no lock needed
			}
		}()
	}
	wg.Wait()

	// Deterministic merge: chunk 0 first, so each name's list is the
	// concatenation of its per-chunk lists in input order.
	for _, partial := range partials {
		for country, countryMap := range partial {
			mainCountryMap, exists := index[country]
			if !exists {
				index[country] = countryMap
				continue
			}
			for name, ids := range countryMap {
				mainIDs, exists := mainCountryMap[name]
				if !exists {
					mainCountryMap[name] = ids
					continue
				}
				merged := make([]int32, len(mainIDs), len(mainIDs)+len(ids))
				copy(merged, mainIDs)
				mainCountryMap[name] = append(merged, ids...)
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

// BuildIndex creates a name index from city data using highly optimized concurrent batch processing
// Uses Go 1.22+ optimizations: concurrent processing, better capacity estimation, and memory efficiency
//
// The bulk loaders stage the (country, name, city-pointer) tuples into a
// nested map — the shape their lock-free worker merge is built around — and
// buildFromIndexMap then flattens that staging structure into the sorted CSR
// tables in one pass, after which it is dropped. The nested-map overhead is
// therefore build-transient: it never survives into the resident index.
// opts optionally overrides the fuzzy limits (see Options).
func BuildIndex(cities []city.SpatialCity, opts ...Options) *Finder {
	log.Printf("Building name index with %d cities using concurrent batch processing", len(cities))
	start := time.Now()

	finder := NewFinderWithCapacity(estimateCapacity(cities), opts...)
	finder.cities = ownedCityTable(cities)

	// Staging structure for the loaders; flattened (and freed) below.
	index := make(map[string]map[string][]int32, estimateCapacity(cities))

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
		processBatchStreamlined(index, cities, 0)
	} else {
		// Concurrent processing for larger datasets
		processBatchConcurrent(index, cities, numWorkers)
	}

	finder.buildFromIndexMap(index)
	index = nil

	// The fuzzy n-gram index is derived lazily from the inverted index on
	// first fuzzy lookup; the bulk build path does not pre-compute it.

	// Quick final cleanup
	runtime.GC()

	totalDuration := time.Since(start)
	throughput := float64(len(cities)) / totalDuration.Seconds()
	log.Printf("Name index built in %v (throughput: %.0f cities/sec, %.1f MB memory)",
		totalDuration, throughput, getMemoryUsageMB())

	return finder
}

// buildFromIndexMap flattens a fully loaded staging index into the sorted CSR
// tables, replacing whatever tables the finder held. Only BuildIndex calls
// it, before the finder is published, so no locking is needed.
//
// Homonym order: each name's ids are copied in the staging slice's order,
// which every loader path builds as load order — so the CSR range preserves
// the insertion-order homonym sequence that CityByName's first-id-wins
// resolution has always returned.
func (nf *Finder) buildFromIndexMap(index map[string]map[string][]int32) {
	nf.countries = make(map[string]*nameTable, len(index))
	for country, countryMap := range index {
		nf.countries[country] = buildTable(countryMap)
	}
}

// buildTable flattens one country's name -> ids map into a nameTable: names
// sorted once, ids copied CSR-style in their stored order.
func buildTable(refs map[string][]int32) *nameTable {
	t := &nameTable{names: make([]string, 0, len(refs))}
	for name := range refs {
		t.names = append(t.names, name)
	}
	sort.Strings(t.names)
	total := 0
	for _, name := range t.names {
		total += len(refs[name])
	}
	t.starts = make([]int32, len(t.names)+1)
	t.ids = make([]int32, 0, total)
	for i, name := range t.names {
		t.starts[i] = int32(len(t.ids))
		t.ids = append(t.ids, refs[name]...)
	}
	t.starts[len(t.names)] = int32(len(t.ids))
	return t
}
