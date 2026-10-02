package postalCode

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileHeader mirrors the on-disk header contract: a small struct gob-encoded
// as the FIRST value of an index file, before the payload. The field names
// and types ARE the wire format; keeping an independent copy in the test
// freezes the format — renaming or retyping a production indexHeader field
// breaks these tests instead of silently invalidating every stored index.
type fileHeader struct {
	Magic   string
	Version uint32
	Count   int
}

const (
	testIndexMagic   = "CFPOSTIDX"
	testIndexVersion = uint32(4)
)

// readFileHeader decodes only the leading header value from path.
func readFileHeader(t *testing.T, path string) fileHeader {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var h fileHeader
	require.NoError(t, gob.NewDecoder(f).Decode(&h), "the first gob value in an index file must be the header")
	return h
}

// writeHeaderAndPayload writes a hand-crafted index file: header first, then
// an optional payload, so wrong-magic/version/count files can be simulated.
// The payload is written RAW (no zstd frame): exactly the shape a pre-v3
// writer or a foreign tool would leave behind, which the payload decoder must
// reject. Tests that need a decodable v3 payload use writeFramedIndex.
func writeHeaderAndPayload(t *testing.T, path string, h fileHeader, payload any) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(h))
	if payload != nil {
		require.NoError(t, enc.Encode(payload))
	}
	require.NoError(t, f.Close())
}

// writeFramedIndex writes a structurally valid framed file: raw gob header
// followed by one zstd frame (SpeedFastest, CRC) holding the gob payload.
// Count validation tests need a payload that survives decompression so the
// count check is what rejects the file.
func writeFramedIndex(t *testing.T, path string, h fileHeader, payload any) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(h))
	zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderCRC(true))
	require.NoError(t, err)
	require.NoError(t, gob.NewEncoder(zw).Encode(payload))
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}

func buildTestPostalFinder() *Finder {
	finder := NewPostalCodeFinder()
	for _, entry := range testPostalCodeEntries {
		finder.AddPostalCode(entry)
	}
	return finder
}

// countPostalEntries counts the postal code records across all countries of
// a finder; this is the quantity the header Count records.
func countPostalEntries(finder *Finder) int {
	return finder.Len()
}

// legacyPayload is the v2/v3 payload shape: the old Finder's exported
// country -> code -> PostalCodeEntry map.
func legacyPayload() *payloadV3 {
	m := map[string]map[string]dataLoader.PostalCodeEntry{}
	for _, e := range testPostalCodeEntries {
		if m[e.CountryCode] == nil {
			m[e.CountryCode] = map[string]dataLoader.PostalCodeEntry{}
		}
		m[e.CountryCode][e.PostalCode] = e
	}
	return &payloadV3{PostalCode: m}
}

// TestDeserializeIndex_ReadsLegacyV3 pins the upgrade path: a v3 file (the
// full loader map) loads into the compact table, answers exactly like an
// index built from the same entries, and reports LegacyFormat so the
// initializer rewrites it as v4.
func TestDeserializeIndex_ReadsLegacyV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_v3.gob")
	writeFramedIndex(t, path,
		fileHeader{Magic: testIndexMagic, Version: indexVersionV3, Count: len(testPostalCodeEntries)},
		legacyPayload())
	got, err := DeserializeIndex(path)
	require.NoError(t, err)
	assert.True(t, got.LegacyFormat())
	want := buildTestPostalFinder()
	assert.False(t, want.LegacyFormat())
	for _, e := range testPostalCodeEntries {
		assert.Equal(t, want.CityByPostalCode(e.PostalCode, e.CountryCode), got.CityByPostalCode(e.PostalCode, e.CountryCode))
	}

	// Rewritten as v4, it round-trips to the same answers.
	v4 := filepath.Join(t.TempDir(), "postal_v4.gob")
	require.NoError(t, got.SerializeIndex(v4))
	assert.Equal(t, testIndexVersion, readFileHeader(t, v4).Version)
	again, err := DeserializeIndex(v4)
	require.NoError(t, err)
	assert.False(t, again.LegacyFormat())
	for _, e := range testPostalCodeEntries {
		assert.Equal(t, want.CityByPostalCode(e.PostalCode, e.CountryCode), again.CityByPostalCode(e.PostalCode, e.CountryCode))
	}
}

func TestSerializeIndex_WritesHeaderFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_code_index.gob")
	finder := buildTestPostalFinder()
	require.NoError(t, finder.SerializeIndex(path))

	h := readFileHeader(t, path)
	assert.Equal(t, testIndexMagic, h.Magic)
	assert.Equal(t, testIndexVersion, h.Version)
	assert.Equal(t, len(testPostalCodeEntries), h.Count, "header count must record the total number of entries")

	// The atomic-write protocol must not leave a .part file behind on success.
	_, statErr := os.Stat(path + ".part")
	assert.True(t, os.IsNotExist(statErr), "no .part file may remain after a successful serialize")
}

