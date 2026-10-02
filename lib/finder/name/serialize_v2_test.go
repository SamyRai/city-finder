package name

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeV2File builds a v2 file with a handcrafted header and payload in the
// production framing: raw gob header, then the payload gob-compressed into
// one CRC-checked zstd frame. Tests use it to reach validation paths behind
// the compression layer.
func writeV2File(t *testing.T, path string, header indexHeader, payload nameIndexPayloadV2) {
	t.Helper()
	var raw bytes.Buffer
	require.NoError(t, gob.NewEncoder(&raw).Encode(&payload))

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(nameIndexZstdLevel), zstd.WithEncoderCRC(nameIndexZstdCRC))
	require.NoError(t, err)
	compressed := enc.EncodeAll(raw.Bytes(), nil)

	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, gob.NewEncoder(f).Encode(&header))
	_, werr := f.Write(compressed)
	require.NoError(t, werr)
	require.NoError(t, f.Close())
}

// readV2File decodes a v2 file exactly the way the production reader layers
// it: the raw gob header first (shared bufio), then the decompressed payload
// bytes. Tests use it to inspect the decompressed stream.
func readV2File(t *testing.T, path string) (indexHeader, []byte) {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	br := bufio.NewReader(f)
	var header indexHeader
	require.NoError(t, gob.NewDecoder(br).Decode(&header))

	compressed, err := io.ReadAll(br)
	require.NoError(t, err)
	payloadBytes, err := decodeZstdFrame(compressed)
	require.NoError(t, err)
	return header, payloadBytes
}

// refsSnapshot reconstructs the per-country name -> city-pointers view of a
// Finder's flat tables plus its overflow (sorted-table ids first, overflow
// ids appended after — the same order the payload build merges them). It is
// the test-side replacement for the old InvertedIndex field: same shape,
// derived from the flat storage, so DeepEqual/Same assertions keep their
// original strength.
func refsSnapshot(f *Finder) map[string]map[string][]*city.City {
	f.mutex.RLock()
	defer f.mutex.RUnlock()

	out := make(map[string]map[string][]*city.City, len(f.countries)+len(f.overflow))
	for country, t := range f.countries {
		refs := make(map[string][]*city.City, len(t.names))
		for i, name := range t.names {
			// Always assign, even for zero-width CSR ranges: a name indexed
			// with no references is a present-but-empty key.
			cityList := make([]*city.City, 0, t.starts[i+1]-t.starts[i])
			for _, id := range t.ids[t.starts[i]:t.starts[i+1]] {
				cityList = append(cityList, f.cities.at(id))
			}
			refs[name] = cityList
		}
		for name, ids := range f.overflow[country] {
			for _, id := range ids {
				refs[name] = append(refs[name], f.cities.at(id))
			}
		}
		out[country] = refs
	}
	for country, countryOverflow := range f.overflow {
		if _, ok := f.countries[country]; ok {
			continue // merged into its sorted table above
		}
		refs := make(map[string][]*city.City, len(countryOverflow))
		for name, ids := range countryOverflow {
			for _, id := range ids {
				refs[name] = append(refs[name], f.cities.at(id))
			}
		}
		out[country] = refs
	}
	return out
}

// sharedCityFixture builds a dataset whose cities are each referenced under
// several names (primary + alternates), and one city that is manually indexed
// under two different countries — the shape the v2 id table must dedupe by
// pointer identity across both names and countries.
func sharedCityFixture() (*Finder, []*city.City) {
	f := NewNameFinder()
	// Row ids: 0 = Paris, 1 = London.
	f.cities = cityTable{base: []city.City{
		{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35, Population: 2_161_000},
		{Name: "London", Country: "GB", Latitude: 51.50, Longitude: -0.12, Population: 8_982_000},
	}, baseCount: 2}
	// Flattened through the same path BuildIndex uses; the empty-ref key is
	// preserved as a zero-width CSR range.
	f.buildFromIndexMap(map[string]map[string][]int32{
		"FR": {
			"Paris": {0, 1}, // cross-country sharing: the SAME row under two countries. Real
			// builders never produce this, but the format must not corrupt it.
			"Lutèce": {0}, // cross-name sharing within one country
			"Paname": {0},
			"Orly":   {}, // empty ref list: preserved as a key with no ids
		},
		"GB": {
			"London":    {1},
			"Londres":   {1},
			"Big Smoke": {1, 0},
		},
	})
	return f, []*city.City{f.cities.at(0), f.cities.at(1)}
}

