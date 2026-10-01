package name

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refLevenshtein is the test-only reference distance: a textbook full-matrix
// Levenshtein DP over runes, deliberately independent of the banded,
// allocation-free checker in ngram.go (no band, no early exit, no shared
// code paths). Cross-validating the production checker against it is what
// proves the banding and early exits correct. Small strings only — the test
// grids are tens of runes, so the O(len(a)·len(b)) matrix is irrelevant.
func refLevenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	dp := make([][]int, len(ra)+1)
	for i := range dp {
		dp[i] = make([]int, len(rb)+1)
		dp[i][0] = i
	}
	for j := range dp[0] {
		dp[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			dp[i][j] = min(dp[i-1][j-1]+cost, dp[i-1][j]+1, dp[i][j-1]+1)
		}
	}
	return dp[len(ra)][len(rb)]
}

// ngramCorpus is a small but shape-diverse corpus: ASCII words of many
// lengths, repeated-gram names (which exercise duplicate postings), Unicode
// names (rune-level grams), and names that are prefixes/suffixes of each
// other.
func ngramCorpus() []string {
	return []string{
		"Paris", "Parma", "Palma", "Panama", "Paramaribo",
		"London", "Londonderry", "Londrina", "Lyon",
		"aaaaa", "ababab", "abcabcabc",
		"São Paulo", "São Pedro", "São Roque",
		"Zürich", "Zürichsee",
		"北京市", "北京", "上海市",
		"Kyyiv", "Kyiv", "Kiev",
		"W", "Wo", "Wol", "Wolfsburg", "Wolfenbüttel",
		"Saint-Étienne", "Saint-Denis", "Sainte-Geneviève",
		"Xcaret", "Xcalacoop", "Xochimilco",
		"York", "New York", "New York City",
	}
}

// TestNGramSearchMatchesBruteForce is the core correctness gate: for every
// query and distance, the n-gram search must return EXACTLY the brute-force
// Levenshtein matches over the covered name lengths. The q-gram lemma's
// completeness boundary (see ngram.go) excludes very short names — names of
// ≤ 1 rune may be missed at d=1, ≤ 4 runes at d=2 — so the comparison
// restricts both sides to names safely above the boundary. Every result the
// search does return must still be a true match (no false positives), which
// holds for ALL lengths.
func TestNGramSearchMatchesBruteForce(t *testing.T) {
	corpus := ngramCorpus()
	index, err := buildNGramIndex(corpus)
	require.NoError(t, err)

	bruteForceMatches := func(query string, maxDistance, minRunes int) []string {
		var matches []string
		for _, name := range corpus {
			if utf8.RuneCountInString(name) >= minRunes && refLevenshtein(query, name) <= maxDistance {
				matches = append(matches, name)
			}
		}
		return matches
	}

	queries := append([]string{}, corpus...) // exact queries
	for _, name := range corpus {
		if r := utf8.RuneCountInString(name); r >= 5 {
			queries = append(queries, mangle(name, 1), mangle(name, 2))
		}
	}
	queries = append(queries, "zzz", "Pari", "Londinium", "Saint", "北京时代", "Wolf")

	for _, d := range []int{0, 1, 2} {
		// Safety threshold from the boundary analysis: runeLen > q(d-1)+1.
		safeLen := ngramQ*(d-1) + 2
		for _, q := range queries {
			got := index.search(q, d)
			for _, m := range got {
				dist := refLevenshtein(q, m)
				assert.LessOrEqualf(t, dist, d,
					"false positive: search(%q, %d) returned %q at distance %d", q, d, m, dist)
			}
			want := bruteForceMatches(q, d, safeLen)
			gotSafe := make([]string, 0, len(got))
			for _, m := range got {
				if utf8.RuneCountInString(m) >= safeLen {
					gotSafe = append(gotSafe, m)
				}
			}
			assert.ElementsMatchf(t, want, gotSafe, "search(%q, %d) over safe-length names must equal brute force", q, d)
		}
	}
}

