package name

import (
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"testing"
	"time"
	"unicode/utf8"
)

// TestNGramScaleGate is the env-gated scale measurement behind the fuzzy
// go/no-go decision (lane brief: build at 1M names, report structure memory
// and typo p50/p99, extrapolate to prod). It is skipped unless
// FUZZY_SCALE_BENCH points at a serialized v2 name index (e.g.
// datasets-v2/name_index.gob); FUZZY_SCALE_SCALES optionally selects the
// scales as a comma list of "100k", "1m", "all".
//
//	go test ./lib/finder/name/ -run TestNGramScaleGate -v \
//	  -timeout 2h            # default: 100k,1m
//	FUZZY_SCALE_BENCH=... FUZZY_SCALE_SCALES=all go test ...   # adds prod

func settleHeap() uint64 {
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func scaleMultiplier(scale string) int {
	switch scale {
	case "100k":
		return 100_000
	case "1m":
		return 1_000_000
	case "all":
		return -1
	}
	return 0
}

func TestNGramScaleGate(t *testing.T) {
	indexFile := os.Getenv("FUZZY_SCALE_BENCH")
	if indexFile == "" {
		t.Skip("FUZZY_SCALE_BENCH not set; scale gate is opt-in (needs a serialized prod index)")
	}
	scalesCsv := os.Getenv("FUZZY_SCALE_SCALES")
	if scalesCsv == "" {
		scalesCsv = "100k,1m"
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

	// Deterministic sampling and typo generation so runs are comparable.
	rng := rand.New(rand.NewSource(42))
	shuffled := append([]string(nil), names...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	for _, scale := range splitScales(scalesCsv) {
		n := scaleMultiplier(scale)
		if n < 0 || n > len(shuffled) {
			n = len(shuffled)
		}
		corpus := shuffled[:n]

		before := settleHeap()
		buildStart := time.Now()
		index, err := buildNGramIndex(corpus)
		if err != nil {
			t.Fatalf("buildNGramIndex at scale %q: %v", scale, err)
		}
		buildDur := time.Since(buildStart)
		structure := settleHeap() - before
		runtime.KeepAlive(index)

		// Typo workload: 1k real names (>= 5 runes so edits stay inside the
		// completeness envelope), 1-2 edits applied.
		var queries [][2]string // query, distance
		for i := 0; len(queries) < 1000; i++ {
			base := corpus[rng.Intn(len(corpus))]
			if utf8.RuneCountInString(base) < 5 {
				continue
			}
			queries = append(queries,
				[2]string{mangle(base, 1), "1"},
				[2]string{mangle(base, 2), "2"},
			)
		}

		lat1 := measureSearch(index, queries, 1)
		lat2 := measureSearch(index, queries, 2)

		t.Logf("SCALE %s: names=%d build=%v structure=%d B (%.2f GiB) approxBytes=%d B postings=%d grams=%d",
			scale, n, buildDur.Round(time.Millisecond), structure, float64(structure)/(1<<30),
			index.approxBytes(), len(index.post), len(index.gramIDs))
		report(t, "d1", lat1)
		report(t, "d2", lat2)
		runtime.KeepAlive(index)
	}
}

func splitScales(csv string) []string {
	var out []string
	cur := ""
	for _, r := range csv {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func measureSearch(index *ngramIndex, queries [][2]string, d int) []time.Duration {
	// Only queries mangled to exactly this distance.
	var lat []time.Duration
	for _, q := range queries {
		if q[1] != string(rune('0'+d)) {
			continue
		}
		start := time.Now()
		index.search(q[0], d)
		lat = append(lat, time.Since(start))
	}
	return lat
}

func report(t *testing.T, name string, lat []time.Duration) {
	if len(lat) == 0 {
		t.Logf("RESULT fuzzy %s n=0", name)
		return
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration { return lat[int(float64(len(lat)-1)*p)] }
	t.Logf("RESULT fuzzy %s n=%d p50=%v p99=%v p999=%v max=%v",
		name, len(lat), pct(0.50), pct(0.99), pct(0.999), lat[len(lat)-1])
}
