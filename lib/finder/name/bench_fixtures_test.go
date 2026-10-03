package name

import (
	"fmt"
	"io"
	"log"
	"testing"

	"github.com/SamyRai/cityFinder/internal/testfixture"
	"github.com/SamyRai/cityFinder/lib/city"
)

// benchSyllables feeds benchDiverseName: 50 two-letter syllables combined
// three deep give broadly varying 3-gram content (unlike a single repeated
// name, which collapses the n-gram index to a handful of keys and makes
// fuzzy/prefix benchmarks measure a cache-hot special case).
var benchSyllables = []string{
	"ba", "be", "bi", "bo", "bu", "da", "de", "di", "do", "du",
	"ka", "ke", "ki", "ko", "ku", "ma", "me", "mi", "mo", "mu",
	"na", "ne", "ni", "no", "nu", "ra", "re", "ri", "ro", "ru",
	"sa", "se", "si", "so", "su", "ta", "te", "ti", "to", "tu",
	"va", "ve", "vi", "vo", "vu", "za", "ze", "zi", "zo", "zu",
}

// benchDiverseName returns a distinct 12-char name for every i < 1e6: three
// syllables (coarse position encoding) plus the index itself, so the trailing
// digits alone already guarantee uniqueness below one million.
func benchDiverseName(i int) string {
	return benchSyllables[i%50] +
		benchSyllables[(i/50)%50] +
		benchSyllables[(i/2500)%50] +
		fmt.Sprintf("%06d", i%1000000)
}

// benchDiverseCities builds count distinct-name cities spread round-robin
// over exactTailCountries (same countries as the exact-tail fixture), with
// no alternate names — the canonical-name tables only.
func benchDiverseCities(count int) []city.SpatialCity {
	return testfixture.Cities(count, testfixture.Spec{
		Name:    benchDiverseName,
		Country: testfixture.Cycle(exactTailCountries),
		Lat:     func(i int) float64 { return float64(i%18000) / 100.0 },
		Lon:     func(i int) float64 { return float64(i%36000)/100.0 - 180.0 },
	})
}

// benchFuzzyQueries returns count typo queries against the benchDiverseName
// fixture: each query is its base name with the middle syllable's second
// letter replaced by 'x' — a distance-1 edit the fuzzy index must resolve —
// paired with the base name's country, since fuzzy candidates resolve
// per-country.
func benchFuzzyQueries(count int) []struct{ name, country string } {
	queries := make([]struct{ name, country string }, count)
	for i := range queries {
		key := (i * 97) % count
		base := benchDiverseName(key)
		queries[i].name = base[:3] + "x" + base[4:]
		queries[i].country = exactTailCountries[key%len(exactTailCountries)]
	}
	return queries
}

// buildDiverseIndex constructs a BuildIndex over benchDiverseCities, failing
// the benchmark if the fixture ever breaks.
func buildDiverseIndex(b *testing.B, count int) *Finder {
	b.Helper()
	silenceBuildLogs(b)
	finder := BuildIndex(benchDiverseCities(count))
	if finder == nil {
		b.Fatal("BuildIndex returned nil")
	}
	return finder
}

// silenceBuildLogs discards the package's log output (BuildIndex logs every
// call) for the rest of the benchmark and restores it afterwards. The log
// calls still run and format their messages — only the terminal write is
// dropped — so the measured op keeps its logging CPU cost while benchmark
// output stays parseable by benchstat.
func silenceBuildLogs(b *testing.B) {
	b.Helper()
	old := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(old) })
}