// indexStats captures the quantities the count-validation gate compares.
type indexStats struct {
	distinctCities int
	totalRefs      int
	perCountryKeys map[string]int
}

func statsOf(f *Finder) indexStats {
	snapshot := refsSnapshot(f)
	seen := make(map[*city.City]struct{})
	perCountry := make(map[string]int)
	total := 0
	for country, countryMap := range snapshot {
		perCountry[country] = len(countryMap)
		for _, cityList := range countryMap {
			total += len(cityList)
			for _, c := range cityList {
				seen[c] = struct{}{}
			}
		}
	}
	return indexStats{distinctCities: len(seen), totalRefs: total, perCountryKeys: perCountry}
}

func serializeToTemp(t *testing.T, f *Finder) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "name_v2.gob")
	require.NoError(t, f.SerializeIndex(path))
	return path
}

// TestSerializeV2RoundTripDeepEqual proves the core v2 contract: serialize →
// deserialize yields an index deep-equal to the original, with the empty-ref
// key and the cross-country ref order preserved.
func TestSerializeV2RoundTripDeepEqual(t *testing.T) {
	original, _ := sharedCityFixture()
	path := serializeToTemp(t, original)

	restored, err := DeserializeIndex(path)
	require.NoError(t, err)

	assert.True(t, reflect.DeepEqual(refsSnapshot(original), refsSnapshot(restored)),
		"v2 round trip must restore the inverted index exactly")
	assert.NotNil(t, restored.CityByName("Paris", "FR"))
	assert.NotNil(t, restored.CityByName("Londres", "GB"))
}

// TestSerializeV2PointerSharing pins the semantics the v1 format could not
// carry: after deserialize, the *city.City reachable under two different
// names is the SAME pointer — mutate through one, observe through the other.
func TestSerializeV2PointerSharing(t *testing.T) {
	original, _ := sharedCityFixture()
	path := serializeToTemp(t, original)

	restored, err := DeserializeIndex(path)
	require.NoError(t, err)
	snapshot := refsSnapshot(restored)

	// Cross-name sharing within a country.
	paris := snapshot["FR"]["Paris"][0]
	lutèce := snapshot["FR"]["Lutèce"][0]
	assert.Same(t, paris, lutèce, "one city under two names must deserialize to one pointer")

	// Cross-country sharing: the London entry inside FR must be the same
	// pointer as London inside GB.
	londonFR := snapshot["FR"]["Paris"][1]
	londonGB := snapshot["GB"]["London"][0]
	assert.Same(t, londonFR, londonGB, "one city under two countries must deserialize to one pointer")

	// Mutation through one reference is observable from every other.
	paris.Population = 42
	assert.Equal(t, int32(42), lutèce.Population, "mutating via one name must be visible via the other")
	assert.Equal(t, int32(42), refsSnapshot(restored)["GB"]["Big Smoke"][1].Population,
		"mutating via one country must be visible via another")

	// The empty ref list must deserialize as a present-but-empty key, not as
	// a nil lookup.
	refs, exists := refsSnapshot(restored)["FR"]["Orly"]
	assert.True(t, exists, "a name with zero refs must survive as a key")
	assert.Empty(t, refs)
}

// TestSerializeV2CountValidation is design gate 3: distinct-city count, total
// reference count, and per-country key counts are all preserved across the
// round trip.
func TestSerializeV2CountValidation(t *testing.T) {
	original, _ := sharedCityFixture()
	want := statsOf(original)
	require.Equal(t, 2, want.distinctCities, "fixture must hold exactly two distinct pointers")
	require.Equal(t, 8, want.totalRefs)

	path := serializeToTemp(t, original)
	restored, err := DeserializeIndex(path)
	require.NoError(t, err)

	got := statsOf(restored)
	assert.Equal(t, want.distinctCities, got.distinctCities, "distinct city count must be preserved")
	assert.Equal(t, want.totalRefs, got.totalRefs, "total reference count must be preserved")
	assert.Equal(t, want.perCountryKeys, got.perCountryKeys, "per-country key counts must be preserved")
}

