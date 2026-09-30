package name

import (
	"encoding/gob"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
)

// internFixtureCities builds a dataset shaped like GeoNames data: every base
// name appears in several countries and every city carries two alternate
// names, so each city is referenced from three index entries (duplicating the
// decoded City values and their strings).
func internFixtureCities(scale int) []city.SpatialCity {
	n := 50000 * scale
	cities := make([]city.SpatialCity, n)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:    fmt.Sprintf("IntnCity%05d", i%(5000*scale)),
				Country: fmt.Sprintf("N%02d", i%5),
			},
			AltNames: []string{
				fmt.Sprintf("IntnAlt%05d", (i*3)%(15000*scale)),
				fmt.Sprintf("IntnOld%05d", (i*7)%(25000*scale)),
			},
		}
	}
	return cities
}

// decodeUninterned replicates DeserializeIndex's decode sequence without the
// post-decode interning pass, so interning can be measured in isolation. Each
// measurement variant must run in its own test process: the unique-package
// intern table is global, so a pass measured second would not pay (or benefit
// from) table state left by an earlier variant.
func decodeUninterned(t *testing.T, path string) *Finder {
	t.Helper()
	file, err := os.Open(path)
	assert.NoError(t, err)
	defer func() { _ = file.Close() }()

	decoder := gob.NewDecoder(file)
	var header indexHeader
	assert.NoError(t, decoder.Decode(&header))
	finder := NewNameFinder()
	assert.NoError(t, decoder.Decode(&finder.InvertedIndex))
	assert.NoError(t, decoder.Decode(&finder.BKTree))
	assert.NoError(t, decoder.Decode(&finder.isBKTreeBuilt))
	assert.NoError(t, decoder.Decode(&finder.allNames))
	return finder
}

func internHeap(t *testing.T) uint64 {
	t.Helper()
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestDeserializeInterningSavesMemory measures how much heap the post-decode
// interning reclaims and verifies the interned index still serves lookups.
//
// With INTERN_MEASURE=none it only decodes (the no-interning baseline); each
// variant must run in its own `go test` process for a clean intern table.
//
// Measurement history: interning the inverted-index map KEYS was also tried
// and dropped - on 50K/200K-city synthetic indexes the unique-table growth
// for key-only strings (alternate names exist only as map keys) outweighed
// the freed key duplicates, leaving the final heap up to ~0.5 MB larger. See
// the comment on internDecodedStrings.
func TestDeserializeInterningSavesMemory(t *testing.T) {
	scale := 1
	if s := os.Getenv("INTERN_SCALE"); s != "" {
		var v int
		fmt.Sscan(s, &v)
		if v > 0 {
			scale = v
		}
	}

	f := NewFinderWithCapacity(estimateCapacity(internFixtureCities(scale)))
	f.processBatchStreamlined(internFixtureCities(scale))

	path := t.TempDir() + "/intern_measure.gob"
	assert.NoError(t, f.SerializeIndex(path))

	finder := decodeUninterned(t, path)
	decoded := internHeap(t)

	if os.Getenv("INTERN_MEASURE") == "none" {
		t.Logf("MODE=none decoded heap: %d B", decoded)
		runtime.KeepAlive(finder)
		return
	}

	finder.internDecodedStrings()
	interned := internHeap(t)

	saved := int64(decoded) - int64(interned)
	t.Logf("decoded heap: %d B; after interning: %d B; saved: %d B (%.1f MB, %.1f%%)",
		decoded, interned, saved, float64(saved)/1024/1024, 100*float64(saved)/float64(decoded))

	// The fixture duplicates every city across 3 index entries, so the pass
	// must reclaim measurable MB.
	assert.Greater(t, saved, int64(scale)*1024*1024, "post-decode interning must save at least 1 MB per fixture scale unit")

	// The interned index must still resolve exact and fuzzy lookups.
	name := fmt.Sprintf("IntnCity%05d", 1)
	got := finder.CityByName(name, "N01")
	if !assert.NotNil(t, got, "exact lookup after interning") {
		return
	}
	assert.Equal(t, name, got.Name)
	assert.NotNil(t, finder.CityByName(fmt.Sprintf("IntnCitt%05d", 1), "N01"), "distance-1 fuzzy lookup after interning")
	runtime.KeepAlive(finder)
}
