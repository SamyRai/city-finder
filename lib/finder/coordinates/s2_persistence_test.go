package coordinates

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
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
	testIndexMagic   = "CFS2IDX"
	testIndexVersion = uint32(3)
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

// writeFramedIndex writes a structurally valid v3 file: raw gob header
// followed by one zstd frame (SpeedFastest, CRC) holding the gob payload.
// Count/id validation tests need a payload that survives decompression so
// the check under test is what rejects the file.
func writeFramedIndex(t *testing.T, path string, h fileHeader, payload SerializableS2Finder) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(h))
	zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderCRC(true))
	require.NoError(t, err)
	require.NoError(t, gob.NewEncoder(zw).Encode(&payload))
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}

func buildTestS2Finder(t *testing.T) *S2Finder {
	t.Helper()
	f, err := BuildIndex(testCities)
	require.NoError(t, err)
	return f
}

func TestSerializeIndex_WritesHeaderFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	finder := buildTestS2Finder(t)
	require.NoError(t, finder.SerializeIndex(path))

	h := readFileHeader(t, path)
	assert.Equal(t, testIndexMagic, h.Magic)
	assert.Equal(t, testIndexVersion, h.Version)
	assert.Equal(t, len(testCities), h.Count, "header count must record len(Cities)")

	// The atomic-write protocol must not leave a .part file behind on success.
	_, statErr := os.Stat(path + ".part")
	assert.True(t, os.IsNotExist(statErr), "no .part file may remain after a successful serialize")
}

func TestSerializeDeserialize_RoundTripValidatesHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	finder := buildTestS2Finder(t)
	require.NoError(t, finder.SerializeIndex(path))

	got, err := DeserializeIndex(path)
	require.NoError(t, err)
	require.Len(t, got.Cities, len(testCities))

	nearest, _, err := got.NearestPlace(37.7750, -122.4190, RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "San Francisco", nearest.Name)
}

func TestDeserializeIndex_TruncatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	finder := buildTestS2Finder(t)
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

func TestDeserializeIndex_GarbageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	require.NoError(t, os.WriteFile(path, []byte("this is not gob data"), 0o600))

	_, err := DeserializeIndex(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
}

func TestDeserializeIndex_WrongMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	writeHeaderAndPayload(t, path,
		fileHeader{Magic: "NOTCFS2", Version: testIndexVersion, Count: len(testCities)},
		SerializableS2Finder{Cities: nil})

	_, err := DeserializeIndex(path)
	require.Error(t, err, "a file with a foreign magic must be rejected")
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
	assert.Contains(t, err.Error(), path)
}

func TestDeserializeIndex_WrongVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	writeHeaderAndPayload(t, path,
		fileHeader{Magic: testIndexMagic, Version: testIndexVersion + 1, Count: len(testCities)},
		SerializableS2Finder{Cities: nil})

	_, err := DeserializeIndex(path)
	require.Error(t, err, "a file written by a future format version must be rejected")
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
}

func TestDeserializeIndex_CountMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	payload := SerializableS2Finder{
		Cities: []city.City{
			{Name: "A", Latitude: 1, Longitude: 1},
			{Name: "B", Latitude: 2, Longitude: 2},
		},
		Admin1IDs: []int32{-1, -1},
		Admin2IDs: []int32{-1, -1},
	}
	// A fully decodable v3 frame whose header count disagrees with the
	// payload: only the count check can reject it.
	writeFramedIndex(t, path,
		fileHeader{Magic: testIndexMagic, Version: testIndexVersion, Count: len(payload.Cities) + 7},
		payload)

	_, err := DeserializeIndex(path)
	require.Error(t, err, "a payload whose length disagrees with the header count must be rejected")
	assert.ErrorIs(t, err, ErrCorruptIndex)
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
}

// A v2 file (the format this lane replaces: raw gob payload, no admin
// arrays, no zstd frame) must fail the version check before any payload
// decoding could run, so it can never load as a v3 struct with zero-filled
// admin ids. The initializer test covers the rebuild half of the contract.
func TestDeserializeIndex_V2FileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index_v2.gob")
	// A faithful v2 stream: header with version 2 followed by the raw gob
	// payload v2 wrote (Cities only — the admin arrays did not exist).
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(fileHeader{Magic: testIndexMagic, Version: 2, Count: 1}))
	require.NoError(t, enc.Encode(SerializableS2Finder{
		Cities: []city.City{{Name: "Legacy", Latitude: 1, Longitude: 1}},
	}))
	require.NoError(t, f.Close())

	_, err = DeserializeIndex(path)
	require.Error(t, err, "a v2 file must not decode into the v3 struct")
	assert.ErrorIs(t, err, ErrCorruptIndex)
	assert.Contains(t, err.Error(), "unsupported version 2")
}

// A file written by the PREVIOUS, header-less format must be rejected by the
// magic check instead of silently decoding into zero-valued data. gob matches
// struct fields by name, so the old payload decodes into an all-zero header.
func TestDeserializeIndex_LegacyHeaderlessFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index_legacy.gob")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, gob.NewEncoder(f).Encode(SerializableS2Finder{
		Cities: []city.City{{Name: "Legacy", Latitude: 1, Longitude: 1}},
	}))
	require.NoError(t, f.Close())

	_, err = DeserializeIndex(path)
	require.Error(t, err, "an old header-less index must not be silently accepted")
	assert.Contains(t, err.Error(), "truncated or from an incompatible version")
}

func TestSerializeIndex_FailedWriteLeavesNoPart(t *testing.T) {
	dir := t.TempDir()
	finder := buildTestS2Finder(t)

	// Case 1: create failure (parent directory missing).
	missing := filepath.Join(dir, "no-such-dir", "s2index.gob")
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
