package name

import (
	"fmt"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
)

// benchFuzzyFinder returns a finder whose index holds count unique names
// (through the post-construction overflow) but whose fuzzy n-gram index has
// not been built yet.
func benchFuzzyFinder(b *testing.B, count int) *Finder {
	b.Helper()
	finder := NewNameFinder()
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("BenchCity%06d", i)
		finder.addNameToIndexDirect("BC", name, &city.City{Name: name, Country: "BC"})
	}
	return finder
}

// BenchmarkEnsureFuzzyBuilt measures the one-time lazy n-gram build over
// 100k unique names. The build runs in a background goroutine, so each
// iteration triggers it and then waits it out with the timer running — the
// wait IS the build. Without the wait the loop would spawn unbounded
// concurrent builds; without the reset every iteration after the first
// would fast-path out of ensureFuzzyBuilt (state already fuzzyBuilt) and
// measure nothing.
func BenchmarkEnsureFuzzyBuilt(b *testing.B) {
	finder := benchFuzzyFinder(b, 100000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		finder.mutex.Lock()
		finder.ngrams = nil
		finder.mutex.Unlock()
		finder.fuzzyState.Store(fuzzyNotBuilt)
		finder.ensureFuzzyBuilt()
		waitFuzzyBuilt(b, finder)
	}
}

// BenchmarkNGramSearch measures steady-state distance-2 typo queries against
// the built index (cache-bypassed, straight into the structure).
func BenchmarkNGramSearch(b *testing.B) {
	finder := benchFuzzyFinder(b, 100000)
	finder.ensureFuzzyBuilt()
	waitFuzzyBuilt(b, finder)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if got, _ := finder.ngrams.search(fmt.Sprintf("BenchCitt%06d", i%100000), 2); len(got) == 0 {
			b.Fatal("typo query must find its base name")
		}
	}
}
