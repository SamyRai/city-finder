package name

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
)

// TestSerializedStreamStartsWithHeader pins the on-disk contract: the first
// value in the gob stream is the versioning header (magic CFNAMEIDX, version
// 2, count = number of country sub-maps).
func TestSerializedStreamStartsWithHeader(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities()) // 5 countries: FR, GB, DE, JP, ES

	path := filepath.Join(t.TempDir(), "name.gob")
	assert.NoError(t, finder.SerializeIndex(path))

	file, err := os.Open(path)
	assert.NoError(t, err)
	defer func() { _ = file.Close() }()

	var header indexHeader
	assert.NoError(t, gob.NewDecoder(file).Decode(&header))
	assert.Equal(t, nameIndexMagic, header.Magic)
	assert.Equal(t, nameIndexVersion, header.Version)
	assert.Equal(t, len(refsSnapshot(finder)), header.Count)
}

// TestDeserializeRejectsIncompatibleHeaders covers the header-validation
// paths: every rejected file must produce a descriptive error that tells the
// caller to rebuild (so the initializer can treat any of these as a
// rebuildable decode failure).
func TestDeserializeRejectsIncompatibleHeaders(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, encode func(*gob.Encoder) error) string {
		path := filepath.Join(dir, name)
		file, err := os.Create(path)
		assert.NoError(t, err)
		assert.NoError(t, encode(gob.NewEncoder(file)))
		assert.NoError(t, file.Close())
		return path
	}

	// A faithful v1 payload stand-in: the raw nested index the v1 format
	// wrote (struct-per-reference). The flat in-memory index no longer stores
	// this shape, but the bytes on disk are what the version check rejects,
	// so the literal keeps the fixture honest without storage coupling.
	v1Payload := map[string]map[string][]*city.City{
		"FR": {"Paris": {{Name: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}}},
	}

	cases := []struct {
		desc        string
		path        string
		wantSub     []string // substrings the error must mention
		wantCorrupt bool     // error must (not) wrap ErrCorruptIndex
	}{
		{
			desc: "wrong magic",
			path: write("wrong_magic.gob", func(enc *gob.Encoder) error {
				return enc.Encode(&indexHeader{Magic: "CFS2INDEX", Version: nameIndexVersion, Count: 1})
			}),
			wantSub:     []string{nameIndexMagic, "rebuilt"},
			wantCorrupt: true,
		},
		{
			desc: "v1 file is rejected, not decoded",
			path: write("v1_file.gob", func(enc *gob.Encoder) error {
				// A faithful v1 stream: header with version 1 followed by the
				// raw nested index (struct-per-reference payload). The
				// rejection must come from the version check, before any
				// payload decoding could zero-fill Population.
				if err := enc.Encode(&indexHeader{Magic: nameIndexMagic, Version: 1, Count: 1}); err != nil {
					return err
				}
				return enc.Encode(v1Payload)
			}),
			wantSub:     []string{"version", "rebuilt"},
			wantCorrupt: true,
		},
		{
			desc: "future version",
			path: write("future_version.gob", func(enc *gob.Encoder) error {
				return enc.Encode(&indexHeader{Magic: nameIndexMagic, Version: nameIndexVersion + 1, Count: 1})
			}),
			wantSub:     []string{"version", "rebuilt"},
			wantCorrupt: true,
		},
		{
			desc: "legacy headerless stream",
			path: write("legacy.gob", func(enc *gob.Encoder) error {
				return enc.Encode(v1Payload) // pre-header format: payload first
			}),
			wantSub:     []string{"legacy", "rebuilt"},
			wantCorrupt: true,
		},
		{
			desc:        "empty file",
			path:        write("empty.gob", func(enc *gob.Encoder) error { return nil }),
			wantSub:     []string{"rebuilt"},
			wantCorrupt: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			finder, err := DeserializeIndex(tc.path)
			assert.Nil(t, finder, "rejected files must not yield a finder")
			if !assert.Error(t, err, "DeserializeIndex must reject %s", tc.desc) {
				return
			}
			for _, sub := range tc.wantSub {
				assert.Contains(t, err.Error(), sub)
			}
			assert.ErrorIs(t, err, ErrCorruptIndex,
				"decode failures must wrap ErrCorruptIndex so the initializer can rebuild")
		})
	}
}

// TestDeserializeMissingFileIsNotCorrupt pins the sentinel boundary from the
// other side: an fs error from opening a nonexistent file must NOT wrap
// ErrCorruptIndex, or the initializer would paper over broken filesystems by
// rebuilding instead of failing.
func TestDeserializeMissingFileIsNotCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does_not_exist.gob")
	finder, err := DeserializeIndex(path)
	assert.Nil(t, finder)
	if !assert.Error(t, err, "opening a nonexistent index must fail") {
		return
	}
	assert.NotErrorIs(t, err, ErrCorruptIndex,
		"fs errors must stay distinguishable from corruption")
	assert.True(t, os.IsNotExist(err), "the underlying cause must be the fs error")
}

// TestSerializeIndexAtomicParts verifies the temp+rename discipline: a
// successful write leaves the final file and no ".part" sibling, a failed
// create leaves nothing behind, and the renamed file decodes cleanly.
func TestSerializeIndexAtomicParts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "name.gob")
	finder := BuildIndex(fuzzyFixtureCities())

	assert.NoError(t, finder.SerializeIndex(path))

	_, err := os.Stat(path)
	assert.NoError(t, err, "final index file must exist after serialization")
	_, err = os.Stat(path + ".part")
	assert.True(t, os.IsNotExist(err), "no .part file may survive a successful serialization")

	restored, err := DeserializeIndex(path)
	assert.NoError(t, err)
	if assert.NotNil(t, restored.CityByName("Paris", "FR"), "renamed file must decode and serve lookups") {
		assert.Equal(t, "Paris", restored.CityByName("Paris", "FR").Name)
	}

	// A create failure (unwritable directory) must not litter a .part file.
	badPath := filepath.Join(dir, "no_such_dir", "name.gob")
	assert.Error(t, finder.SerializeIndex(badPath))
	_, err = os.Stat(badPath + ".part")
	assert.True(t, os.IsNotExist(err), "failed serialization must remove its .part file")
}
