package name

import (
	"runtime"
	"sort"
	"sync"
)

// maxTableWorkers caps the per-country table build and the loader fan-out:
// past eight workers the maps being read and the allocator, not the CPU,
// are the contention.
const maxTableWorkers = 8

// tableWorkers is the worker count both the build path and DeserializeIndex
// use: GOMAXPROCS, capped at maxTableWorkers.
func tableWorkers() int {
	return max(1, min(runtime.GOMAXPROCS(0), maxTableWorkers))
}

// buildTable flattens one country's name -> ids map into a nameTable: names
// sorted once, ids copied CSR-style in their stored order. check, when
// non-nil, is run on the country's map first and its error aborts the build.
func buildTable(refs map[string][]int32, check func(map[string][]int32) error) (*nameTable, error) {
	if check != nil {
		if err := check(refs); err != nil {
			return nil, err
		}
	}
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
	return t, nil
}

// buildTables builds every country's nameTable from the staging maps, the
// one per-country flatten both BuildIndex and DeserializeIndex run. Each
// country is independent (its own sort and arrays), so up to workers
// goroutines take countries off a queue, biggest first so the one huge
// country (the US holds a fifth of prod's names) starts immediately instead
// of landing last. Results land in a slice by job index and the map is
// assembled after the workers finish, so the output is identical for any
// worker count.
//
// The first error wins: it stops the queue (workers finish the country they
// are on) and is returned with no tables. check is passed to every
// buildTable call and must be safe for concurrent use.
func buildTables(refs map[string]map[string][]int32, workers int, check func(map[string][]int32) error) (map[string]*nameTable, error) {
	countries := make([]string, 0, len(refs))
	for country := range refs {
		countries = append(countries, country)
	}
	sort.Slice(countries, func(i, j int) bool {
		li, lj := len(refs[countries[i]]), len(refs[countries[j]])
		if li != lj {
			return li > lj
		}
		return countries[i] < countries[j]
	})

	tables := make([]*nameTable, len(countries))
	workers = max(1, min(workers, len(countries)))

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		next     int
		firstErr error
	)
	// claim hands out the next job index, or false once the queue is empty
	// or a worker has failed.
	claim := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr != nil || next >= len(countries) {
			return 0, false
		}
		next++
		return next - 1, true
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i, ok := claim()
				if !ok {
					return
				}
				t, err := buildTable(refs[countries[i]], check)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				tables[i] = t // distinct index per job: no lock needed
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	out := make(map[string]*nameTable, len(countries))
	for i, country := range countries {
		out[country] = tables[i]
	}
	return out, nil
}