// mangle applies deterministic typos to s: substitutions, insertions, and
// deletions chosen by a masked-positive seed (negative shifts panic in Go).
func mangle(s string, edits int) string {
	runes := []rune(s)
	seed := 1
	for _, r := range s {
		seed = (seed*31 + int(r)) & 0x7fffffff
		if seed == 0 {
			seed = 1
		}
	}
	posOf := func(e, n int) int { return int(uint(seed>>uint(e)) % uint(n)) }
	for e := 0; e < edits; e++ {
		switch (seed >> uint(e)) % 3 {
		case 0: // substitute
			if len(runes) > 0 {
				pos := posOf(e, len(runes))
				runes[pos] = runes[pos] + 1
			}
		case 1: // insert
			pos := posOf(e, len(runes)+1)
			runes = append(runes, 0)
			copy(runes[pos+1:], runes[pos:])
			runes[pos] = 'x'
		default: // delete
			if len(runes) > 1 {
				pos := posOf(e, len(runes))
				runes = append(runes[:pos], runes[pos+1:]...)
			}
		}
	}
	return string(runes)
}

// TestLevenshteinCheckerMatchesReference cross-validates the banded,
// allocation-free checker against the independent full-matrix DP reference
// (refLevenshtein above) over a grid of string pairs and distance bounds,
// including Unicode, repeated runes, and empty strings.
func TestLevenshteinCheckerMatchesReference(t *testing.T) {
	pairs := [][2]string{
		{"", ""}, {"", "abc"}, {"abc", ""}, {"abc", "abc"},
		{"abc", "abd"}, {"abc", "ab"}, {"abc", "abcd"}, {"kitten", "sitting"},
		{"flaw", "lawn"}, {"gumbo", "gambol"}, {"aaaaa", "aaa"},
		{"São Paulo", "São Paulu"}, {"北京市", "北京 市"}, {"北京", "东京"},
		{"Saint-Étienne", "Saint-Etienne"}, {"x", "y"}, {"xy", "yx"},
		{"Wolfenbüttel", "Wolfenbuttel"}, {"a", "aaaaaaaaaa"},
	}
	// Deterministic pseudo-random pairs on top of the fixed grid.
	seed := 7
	for i := 0; i < 200; i++ {
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		a := mangle(ngramCorpus()[seed%len(ngramCorpus())], seed%3)
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		b := mangle(ngramCorpus()[seed%len(ngramCorpus())], seed%3)
		pairs = append(pairs, [2]string{a, b})
	}

	var c levenshteinChecker
	for _, p := range pairs {
		c.prepare(p[0])
		want := refLevenshtein(p[0], p[1])
		for d := 0; d <= 5; d++ {
			got := c.atMost(p[1], d)
			assert.Equalf(t, want <= d, got, "atMost(%q, %q, %d): reference distance is %d", p[0], p[1], d, want)
		}
	}
}

// TestNGramSearchEmptyAndShort pins degenerate inputs: an empty query returns
// nothing without panicking, and queries longer than every name return
// nothing (length filter).
func TestNGramSearchEmptyAndShort(t *testing.T) {
	index, err := buildNGramIndex(ngramCorpus())
	require.NoError(t, err)
	assert.Empty(t, index.search("", 2))
	assert.Empty(t, index.search("AVeryLongQueryNameThatMatchesNothingAtAll", 2))
}

// TestNGramBuildCapsOverlongNameLengths pins the uint16 length-table guard:
// a name of ≥ 65,536 runes is recorded as the MaxUint16 cap rather than the
// silently wrapped value, the build still succeeds, and normal fuzzy queries
// are unaffected (the overlong name simply cannot pass the length filter for
// any realistic query). Exact phase-1 lookups never consult nameLens, so
// they are unaffected by construction.
func TestNGramBuildCapsOverlongNameLengths(t *testing.T) {
	overlong := strings.Repeat("a", math.MaxUint16+10) // 65,545 runes: wraps to 9 today
	index, err := buildNGramIndex([]string{"Paris", overlong})
	require.NoError(t, err)
	require.Len(t, index.nameLens, 2)
	assert.Equal(t, uint16(5), index.nameLens[0])
	assert.Equal(t, uint16(math.MaxUint16), index.nameLens[1],
		"overlong name length must be capped, not wrapped")

	matches := index.search("Parls", 1) // distance-1 typo of Paris
	assert.Contains(t, matches, "Paris", "normal names must stay fuzzy-findable")
	assert.NotContains(t, matches, overlong, "the overlong name must never surface as a match")
}

