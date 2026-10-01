package name

import (
	"math/rand"
	"os"
	"testing"
	"time"
	"unicode/utf8"
)

// TestNGramProfile is an env-gated CPU/heap profile harness for a focused
// set of slow distance-2 queries at prod scale. It mirrors TestNGramScaleGate
// but runs only the d2 workload under -cpuprofile/-memprofile flags.
//
//	FUZZY_SCALE_BENCH=... go test ./lib/finder/name/ -run TestNGramProfile \
//	  -cpuprofile /tmp/ngram.prof -count=1 -timeout 2h
func TestNGramProfile(t *testing.T) {
	indexFile := os.Getenv("FUZZY_SCALE_BENCH")
	if indexFile == "" {
		t.Skip("FUZZY_SCALE_BENCH not set")
	}
	finder, err := DeserializeIndex(indexFile)
	if err != nil {
		t.Fatal(err)
	}
	finder.mutex.RLock()
	names := finder.namesFromIndex()
	finder.mutex.RUnlock()

	rng := rand.New(rand.NewSource(42))
	shuffled := append([]string(nil), names...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	index := buildNGramIndex(shuffled)

	var queries []string
	for i := 0; len(queries) < 300; i++ {
		base := shuffled[rng.Intn(len(shuffled))]
		if utf8.RuneCountInString(base) < 5 {
			continue
		}
		queries = append(queries, mangle(base, 2))
	}

	start := time.Now()
	var hits int
	for _, q := range queries {
		if got := index.search(q, 2); len(got) > 0 {
			hits++
		}
	}
	t.Logf("300 d2 queries in %v (%.1f ms/query avg), %d with hits", time.Since(start), float64(time.Since(start).Microseconds())/300/1000*1000/1000, hits)
}
