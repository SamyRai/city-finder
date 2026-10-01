package name

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
)

// internFixtureCities builds a dataset shaped like GeoNames data: every base
// name appears in several countries and every city carries two alternate
// names, so each city is referenced from three index entries. Under the v2
// format the file still stores each city exactly once; the shape exercises
// the reference rehydration, not struct duplication.
//
// Country strings are deliberately long-ish ("RepublicOfNaniaNN", 18 chars):
// the v2 intern pass only collapses Country backings, so the measured heap
// delta must come from those duplicates.
func internFixtureCities(scale int) []city.SpatialCity {
	n := 50000 * scale
	cities := make([]city.SpatialCity, n)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City: city.City{
				Name:    fmt.Sprintf("IntnCity%05d", i%(5000*scale)),
				Country: fmt.Sprintf("RepublicOfNania%02d", i%5),
			},
			AltNames: []string{
				fmt.Sprintf("IntnAlt%05d", (i*3)%(15000*scale)),
				fmt.Sprintf("IntnOld%05d", (i*7)%(25000*scale)),
			},
		}
	}
	return cities
}

// decodeUninterned replicates DeserializeIndex's v2 decode sequence — raw gob
// header, zstd-framed payload, pointer-table rehydration — without the
// Country-only intern pass, so the pass can be measured in isolation. It
// returns the rehydrated finder plus the decoded distinct-city slice its
// references point into (interning that slice's Country fields after the fact
// is exactly what the production pre-rehydration pass does, minus ordering).
// Each measurement variant must run in its own test process: the
// unique-package intern table is global, so a pass measured second would not
// pay (or benefit from) table state left by an earlier variant.
func decodeUninterned(t *testing.T, path string) (*Finder, []city.City) {
	t.Helper()
	file, err := os.Open(path)
	assert.NoError(t, err)
	defer func() { _ = file.Close() }()

	bufFile := bufio.NewReader(file)
	var header indexHeader
	assert.NoError(t, gob.NewDecoder(bufFile).Decode(&header))
	assert.Equal(t, nameIndexMagic, header.Magic)
	assert.Equal(t, nameIndexVersion, header.Version)

	compressed, err := io.ReadAll(bufFile)
	assert.NoError(t, err)
	payloadBytes, err := decodeZstdFrame(compressed)
	assert.NoError(t, err)

	var payload nameIndexPayloadV2
	assert.NoError(t, gob.NewDecoder(bytes.NewReader(payloadBytes)).Decode(&payload))

	ptrs := make([]*city.City, len(payload.Cities))
	for i := range payload.Cities {
		ptrs[i] = &payload.Cities[i]
	}

	// Rehydrate into the flat tables exactly the way DeserializeIndex does,
	// minus the intern pass under measurement.
	finder := NewNameFinder()
	finder.cities = ptrs
	for country, refs := range payload.Refs {
		tbl, err := buildTableFromRefs(refs, len(ptrs))
		assert.NoError(t, err)
		finder.countries[country] = tbl
	}
	return finder, payload.Cities
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
// Country intern reclaims and verifies the interned index still serves
// lookups.
//
// With INTERN_MEASURE=none it only decodes (the no-interning baseline); each
// variant must run in its own `go test` process for a clean intern table.
//
// v1 history: interning the inverted-index map KEYS was also tried and
// dropped — on 50K/200K-city synthetic indexes the unique-table growth for
// key-only strings (alternate names exist only as map keys) outweighed the
// freed key duplicates, leaving the final heap up to ~0.5 MB larger. v2 keeps
// that decision for names and interns only the Country field over the
// distinct-city table.
func TestDeserializeInterningSavesMemory(t *testing.T) {
	scale := 1
	if s := os.Getenv("INTERN_SCALE"); s != "" {
		var v int
		fmt.Sscan(s, &v)
		if v > 0 {
			scale = v
		}
	}

	// Build through the production staging path: load into the nested
	// staging map, then flatten into the finder's tables (BuildIndex's
	// sequence without its logging and GC).
	fixture := internFixtureCities(scale)
	index := make(map[string]map[string][]*city.City, estimateCapacity(fixture))
	processBatchStreamlined(index, fixture)
	f := NewNameFinder()
	f.buildFromIndexMap(index)

	path := t.TempDir() + "/intern_measure.gob"
	assert.NoError(t, f.SerializeIndex(path))

	finder, cities := decodeUninterned(t, path)
	decoded := internHeap(t)

	if os.Getenv("INTERN_MEASURE") == "none" {
		t.Logf("MODE=none decoded heap: %d B", decoded)
		runtime.KeepAlive(finder)
		runtime.KeepAlive(cities)
		return
	}

	internDecodedCountries(cities)
	interned := internHeap(t)

	saved := int64(decoded) - int64(interned)
	t.Logf("decoded heap: %d B; after Country intern: %d B; saved: %d B (%.1f MB, %.1f%%)",
		decoded, interned, saved, float64(saved)/1024/1024, 100*float64(saved)/float64(decoded))

	// The fixture decodes 50K*scale country strings of 18 bytes each; the
	// pass must reclaim a measurable fraction of that (~24 B size class each,
	// so up to ~1.2 MB per scale unit).
	assert.Greater(t, saved, int64(scale)*128*1024,
		"the Country intern pass must save a measurable share of the duplicated country backings")

	// The interned index must still resolve exact and fuzzy lookups.
	name := fmt.Sprintf("IntnCity%05d", 1)
	got := finder.CityByName(name, "RepublicOfNania01")
	if !assert.NotNil(t, got, "exact lookup after interning") {
		return
	}
	assert.Equal(t, name, got.Name)
	// The fuzzy index builds in the background now; warm it before the
	// fuzzy assertion so the first typo query does not race the build.
	finder.WarmFuzzy()
	waitFuzzyBuilt(t, finder)
	assert.NotNil(t, finder.CityByName(fmt.Sprintf("IntnCitt%05d", 1), "RepublicOfNania01"), "distance-1 fuzzy lookup after interning")
	runtime.KeepAlive(finder)
}
