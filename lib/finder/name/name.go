package name

import (
	"container/list"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

// fuzzySearchResult caches fuzzy search results to avoid repeated computations
type fuzzySearchResult struct {
	candidates []string
	timestamp  time.Time
	// age is this entry's element in Finder.fuzzyCacheAge, which orders
	// entries oldest-first so eviction is O(1) instead of a full-map scan.
	age *list.Element
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
// The gate counts total (country, name) keys — the sum of the per-country
// sorted-table name counts plus the overflow keys — not unique names. That
// over-approximates the distinct-name set (a name indexed in N countries
// counts N times), which is intentional: the count is O(#countries) with
// zero allocations, and fuzzy matching is a best-effort enhancement, never a
// correctness requirement.
//
// Default rationale (measured on Apple silicon, Oct 2026 GeoNames): at the
// prod scale of 18,698,093 keys / 17,727,652 distinct names the lazy
// n-gram build takes ~94 s (lock-free, in a background goroutine — the
// triggering lookup returns exact-only rather than waiting it out), the
// structure holds 1.18 GiB resident, and typo queries run at
// distance-1 p50 2.6 ms and distance-2 p50 20.6 ms / p99 749 ms — inside
// the <50 ms p50 target with memory under the 3 GiB budget. The default
// therefore sits above prod with ~34% key headroom. See
// docs/design/index-format-v2.md for the full scale table.
//
// Override it BEFORE the first fuzzy lookup on any Finder: the decision is
// evaluated once per index and is terminal, and the variable is read without
// synchronization.
var FuzzyMaxNames = 25_000_000

// FuzzyMaxCandidates caps the posting-list work a single fuzzy search may
// perform: the number of posting entries the rarest-lists walk may read
// before it stops and returns the results verified so far, best-effort, for
// that one query. It is the v1.1 clamp on the fuzzy tail: at prod scale
// (17.73M names), distance-2 queries whose length/gram filters degenerate —
// short or common-gram queries — were measured walking multi-million-entry
// posting lists for up to ~8 s; the budget bounds that walk per query (the
// default to a few seconds at prod, tighter budgets to tens of
// milliseconds — see the measured sweep in docs/design/index-format-v2.md).
//
// Semantics when the cap trips mid-query: no error, no panic — the search
// returns the matches already verified (every returned name is a true
// Levenshtein match; the cap can only lose results, never fabricate them),
// the result is excluded from the fuzzy cache so it is never served to a
// later query as if complete, and FuzzyBudgetTrips() increments. Exact
// (phase-1) lookups never consult this path and are unaffected.
//
// Default rationale (Apple silicon, Oct 2026 GeoNames, 17.73M names; the
// standard typo workload = 1k real names with 1–2 edits, the adversarial
// workload = 1k one-to-three-rune queries): the standard workload's largest
// posting walk is 3,539,399–3,632,442 entries across two independent 1k
// samples (d2; ~531k at d1) while the adversarial workload's is 4,000,118 —
// the two tails overlap, so no budget can both keep the standard workload
// 100% complete AND clamp the adversarial p99 to ~100 ms; a ~500k budget
// measured 415 ms adversarial d2 p99 but truncated 7.1% of standard d2
// queries. The default sits above the standard workload's observed maximum
// (0/1000 truncated at both distances, in both samples) and is deliberately
// tight rather than generous: every trip is loudly observable via
// FuzzyBudgetTrips() and the one-time log, so an operator with a heavier
// typo workload can raise it on evidence, while a generous default would
// silently weaken the only bound on degenerate queries. Its value at
// today's data is the worst-case guarantee — no fuzzy query exceeds 4M
// posting entries of work — not a p99 improvement; see
// docs/design/index-format-v2.md for the full measured budget sweep. At
// corpora of 1M names and below the cap is dormant (largest measured walk
// 225,469 entries).
//
// Override it BEFORE concurrent fuzzy searches run: the variable is read
// once per search without synchronization, identical to FuzzyMaxNames.
// Negative disables the cap (v1.0 behavior: unbounded walk, full
// completeness up to the q-gram boundary, unbounded tail).
var FuzzyMaxCandidates = 4_000_000

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
	countries     map[string]*nameTable         // per-country exact-lookup tables
	cities        cityTable                     // every id in a table or the overflow resolves here
	overflow      map[string]map[string][]int32 // post-construction additions: country -> name -> city ids into cities
	ngrams        *ngramIndex                   // Immutable q-gram fuzzy index (lazy-built, never serialized)
	fuzzyOverflow []string                      // Names added after the last fuzzy build; linearly scanned until the next build folds them in
	fuzzyCache    map[string]*fuzzySearchResult // Cache for fuzzy search results
	fuzzyCacheAge list.List                     // fuzzyCache keys, oldest insertion first (values are string keys)
	cacheMutex    sync.RWMutex                  // Mutex for fuzzy search cache and fuzzyCacheAge
	mutex         sync.RWMutex                  // Mutex for thread-safe operations
	fuzzyState    atomic.Int32                  // Lazy fuzzy-index state (fuzzyNotBuilt*, above); runtime-only, not serialized
}

// Memory pools removed - they were causing excessive memory usage

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

// NewNameFinder creates a new NameFinder instance with default capacity
func NewNameFinder() *Finder {
	return NewFinderWithCapacity(300)
}

// NewFinderWithCapacity creates a new NameFinder with a pre-allocated
// countries-map capacity. (The former names parameter was removed with the
// nested-map index — the flat tables are sized exactly at build time.)
func NewFinderWithCapacity(countries int) *Finder {
	return &Finder{
		countries:  make(map[string]*nameTable, countries),
		fuzzyCache: make(map[string]*fuzzySearchResult, 100), // Pre-allocate cache capacity
	}
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
func BuildIndex(cities []city.SpatialCity) *Finder {
	log.Printf("Building name index with %d cities using concurrent batch processing", len(cities))
	start := time.Now()

	finder := NewFinderWithCapacity(estimateCapacity(cities))
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

// AddCity adds a city to the NameFinder (thread-safe)
func (nf *Finder) AddCity(spatialCity city.SpatialCity) {
	// A fresh slice, never append(spatialCity.AltNames, ...): when the
	// caller's AltNames has spare capacity, that append would write the
	// primary name into the caller's backing array.
	names := make([]string, 0, len(spatialCity.AltNames)+1)
	names = append(names, spatialCity.AltNames...)
	names = append(names, spatialCity.Name)
	nf.mutex.Lock()
	nf.addOverflowLocked(spatialCity.Country, names, spatialCity.City)
	nf.mutex.Unlock()
}

// addOverflowLocked indexes one city value under every given name through the
// unsorted overflow: the value is copied into the city table's post-build
// extras exactly once, and its id is appended to each name's overflow id
// list — so all the city's names resolve to one pointer exactly like a
// build-time city, and CityByName's sorted-table-first probe order makes a
// post-build homonym resolve to the build-time winner.
//
// Insertion into the sorted tables is O(n) per add, which is why
// post-construction additions live here instead: the overflow is consulted
// after every sorted-table miss (exact lookups) and merged into
// serialization, mirroring the fuzzy overflow design. The caller must hold
// nf.mutex for writing.
func (nf *Finder) addOverflowLocked(country string, names []string, c city.City) {
	id := nf.cities.add(c)

	if nf.overflow == nil {
		nf.overflow = make(map[string]map[string][]int32)
	}
	countryOverflow, exists := nf.overflow[country]
	if !exists {
		countryOverflow = make(map[string][]int32)
		nf.overflow[country] = countryOverflow
	}
	for _, name := range names {
		countryOverflow[name] = append(countryOverflow[name], id)
		// The n-gram index is an immutable CSR that cannot take incremental
		// inserts, so names arriving after the last fuzzy build land in a
		// small overflow list that fuzzy searches scan linearly for the
		// Finder's lifetime — once fuzzyBuilt is terminal there is no later
		// rebuild to fold them into (AddCity has no production callers
		// today; results stay correct because the overflow is always
		// scanned). Before the first build the append is harmless: the
		// build's name snapshot (which includes overflow names) supersedes
		// it.
		nf.fuzzyOverflow = append(nf.fuzzyOverflow, name)
	}
}

// addNameToIndexDirect adds a single name-city pair to the index with minimal overhead
// The bulk build paths stage into a nested map and flatten once (see
// BuildIndex), so a direct add after construction lands in the unsorted
// overflow and is consulted after the sorted-table miss — the same
// correctness contract AddCity's names follow. The city value is copied so
// the distinct-city table keeps its one-fresh-pointer-per-entry invariant.
func (nf *Finder) addNameToIndexDirect(country, name string, cityPtr *city.City) {
	nf.mutex.Lock()
	nf.addOverflowLocked(country, []string{name}, *cityPtr)
	nf.mutex.Unlock()
}

// addNameToMap appends a row id to a name's staging list.
func addNameToMap(countryMap map[string][]int32, name string, id int32) {
	ids, exists := countryMap[name]
	if !exists {
		// Most names are unique within a country: start small.
		ids = make([]int32, 0, 2)
	}
	countryMap[name] = append(ids, id)
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
	return names
}

// totalIndexKeys returns the total number of (country, name) keys across the
// sorted tables and the overflow — the gate metric for FuzzyMaxNames.
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

// ensureFuzzyBuilt lazily brings the fuzzy index toward a terminal state:
// built, or disabled when the index exceeds FuzzyMaxNames. It is safe to
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
// built, 1 = building, 2 = built, 3 = disabled (corpus over FuzzyMaxNames,
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

// fuzzyCandidates returns the names within maxDistance of query, from the
// immutable n-gram index plus the post-build overflow list, and whether the
// result is partial (the n-gram walk hit the FuzzyMaxCandidates cap). The
// caller must handle caching; this is the uncached core of
// getCachedFuzzySearch.
//
// The budget scopes to the n-gram walk only: the overflow list is a small
// linear scan bounded by the number of post-build AddCity names (no
// production callers today), so it runs in full even when the walk truncates
// — a truncated query still rescues typos of recently added names.
//
// The read lock is scoped around the candidate generation: AddCity mutates
// the overflow list and can swap nf.ngrams under the write lock, so an
// unlocked read would race. Concurrent readers — the common case at query
// time — do not block each other.
func (nf *Finder) fuzzyCandidates(query string, maxDistance int) ([]string, bool) {
	nf.mutex.RLock()
	candidates, truncated := nf.ngrams.search(query, maxDistance)
	overflow := nf.fuzzyOverflow
	nf.mutex.RUnlock()

	if len(overflow) == 0 {
		return candidates, truncated
	}
	// The overflow scan reuses the same banded, allocation-free checker the
	// n-gram verify step uses (volumes here are tiny, but one distance
	// implementation across the package keeps behavior uniform).
	var check levenshteinChecker
	check.prepare(query)
	for _, name := range overflow {
		if check.atMost(name, maxDistance) {
			candidates = append(candidates, name)
		}
	}
	return candidates, truncated
}

// getCachedFuzzySearch performs fuzzy search with caching. Only complete,
// non-empty results enter the cache: truncated results (FuzzyMaxCandidates
// tripped) and empty results (a later AddCity must become visible to the
// same query) are computed fresh on every call.
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
	candidates, truncated := nf.fuzzyCandidates(query, maxDistance)

	// Cache honesty: a budget-truncated result is partial for this query.
	// Never write it to the cache — the 1h TTL would pin it and serve it to
	// later identical queries as if complete. Exclusion (rather than a
	// truncated-tag on entries) is the simpler correct option: the only cost
	// is that a repeated degenerate query re-pays the (now budget-bounded)
	// search, and raising FuzzyMaxCandidates immediately takes effect for
	// fresh searches instead of waiting out stale tagged entries.
	if truncated {
		return candidates
	}

	// An EMPTY complete result is just as unsafe to pin. AddCity makes new
	// names fuzzy-visible on the very next search (post-build additions are
	// scanned from the overflow list every time), so a cached empty miss
	// would keep serving stale nils for the full TTL after the city
	// appeared — contradicting the "overflow is always scanned" guarantee.
	// Skipping the cache only costs one re-search per repeated no-hit query,
	// bounded by FuzzyMaxCandidates like every other walk.
	if len(candidates) == 0 {
		return candidates
	}

	// Cache the result
	nf.cacheMutex.Lock()
	nf.storeFuzzyCacheLocked(cacheKey, candidates, time.Now())
	nf.cacheMutex.Unlock()

	return candidates
}

// storeFuzzyCacheLocked inserts (or replaces) one cache entry and keeps the
// cache bounded at maxFuzzyCacheEntries. The caller must hold cacheMutex for
// writing.
//
// Entries are never refreshed in place — a key is only (re)written after a
// miss — so insertion order is age order, and fuzzyCacheAge's front is always
// the oldest live entry. That makes both eviction rules O(1) per removal:
// expired entries are dropped from the front, then the oldest entry if the
// cache is still at its cap. The previous implementation scanned the whole
// map (twice) on every insert at capacity, which at the 10k cap cost more
// than the fuzzy search it was caching under a churning typo workload (see
// BenchmarkCityByNameFuzzy/cache-churn).
func (nf *Finder) storeFuzzyCacheLocked(key string, candidates []string, now time.Time) {
	if prev, exists := nf.fuzzyCache[key]; exists {
		// An expired entry being recomputed: drop its stale age slot.
		nf.fuzzyCacheAge.Remove(prev.age)
		delete(nf.fuzzyCache, key)
	}
	for front := nf.fuzzyCacheAge.Front(); front != nil; front = nf.fuzzyCacheAge.Front() {
		oldestKey := front.Value.(string)
		expired := now.Sub(nf.fuzzyCache[oldestKey].timestamp) >= fuzzyCacheTTL
		if !expired && len(nf.fuzzyCache) < maxFuzzyCacheEntries {
			break
		}
		nf.fuzzyCacheAge.Remove(front)
		delete(nf.fuzzyCache, oldestKey)
	}
	nf.fuzzyCache[key] = &fuzzySearchResult{
		candidates: candidates,
		timestamp:  now,
		age:        nf.fuzzyCacheAge.PushBack(key),
	}
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
	// FuzzyMaxNames threshold matching is disabled (exact-only) for this
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

// PrefixMatch pairs an indexed name with the first city that name resolves
// to — the same first-referenced winner CityByName's exact phase returns for
// a homonym.
type PrefixMatch struct {
	Name string
	City *city.City
}

const (
	// defaultPrefixMatches is the limit PrefixNames applies when the caller
	// passes maxNames <= 0.
	defaultPrefixMatches = 10
	// maxPrefixMatches caps any caller-supplied limit so a stray large value
	// cannot turn an autocomplete box into a table walk.
	maxPrefixMatches = 50
)

// PrefixNames returns up to maxNames indexed names under countryCode that
// start with prefix, each paired with its first-referenced city — the feed
// for the upcoming autocomplete endpoint. Sorted-table matches come first
// (in sorted order), then matching overflow names (sorted), with the total
// capped at maxNames; maxNames <= 0 selects a default of 10 and any input
// is capped at 50. A country with no indexed names at all returns nil;
// names indexed with zero references carry no city to pair and are skipped.
//
// The sorted-table walk binary-searches to the first name >= prefix and
// stops at the first non-match: the prefixed names are contiguous in a
// sorted table, so no match can hide behind a miss.
func (nf *Finder) PrefixNames(countryCode, prefix string, maxNames int) []PrefixMatch {
	if maxNames <= 0 {
		maxNames = defaultPrefixMatches
	}
	if maxNames > maxPrefixMatches {
		maxNames = maxPrefixMatches
	}

	nf.mutex.RLock()
	defer nf.mutex.RUnlock()

	t := nf.countries[countryCode]
	countryOverflow := nf.overflow[countryCode]
	if t == nil && len(countryOverflow) == 0 {
		return nil
	}

	matches := make([]PrefixMatch, 0, maxNames)
	if t != nil {
		for i := sort.SearchStrings(t.names, prefix); i < len(t.names) && len(matches) < maxNames; i++ {
			if !strings.HasPrefix(t.names[i], prefix) {
				break
			}
			ids := t.ids[t.starts[i]:t.starts[i+1]]
			if len(ids) == 0 {
				continue // zero-ref keys carry no city to pair
			}
			matches = append(matches, PrefixMatch{Name: t.names[i], City: nf.cities.at(ids[0])})
		}
	}

	if remaining := maxNames - len(matches); remaining > 0 && len(countryOverflow) > 0 {
		var overflowNames []string
		for name := range countryOverflow {
			if strings.HasPrefix(name, prefix) {
				overflowNames = append(overflowNames, name)
			}
		}
		sort.Strings(overflowNames)
		for _, name := range overflowNames[:min(len(overflowNames), remaining)] {
			ids := countryOverflow[name]
			if len(ids) == 0 {
				continue
			}
			matches = append(matches, PrefixMatch{Name: name, City: nf.cities.at(ids[0])})
		}
	}
	return matches
}
