package name

import (
	"sort"
	"strings"

	"github.com/SamyRai/cityFinder/lib/city"
)

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
// (in sorted order), then matching overflow names (sorted) that are not
// already in the table, with the total capped at maxNames; maxNames <= 0 selects a default of 10 and any input
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
			if c := nf.cities.at(ids[0]); c != nil { // nil only while detached
				matches = append(matches, PrefixMatch{Name: t.names[i], City: c})
			}
		}
	}

	if remaining := maxNames - len(matches); remaining > 0 && len(countryOverflow) > 0 {
		var overflowNames []string
		for name := range countryOverflow {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			// A name also in the sorted table was listed (or cut by the
			// limit) above, paired with the table's city, which is also the
			// city CityByName resolves it to; listing it again would
			// duplicate it.
			if t != nil {
				if _, inTable := t.lookup(name); inTable {
					continue
				}
			}
			overflowNames = append(overflowNames, name)
		}
		sort.Strings(overflowNames)
		for _, name := range overflowNames[:min(len(overflowNames), remaining)] {
			ids := countryOverflow[name]
			if len(ids) == 0 {
				continue
			}
			if c := nf.cities.at(ids[0]); c != nil {
				matches = append(matches, PrefixMatch{Name: name, City: c})
			}
		}
	}
	return matches
}