// TestSerializeV2FileDoesNotContainRuntimeState verifies the dropped trailer:
// the decompressed v2 stream must decode header + payload and then END. Any
// trailing value means the file was not written by this format — the guard
// that keeps lazy-build runtime state (fuzzy structure, overflow) out of the
// file.
func TestSerializeV2FileDoesNotContainRuntimeState(t *testing.T) {
	original, _ := sharedCityFixture()
	original.ensureFuzzyBuilt() // runtime fuzzy state must not leak into the file
	path := serializeToTemp(t, original)

	_, payloadBytes := readV2File(t, path)

	payloadDec := gob.NewDecoder(bytes.NewReader(payloadBytes))
	var payload nameIndexPayloadV3
	require.NoError(t, payloadDec.Decode(&payload))

	// Decode into the type v1 wrote next (bool isBKTreeBuilt): a clean v2
	// stream must be exhausted here, so the expected error is EOF — not a
	// type mismatch from leftover runtime state.
	var leftover bool
	err := payloadDec.Decode(&leftover)
	assert.ErrorIs(t, err, io.EOF,
		"the v2 stream must end after the payload: no BK-tree, no isBKTreeBuilt, no allNames")
}

// TestDeserializeV2HeaderCountMismatch rejects a payload whose country count
// disagrees with the header (the count-validation gate at file level).
func TestDeserializeV2HeaderCountMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad_count.gob")
	writeV2File(t, path,
		indexHeader{Magic: nameIndexMagic, Version: nameIndexVersionV2, Count: 7},
		nameIndexPayloadV2{
			Cities: []city.City{{Name: "Paris", Country: "FR"}},
			Refs:   map[string]map[string][]int32{"FR": {"Paris": {0}}},
		})

	_, err := DeserializeIndex(path)
	assert.ErrorIs(t, err, ErrCorruptIndex, "a country-count mismatch is corruption")
	assert.Contains(t, err.Error(), "recorded 7")
}

// TestDeserializeV2RefOutsideCityTable rejects an id that points past the
// distinct-city table — the rehydration bounds check must return an error,
// not panic, on corrupt input.
func TestDeserializeV2RefOutsideCityTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad_ref.gob")
	writeV2File(t, path,
		indexHeader{Magic: nameIndexMagic, Version: nameIndexVersionV2, Count: 1},
		nameIndexPayloadV2{
			Cities: []city.City{{Name: "Paris", Country: "FR"}},
			Refs:   map[string]map[string][]int32{"FR": {"Paris": {0, 99}}},
		})

	_, err := DeserializeIndex(path)
	assert.ErrorIs(t, err, ErrCorruptIndex, "an out-of-range city id is corruption")
	assert.Contains(t, err.Error(), "outside")
}