// TestFuzzyTypoRescueEndToEnd drives CityByName through the new structure at
// small scale: distance-1 and distance-2 typos of an indexed name resolve,
// the country filter still rejects cross-country candidates, and repeated
// lookups are served from the fuzzy cache.
func TestFuzzyTypoRescueEndToEnd(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())

	if got := finder.CityByName("Pars", "FR"); got == nil || got.Name != "Paris" {
		t.Fatalf("distance-1 typo: got %+v, want Paris", got)
	}
	if got := finder.CityByName("Paars", "FR"); got == nil || got.Name != "Paris" {
		t.Fatalf("distance-2 typo: got %+v, want Paris", got)
	}
	if got := finder.CityByName("Pars", "DE"); got != nil {
		t.Fatalf("country filter must reject cross-country fuzzy hits, got %q", got.Name)
	}
	// Warm: the second identical typo comes from the cache.
	if got := finder.CityByName("Pars", "FR"); got == nil || got.Name != "Paris" {
		t.Fatalf("cached distance-1 typo: got %+v, want Paris", got)
	}
}

// TestFuzzyOverflowCoversPostBuildAdds proves names added after a committed
// build are fuzzy-searchable via the overflow list, and that a forced
// rebuild folds them in and clears the list.
func TestFuzzyOverflowCoversPostBuildAdds(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())
	finder.CityByName("Pars", "FR") // force the build
	require.Equal(t, int32(fuzzyBuilt), finder.fuzzyState.Load())

	finder.AddCity(city.SpatialCity{
		City: city.City{Name: "Berlin", Country: "DE", Latitude: 52.52, Longitude: 13.40},
	})

	// Distance-1 typo resolves via the overflow scan.
	if got := finder.CityByName("Berli", "DE"); got == nil || got.Name != "Berlin" {
		t.Fatalf("post-build AddCity must be fuzzy-findable via overflow: got %+v", got)
	}
	finder.mutex.RLock()
	over := len(finder.fuzzyOverflow)
	finder.mutex.RUnlock()
	assert.Positive(t, over, "the added name must sit in the overflow list")

	// A fresh build folds the overflow away.
	finder.fuzzyState.Store(fuzzyNotBuilt)
	finder.CityByName("Pars", "FR")
	require.Equal(t, int32(fuzzyBuilt), finder.fuzzyState.Load())
	finder.mutex.RLock()
	over = len(finder.fuzzyOverflow)
	ngrams := finder.ngrams
	finder.mutex.RUnlock()
	assert.Zero(t, over, "a committed build must clear the overflow list")
	require.NotNil(t, ngrams)
	assert.Contains(t, ngrams.search("Berli", 1), "Berlin", "the rebuilt structure must contain the added name")
}

// TestCityByNameConcurrentDuringNGramBuild is the race-safety gate for the
// lazy build: while one goroutine pays the build, others hammer exact hits,
// typo lookups, and misses. Run under -race this must stay clean: the
// immutable-structure swap, the overflow list, and the fuzzy cache are all
// shared mutable state during the window.
func TestCityByNameConcurrentDuringNGramBuild(t *testing.T) {
	cities := gateCities(150_000)
	finder := BuildIndex(cities)

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		finder.CityByName("zzz-triggers-the-build", "GC")
		close(done)
	}()

	deadline := time.Now().Add(30 * time.Second)
	var wg2 sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg2.Add(1)
		go func(w int) {
			defer wg2.Done()
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				default:
				}
				if time.Now().After(deadline) {
					return
				}
				finder.CityByName(fmt.Sprintf("GateCity%06d", (w*37+i)%150000), "GC")
				finder.CityByName(fmt.Sprintf("GateCitt%06d", (w*41+i)%150000), "GC")
				finder.CityByName(fmt.Sprintf("no-such-%d-%d", w, i), "GC")
			}
		}(w)
	}
	wg2.Wait()
	wg.Wait()

	// After the build settles, typo lookups resolve through the structure.
	require.Eventually(t, func() bool {
		got := finder.CityByName("GateCitt000042", "GC")
		return got != nil && got.Name == "GateCity000042"
	}, 10*time.Second, 50*time.Millisecond, "typos must resolve once the build has settled")
}

// TestNGramApproxBytesSanity checks the size reporter used by the scale gates
// reports a plausible positive number.
func TestNGramApproxBytesSanity(t *testing.T) {
	index, err := buildNGramIndex(ngramCorpus())
	require.NoError(t, err)
	assert.Positive(t, index.approxBytes())
	assert.NotEmpty(t, index.search("Paris", 1))
	assert.True(t, strings.Contains(strings.Join(index.search("Paris", 1), ","), "Paris"))
}
