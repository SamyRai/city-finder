package name

import (
	"fmt"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/util"
)

// benchFuzzyFinder returns a finder whose InvertedIndex and allNames hold
// count unique names but whose BK-tree has not been built yet.
func benchFuzzyFinder(b *testing.B, count int) *Finder {
	b.Helper()
	finder := NewNameFinder()
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("BenchCity%06d", i)
	}
	for _, name := range names {
		finder.addNameToIndexDirect("BC", name, &city.City{Name: name, Country: "BC"})
	}
	finder.allNames = names
	return finder
}

// BenchmarkEnsureBKTreeBuilt measures the one-time lazy BK-tree build over
// 100k unique names. It resets the tree between iterations so every iteration
// pays a full build; this is the benchmark behind the sequential-vs-parallel
// build-path decision.
func BenchmarkEnsureBKTreeBuilt(b *testing.B) {
	finder := benchFuzzyFinder(b, 100000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		finder.mutex.Lock()
		finder.BKTree = util.NewBKTree()
		finder.isBKTreeBuilt = false
		finder.mutex.Unlock()
		finder.ensureBKTreeBuilt()
	}
}
