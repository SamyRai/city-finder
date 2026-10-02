package postalCode

import (
	"slices"
	"strings"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// entry holds exactly the fields a postal lookup serves (CityByPostalCode
// returns the place name and coordinates; the country comes from the query).
// The loader's PostalCodeEntry also carries admin names/codes, accuracy and
// redundant copies of the country and postal code — ~136 B of struct plus
// seven strings per row, none of which any lookup returns — so the index
// keeps 32 B per row instead.
type entry struct {
	Latitude  float64
	Longitude float64
	PlaceName string
}

// countryTable is one country's postal codes: codes sorted ascending, with
// entries[i] belonging to codes[i]. Two parallel slices instead of a map:
// no per-entry hash-bucket overhead, and lookups are a binary search.
type countryTable struct {
	codes   []string
	entries []entry
}

func entryOf(e dataLoader.PostalCodeEntry) entry {
	return entry{Latitude: e.Latitude, Longitude: e.Longitude, PlaceName: e.PlaceName}
}

// buildCountryTable builds a sorted table from one country's loader map.
func buildCountryTable(byCode map[string]dataLoader.PostalCodeEntry) *countryTable {
	t := &countryTable{codes: make([]string, 0, len(byCode)), entries: make([]entry, len(byCode))}
	for code := range byCode {
		t.codes = append(t.codes, code)
	}
	slices.Sort(t.codes)
	for i, code := range t.codes {
		t.entries[i] = entryOf(byCode[code])
	}
	return t
}

// find returns the position of code, and whether it is present.
func (t *countryTable) find(code string) (int, bool) {
	return slices.BinarySearchFunc(t.codes, code, strings.Compare)
}

// put inserts or replaces one code (O(n) insert; the bulk path is
// buildCountryTable, put serves incremental AddPostalCode calls).
func (t *countryTable) put(code string, e entry) {
	i, found := t.find(code)
	if found {
		t.entries[i] = e
		return
	}
	t.codes = slices.Insert(t.codes, i, code)
	t.entries = slices.Insert(t.entries, i, e)
}