// TestDeserializeZstdCorruptionIsCorruptIndex pins the zstd-layer corruption
// contract: a file whose frame is damaged mid-payload — or truncated — must
// surface as ErrCorruptIndex (rebuildable corruption), never as a panic or a
// wrapped fs error (which the initializer would treat as fatal).
func TestDeserializeZstdCorruptionIsCorruptIndex(t *testing.T) {
	// A payload large enough that a corruption window at 2/3 of the file is
	// safely inside the zstd frame, well past the tiny raw header.
	cities := make([]city.SpatialCity, 2000)
	for i := range cities {
		cities[i] = city.SpatialCity{
			City:     city.City{Name: "CorruptCity" + string(rune('A'+i%26)) + string(rune('a'+i%26)) + string(rune('0'+i%10)) + "0123456789", Country: "CC", Latitude: float64(i), Longitude: float64(i)},
			AltNames: []string{"CorruptAlt" + string(rune('A'+i%26)) + "0123456789"},
		}
	}
	finder := BuildIndex(cities)
	path := serializeToTemp(t, finder)
	originalBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Greater(t, len(originalBytes), 4096, "fixture must produce a multi-KB frame")

	t.Run("mid-payload byte corruption", func(t *testing.T) {
		corrupt := append([]byte(nil), originalBytes...)
		off := len(corrupt) * 2 / 3
		for i := 0; i < 64; i++ { // a window, not a single byte: deterministic damage
			corrupt[off+i] ^= 0xFF
		}
		corruptPath := filepath.Join(t.TempDir(), "corrupt_mid.gob")
		require.NoError(t, os.WriteFile(corruptPath, corrupt, 0o600))

		f, err := DeserializeIndex(corruptPath)
		assert.Nil(t, f, "a corrupted frame must not yield a finder")
		require.Error(t, err, "a corrupted frame must fail, not panic")
		assert.ErrorIs(t, err, ErrCorruptIndex, "zstd corruption is rebuildable corruption")
		assert.NotErrorIs(t, err, os.ErrNotExist, "must not masquerade as an fs error")
	})

	t.Run("truncated frame", func(t *testing.T) {
		cut := len(originalBytes) / 2
		truncPath := filepath.Join(t.TempDir(), "trunc.gob")
		require.NoError(t, os.WriteFile(truncPath, originalBytes[:cut], 0o600))

		f, err := DeserializeIndex(truncPath)
		assert.Nil(t, f)
		require.Error(t, err, "a truncated frame must fail, not panic")
		assert.ErrorIs(t, err, ErrCorruptIndex, "truncation is rebuildable corruption")
		assert.NotErrorIs(t, err, os.ErrNotExist, "must not masquerade as an fs error")
	})
}

// TestAddCityAfterDeserializeV2 proves the post-deserialize mutation path:
// AddCity appends fresh pointers (not table ids), old and new lookups work,
// and re-serializing produces a valid v2 file whose id table was rebuilt
// cleanly over old shared pointers plus the new one.
func TestAddCityAfterDeserializeV2(t *testing.T) {
	original, _ := sharedCityFixture()
	path := serializeToTemp(t, original)

	restored, err := DeserializeIndex(path)
	require.NoError(t, err)

	// The added city is reachable under its primary name and an alt name.
	restored.AddCity(city.SpatialCity{
		City:     city.City{Name: "Berlin", Country: "DE", Latitude: 52.52, Longitude: 13.40, Population: 3_664_088},
		AltNames: []string{"Berlín"},
	})

	berlin := restored.CityByName("Berlin", "DE")
	require.NotNil(t, berlin, "the added city must be findable by exact name")
	assert.Equal(t, int32(3_664_088), berlin.Population)
	assert.NotNil(t, restored.CityByName("Berlín", "DE"), "the added alt name must be findable")
	assert.NotNil(t, restored.CityByName("Paris", "FR"), "pre-existing entries must keep resolving")

	// The added refs are new pointers: distinct from every table-rehydrated
	// pointer, and the two names see the same new pointer.
	de := refsSnapshot(restored)["DE"]
	assert.Same(t, de["Berlin"][0], de["Berlín"][0], "AddCity's primary and alt names must share one pointer")

	// Re-serialize: the id table is rebuilt from scratch each time, so the
	// mix of rehydrated and added pointers must serialize and deserialize
	// cleanly, with pointer sharing intact on both sides.
	path2 := serializeToTemp(t, restored)
	again, err := DeserializeIndex(path2)
	require.NoError(t, err)

	assert.NotNil(t, again.CityByName("Berlin", "DE"), "the added city must survive a second round trip")
	assert.NotNil(t, again.CityByName("Lutèce", "FR"), "the original cities must survive a second round trip")

	want, got := statsOf(restored), statsOf(again)
	assert.Equal(t, want.distinctCities, got.distinctCities, "distinct city count across re-serialization")
	assert.Equal(t, want.totalRefs, got.totalRefs, "total refs across re-serialization")
	assert.Equal(t, want.perCountryKeys, got.perCountryKeys, "per-country keys across re-serialization")

	assert.Same(t, refsSnapshot(again)["FR"]["Paris"][0], refsSnapshot(again)["FR"]["Paname"][0],
		"pointer sharing must survive the second round trip")
	assert.Same(t, refsSnapshot(again)["DE"]["Berlin"][0], refsSnapshot(again)["DE"]["Berlín"][0],
		"the added city's sharing must survive the second round trip")
}
