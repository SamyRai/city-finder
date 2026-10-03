package name

import "github.com/SamyRai/cityFinder/lib/city"

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
	nf.hasKeys.Store(true) // names holds at least the primary name
	nf.fuzzyGen.Add(1)     // cached fuzzy results predate these names
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