func TestSerializeDeserialize_RoundTripValidatesHeaderAndCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_code_index.gob")
	finder := buildTestPostalFinder()
	require.NoError(t, finder.SerializeIndex(path))

	got, err := DeserializeIndex(path)
	require.NoError(t, err)
	assert.Equal(t, countPostalEntries(finder), countPostalEntries(got))

	c := got.CityByPostalCode("10001", "US")
	require.NotNil(t, c)
	assert.Equal(t, "New York", c.Name)
}

func TestDeserializeIndex_TruncatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_code_index.gob")
	finder := buildTestPostalFinder()
	require.NoError(t, finder.SerializeIndex(path))

	full, err := os.ReadFile(path)
	require.NoError(t, err)
	// Keep only the first half of the bytes: exactly the prefix a crash
	// mid-write would leave behind.
	require.NoError(t, os.WriteFile(path, full[:len(full)/2], 0o600))

	_, err = DeserializeIndex(path)
	require.Error(t, err, "a truncated index must fail to deserialize")
	assert.Contains(t, err.Error(), "truncated or from an incompatible version",
		"the error must tell the user the file is unusable and how to fix it")
	assert.Contains(t, err.Error(), path, "the error must name the offending file")
}

func TestDeserializeIndex_WrongMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_code_index.gob")
	writeHeaderAndPayload(t, path,
		fileHeader{Magic: "NOTCFPST", Version: testIndexVersion, Count: len(testPostalCodeEntries)},
		legacyPayload())

	_, err := DeserializeIndex(path)
	require.Error(t, err, "a file with a foreign magic must be rejected")
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
	assert.Contains(t, err.Error(), path)
}

func TestDeserializeIndex_WrongVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_code_index.gob")
	writeHeaderAndPayload(t, path,
		fileHeader{Magic: testIndexMagic, Version: testIndexVersion + 1, Count: len(testPostalCodeEntries)},
		legacyPayload())

	_, err := DeserializeIndex(path)
	require.Error(t, err, "a file written by a future format version must be rejected")
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
}

func TestDeserializeIndex_CountMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_code_index.gob")
	// A fully decodable frame whose header count disagrees with the
	// payload: only the count check can reject it.
	writeFramedIndex(t, path,
		fileHeader{Magic: testIndexMagic, Version: indexVersionV3, Count: len(testPostalCodeEntries) + 7},
		legacyPayload())

	_, err := DeserializeIndex(path)
	require.Error(t, err, "a payload whose entry count disagrees with the header must be rejected")
	assert.ErrorIs(t, err, ErrCorruptIndex)
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
}

// A v2 file (the format this lane replaces: raw gob payload, no zstd frame)
// must fail the version check before any payload decoding could run. The
// initializer test covers the rebuild half of the contract.
func TestDeserializeIndex_V2FileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_code_index_v2.gob")
	// A faithful v2 stream: header with version 2 followed by the raw gob
	// payload v2 wrote.
	writeHeaderAndPayload(t, path,
		fileHeader{Magic: testIndexMagic, Version: 2, Count: len(testPostalCodeEntries)},
		legacyPayload())

	_, err := DeserializeIndex(path)
	require.Error(t, err, "a v2 file must not decode into the v3 format")
	assert.ErrorIs(t, err, ErrCorruptIndex)
	assert.Contains(t, err.Error(), "unsupported version 2")
}

// A file written by the PREVIOUS, header-less format must be rejected by the
// magic check instead of silently decoding into zero-valued data. gob matches
// struct fields by name, so the old payload decodes into an all-zero header.
func TestDeserializeIndex_LegacyHeaderlessFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal_code_index_legacy.gob")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, gob.NewEncoder(f).Encode(legacyPayload()))
	require.NoError(t, f.Close())

	_, err = DeserializeIndex(path)
	require.Error(t, err, "an old header-less index must not be silently accepted")
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
}

func TestSerializeIndex_FailedWriteLeavesNoPart(t *testing.T) {
	dir := t.TempDir()
	finder := buildTestPostalFinder()

	// Case 1: create failure (parent directory missing).
	missing := filepath.Join(dir, "no-such-dir", "postal_code_index.gob")
	assert.Error(t, finder.SerializeIndex(missing))

	// Case 2: encode succeeds but the final rename fails: the target path is
	// a non-empty directory, so moving the .part file onto it must fail.
	target := filepath.Join(dir, "occupied")
	require.NoError(t, os.Mkdir(target, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o600))
	assert.Error(t, finder.SerializeIndex(target))

	for _, p := range []string{missing + ".part", target + ".part"} {
		_, statErr := os.Stat(p)
		assert.True(t, os.IsNotExist(statErr), "no .part file may remain after a failed serialize: %s", p)
	}
	// And the original target directory content is untouched.
	_, statErr := os.Stat(filepath.Join(target, "keep"))
	assert.NoError(t, statErr)
}
