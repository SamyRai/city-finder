package name

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestFuzzyBudgetScaleGate is the env-gated measurement behind the v1.1
// candidate-budget decision (lane brief: measure the standard typo workload
// plus an adversarial 1–3-rune workload at 1M and prod, before and after the
// cap, and sweep budgets to pick the default). It shares the methodology of
// TestNGramScaleGate — same seed, shuffle, and typo generation — so its
// "unlimited" numbers are directly comparable with the v1.0 design-note
// table. Skipped unless FUZZY_SCALE_BENCH points at a serialized v2 name
// index (read-only; nothing writes back to the datasets dir).
//
//	FUZZY_SCALE_BENCH=datasets/name_index.gob \
//	FUZZY_BUDGET_SCALES=1m go test ./lib/finder/name/ \
//	  -run TestFuzzyBudgetScaleGate -v -timeout 2h
//
// FUZZY_BUDGET_SCALES defaults to "1m"; "all" runs the full deserialized
// keyspace (prod). FUZZY_BUDGET_SWEEP overrides the swept budgets as a comma
// list of integers ("unlimited" is always measured first as the before).
func TestFuzzyBudgetScaleGate(t *testing.T) {
	indexFile := os.Getenv("FUZZY_SCALE_BENCH")
	if indexFile == "" {
		t.Skip("FUZZY_SCALE_BENCH not set; scale gate is opt-in (needs a serialized prod index)")
	}
	scalesCsv := os.Getenv("FUZZY_BUDGET_SCALES")
	if scalesCsv == "" {
		scalesCsv = "1m"
	}
	sweepCsv := os.Getenv("FUZZY_BUDGET_SWEEP")
	var sweep []int
	if sweepCsv == "" {
		sweep = []int{250_000, 500_000, 1_000_000, 2_000_000, 4_000_000}
	} else {
		for _, s := range strings.Split(sweepCsv, ",") {
			b, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				t.Fatalf("FUZZY_BUDGET_SWEEP entry %q: %v", s, err)
			}
			sweep = append(sweep, b)
		}
	}

	finder, err := DeserializeIndex(indexFile)
	if err != nil {
		t.Fatalf("deserialize %s: %v", indexFile, err)
	}
	finder.mutex.RLock()
	totalKeys := finder.totalIndexKeys()
	names := finder.namesFromIndex()
	finder.mutex.RUnlock()
	t.Logf("index: %d (country,name) keys, %d distinct names", totalKeys, len(names))

	// Same deterministic sampling as TestNGramScaleGate (seed 42) so the
	// unlimited-run numbers line up with the published v1.0 table.
	rng := rand.New(rand.NewSource(42))
	shuffled := append([]string(nil), names...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	for _, scale := range splitScales(scalesCsv) {
		n := scaleMultiplier(scale)
		if n < 0 || n > len(shuffled) {
			n = len(shuffled)
		}
		corpus := shuffled[:n]

		index, err := buildNGramIndex(corpus)
		if err != nil {
			t.Fatalf("buildNGramIndex at scale %q: %v", scale, err)
		}
		t.Logf("SCALE %s: names=%d postings=%d grams=%d", scale, n, len(index.post), len(index.gramIDs))

		std := standardQueries(rng, corpus)
		adv := adversarialQueries(rng, corpus)

		// Before (unlimited) then the sweep. budget < 0 disables the cap.
		origBudget := FuzzyMaxCandidates
		configs := append([]int{-1}, sweep...)
		for _, budget := range configs {
			label := "unlimited"
			if budget >= 0 {
				label = fmt.Sprintf("%d", budget)
			}
			FuzzyMaxCandidates = budget
			runtime.GC()
			measureScaleWorkload(t, index, "std", label, std)
			measureScaleWorkload(t, index, "adv", label, adv)
		}
		FuzzyMaxCandidates = origBudget
	}
}

// standardQueries reproduces the design-note typo workload: 1k real names
// (>= 5 runes so edits stay inside the completeness envelope), 1-2 edits.
func standardQueries(rng *rand.Rand, corpus []string) [][2]string {
	var queries [][2]string // query, distance
	for len(queries) < 1000 {
		base := corpus[rng.Intn(len(corpus))]
		if utf8.RuneCountInString(base) < 5 {
			continue
		}
		queries = append(queries,
			[2]string{mangle(base, 1), "1"},
			[2]string{mangle(base, 2), "2"},
		)
	}
	return queries
}

// adversarialQueries builds the weak-filter workload: 1k queries of 1-3
// runes. Prefixes of real corpus names keep every gram present in the index
// (the maximally degenerate case — the walked lists are the real giant
// sentinel- and common-gram lists), with the rune count cycling 1/2/3.
func adversarialQueries(rng *rand.Rand, corpus []string) [][2]string {
	var queries [][2]string
	for len(queries) < 1000 {
		base := corpus[rng.Intn(len(corpus))]
		runes := []rune(base)
		if len(runes) < 3 || utf8.RuneCountInString(base) < 5 {
			continue
		}
		k := 1 + len(queries)%3
		queries = append(queries, [2]string{string(runes[:k]), "0"})
	}
	return queries
}

// measureScaleWorkload runs every query at both distances (CityByName tries
// d=1 then d=2 on a miss) and reports the latency distribution, truncation
// count, and the walked/verified diagnostics per distance.
func measureScaleWorkload(t *testing.T, index *ngramIndex, workload, label string, queries [][2]string) {
	t.Helper()
	for _, d := range []int{1, 2} {
		var lat []time.Duration
		var walked []int64
		var verified []int64
		truncated := 0
		for _, q := range queries {
			start := time.Now()
			_, trunc := index.search(q[0], d)
			lat = append(lat, time.Since(start))
			walked = append(walked, fuzzyWalkedLast.Load())
			verified = append(verified, fuzzyVerifiedLast.Load())
			if trunc {
				truncated++
			}
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		sort.Slice(walked, func(i, j int) bool { return walked[i] < walked[j] })
		sort.Slice(verified, func(i, j int) bool { return verified[i] < verified[j] })
		pct := func(s []time.Duration, p float64) time.Duration { return s[int(float64(len(s)-1)*p)] }
		t.Logf("RESULT budget=%s workload=%s d=%d n=%d p50=%v p99=%v p999=%v max=%v trunc=%d/%d walked_p50=%d walked_max=%d verified_max=%d",
			label, workload, d, len(lat), pct(lat, 0.50), pct(lat, 0.99), pct(lat, 0.999), lat[len(lat)-1],
			truncated, len(lat), walked[len(walked)/2], walked[len(walked)-1], verified[len(verified)-1])
	}
}
