package name

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/SamyRai/cityFinder/lib/city"
)

// ErrCityTableMismatch reports a ShareCities table that is not the table the
// index's ids were built against. Sharing is refused and the index keeps
// whatever it had, so a mismatch can never change an answer.
var ErrCityTableMismatch = errors.New("city table does not match the name index")

// cityTable owns the cities name ids resolve to. Ids are ROW numbers: id i
// < baseCount is base[i], the i-th city of the build input (the loader's row
// order, which is also the S2 index's row order); ids from baseCount up are
// cities added after the build (AddCity), held individually in extra.
//
// The base table is either owned (built from the input, or decoded from an
// index file that embeds it) or shared — the very []city.City slice the S2
// index serves from, attached with ShareCities after it was proven identical.
// Sharing is what removes the second copy of every city from the process. An
// index decoded from a file that references an external table is detached
// (base nil) until ShareCities attaches the matching table; a detached index
// resolves every base id to nil instead of to a wrong city.
type cityTable struct {
	base        []city.City
	baseCount   int    // len(base) when attached; the expected length while detached
	fingerprint uint64 // expected city.Fingerprint of base while detached
	shared      bool   // base is borrowed (never embedded in a serialized index)
	extra       []*city.City
}

// ownedCityTable returns a table owning a copy of the build input's City
// values in row order. Contiguous values: one allocation for the whole table
// instead of one heap object (plus a pointer) per city.
func ownedCityTable(cities []city.SpatialCity) cityTable {
	base := make([]city.City, len(cities))
	for i := range cities {
		base[i] = cities[i].City
	}
	return cityTable{base: base, baseCount: len(base)}
}

// at resolves an id. It returns nil for an id the table cannot resolve (a
// detached base id, or an out-of-range id), never a wrong city.
func (t *cityTable) at(id int32) *city.City {
	switch {
	case id < 0:
		return nil
	case int(id) < t.baseCount:
		if t.base == nil {
			return nil
		}
		return &t.base[id]
	case int(id)-t.baseCount < len(t.extra):
		return t.extra[int(id)-t.baseCount]
	default:
		return nil
	}
}

// add stores a post-build city and returns its id.
func (t *cityTable) add(c city.City) int32 {
	cityCopy := c
	t.extra = append(t.extra, &cityCopy)
	return int32(t.baseCount + len(t.extra) - 1)
}

// size is the number of resolvable ids (base + extra), the bound every stored
// id must respect.
func (t *cityTable) size() int { return t.baseCount + len(t.extra) }

// share attaches table as the base after proving it holds exactly the cities
// the ids were built against, and returns the id remapping to apply (nil when
// ids are unchanged). Three cases:
//
//   - detached (loaded from a file referencing an external table): the table
//     must match the stored fingerprint; ids are already row numbers.
//   - owned and element-wise equal (BuildIndex over the same rows): ids are
//     already row numbers.
//   - owned and equal only as a MULTISET (a legacy v2 file numbers cities by
//     first encounter, not by row): ids are remapped value-for-value onto the
//     table's rows. Every remapped id resolves to a City equal to the one it
//     resolved to before, so no answer changes.
//
// On any mismatch nothing is changed.
func (t *cityTable) share(table []city.City) ([]int32, error) {
	if len(table) != t.baseCount {
		return nil, fmt.Errorf("%w: table has %d cities, index expects %d", ErrCityTableMismatch, len(table), t.baseCount)
	}
	var remap []int32
	switch {
	case t.base == nil:
		if fp := city.Fingerprint(table); fp != t.fingerprint {
			return nil, fmt.Errorf("%w: fingerprint %016x, index expects %016x", ErrCityTableMismatch, fp, t.fingerprint)
		}
	case !slices.Equal(t.base, table):
		var err error
		if remap, err = remapByValue(t.base, table); err != nil {
			return nil, err
		}
	}
	t.base, t.shared = table, true
	return remap, nil
}

// remapByValue pairs every row of from with an equal row of to and returns
// remap[i] = the row of to holding a City equal to from[i]. It fails unless
// the two tables are the same multiset of City values. Equal values are
// paired in row order, which is arbitrary but harmless: they are equal.
// Cost: two index sorts by City value (O(n log n)); used once, to migrate a
// legacy index file.
func remapByValue(from, to []city.City) ([]int32, error) {
	order := func(cities []city.City) []int32 {
		idx := make([]int32, len(cities))
		for i := range idx {
			idx[i] = int32(i)
		}
		slices.SortStableFunc(idx, func(a, b int32) int { return compareCity(&cities[a], &cities[b]) })
		return idx
	}
	fromOrder, toOrder := order(from), order(to)
	remap := make([]int32, len(from))
	for k := range fromOrder {
		if from[fromOrder[k]] != to[toOrder[k]] {
			return nil, fmt.Errorf("%w: tables hold different cities (e.g. %+v)", ErrCityTableMismatch, from[fromOrder[k]])
		}
		remap[fromOrder[k]] = toOrder[k]
	}
	return remap, nil
}

// compareCity is a total order over every City field.
func compareCity(a, b *city.City) int {
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	if c := strings.Compare(a.Country, b.Country); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Latitude, b.Latitude); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Longitude, b.Longitude); c != 0 {
		return c
	}
	return cmp.Compare(a.Population, b.Population)
}

// ShareCities makes the index resolve its ids through table — in production
// the S2 index's Cities slice, so every city is held once per process instead
// of once per index. It succeeds only when table holds exactly the cities the
// index was built against (see cityTable.share; a legacy index numbered in a
// different order is remapped onto the table's rows), so sharing never
// changes a lookup result; otherwise it returns an error wrapping
// ErrCityTableMismatch and changes nothing.
//
// Call it during initialization, before the finder serves lookups. After a
// successful call, SerializeIndex writes the compact format that references
// the table instead of embedding a copy.
func (nf *Finder) ShareCities(table []city.City) error {
	nf.mutex.Lock()
	defer nf.mutex.Unlock()
	remap, err := nf.cities.share(table)
	if err != nil || remap == nil {
		return err
	}
	apply := func(ids []int32) {
		for i, id := range ids {
			if int(id) < len(remap) { // extras (post-build cities) keep their ids
				ids[i] = remap[id]
			}
		}
	}
	for _, t := range nf.countries {
		apply(t.ids)
	}
	for _, countryOverflow := range nf.overflow {
		for _, ids := range countryOverflow {
			apply(ids)
		}
	}
	return nil
}

// OwnsCityTable reports whether the index holds its own copy of the city
// table: true after BuildIndex and after loading a file that embeds the
// table, false once ShareCities succeeded or while detached. The initializer
// uses it to migrate an embedding file to the compact referencing format.
func (nf *Finder) OwnsCityTable() bool {
	nf.mutex.RLock()
	defer nf.mutex.RUnlock()
	return nf.cities.base != nil && !nf.cities.shared
}

// CitiesShared reports whether the index resolves through a shared table
// (ShareCities succeeded).
func (nf *Finder) CitiesShared() bool {
	nf.mutex.RLock()
	defer nf.mutex.RUnlock()
	return nf.cities.shared
}
