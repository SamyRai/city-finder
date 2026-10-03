package coordinates

import (
	"slices"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/golang/geo/s2"
)

// topPopulationK is the size of the top-K population table backing the anytime
// bound of population-ranked queries (see populationOutsideBound). 4096 keeps
// the table ~128 KB at prod (32 B per entry) while dropping the worst-case
// outside-radius score ceiling from maxPopulation (the single largest city,
// ~37M) to the 4096th-largest population — plus exact scores for those 4096
// cities — which is what lets mid-ocean queries certify a winner without the
// terminal full-sphere scan.
const topPopulationK = 4096

// maxPopulationOf returns the largest Population in cities (0 when empty or
// all population-less).
func maxPopulationOf(cities []city.City) int32 {
	var max int32
	for i := range cities {
		if cities[i].Population > max {
			max = cities[i].Population
		}
	}
	return max
}

// topPopulationEntry is one row of the finder's top-K population table: a
// city's population paired with its indexed s2 location, so the anytime bound
// can score it against a query point without touching the shape index.
type topPopulationEntry struct {
	population int32
	point      s2.Point
}

// topPopulationsOf builds the top-K population table from cities: the
// topPopulationK largest populations, population > 0 only (zero/negative
// populations score nothing under the gravity model and are excluded), sorted
// by population descending with ties broken by city index ascending for
// determinism. With fewer than topPopulationK positive populations the table
// simply holds them all, which leaves the K-th-largest population at 0 —
// populationOutsideBound then relies on exact scores alone.
func topPopulationsOf(cities []city.City) []topPopulationEntry {
	top := topPopulationRefs(cities, topPopulationK)
	entries := make([]topPopulationEntry, len(top))
	for i, r := range top {
		entries[i] = topPopulationEntry{
			population: r.population,
			point:      s2.PointFromLatLng(s2.LatLngFromDegrees(cities[r.index].Latitude, cities[r.index].Longitude)),
		}
	}
	return entries
}

// populationRef is a candidate row of the top-K table.
type populationRef struct {
	population int32
	index      int
}

// outranks is the table order: larger population first, then lower index.
func (r populationRef) outranks(o populationRef) bool {
	if r.population != o.population {
		return r.population > o.population
	}
	return r.index < o.index
}

// topPopulationRefs selects the k best positive-population rows in table
// order with a size-k heap whose root is the weakest kept row: O(n log k)
// time and O(k) memory, where sorting every populated row was O(n log n)
// time and O(n) memory on each boot.
func topPopulationRefs(cities []city.City, k int) []populationRef {
	heap := make([]populationRef, 0, min(k, len(cities)))
	// siftDown restores the heap below i; a parent never outranks a child.
	siftDown := func(i int) {
		for {
			weakest, l, r := i, 2*i+1, 2*i+2
			if l < len(heap) && heap[weakest].outranks(heap[l]) {
				weakest = l
			}
			if r < len(heap) && heap[weakest].outranks(heap[r]) {
				weakest = r
			}
			if weakest == i {
				return
			}
			heap[i], heap[weakest] = heap[weakest], heap[i]
			i = weakest
		}
	}
	for i := range cities {
		if cities[i].Population <= 0 || k == 0 {
			continue
		}
		ref := populationRef{population: cities[i].Population, index: i}
		if len(heap) < k {
			heap = append(heap, ref)
			for c := len(heap) - 1; c > 0; {
				p := (c - 1) / 2
				if !heap[p].outranks(heap[c]) {
					break
				}
				heap[p], heap[c] = heap[c], heap[p]
				c = p
			}
			continue
		}
		if ref.outranks(heap[0]) {
			heap[0] = ref
			siftDown(0)
		}
	}
	slices.SortFunc(heap, func(a, b populationRef) int {
		if a.outranks(b) {
			return -1
		}
		return 1 // distinct indexes: never equal
	})
	return heap
}
