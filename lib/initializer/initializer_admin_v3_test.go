package initializer

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the v3 format-hop gate for the initializer (design gate 2 in
// docs/design/admin-attribution-v1.1.md): a v2-headed s2 or postal index must
// be rejected with the package's ErrCorruptIndex and the existing rebuild
// fallback must rewrite it as v3 from the source datasets, carrying the admin
// attribution arrays a v2 payload cannot represent. It also covers design
// gate 3 (admin1-names present/absent modes) and pins the name index at v2.

// v2IndexHeader mirrors the shared header shape (gob matches fields by name,
// so the local copy is wire-compatible).
type v2IndexHeader struct {
	Magic   string
	Version uint32
	Count   int
}

// writeV2S2Index synthesizes a faithful v2 s2 index: CFS2IDX header with
// version 2 followed by the raw gob payload v2 wrote (Cities only — no admin
// arrays, no zstd frame). The version check must reject it before any
// payload decoding could zero-fill the admin ids.
func writeV2S2Index(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(v2IndexHeader{Magic: "CFS2IDX", Version: 2, Count: 2}))
	require.NoError(t, enc.Encode(struct{ Cities []struct{ Name string } }{}))
	require.NoError(t, f.Close())
}

// writeV2PostalIndex synthesizes a faithful v2 postal index: CFPOSTIDX
// header with version 2 followed by the raw gob Finder payload.
func writeV2PostalIndex(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(v2IndexHeader{Magic: "CFPOSTIDX", Version: 2, Count: 2}))
	require.NoError(t, enc.Encode(postalCode.NewPostalCodeFinder()))
	require.NoError(t, f.Close())
}

// readFileVersion decodes the leading header of an index file and returns
// its version — the wire-level assertion that a rebuild rewrote the file in
// the expected format.
func readFileVersion(t *testing.T, path string) uint32 {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var h v2IndexHeader
	require.NoError(t, gob.NewDecoder(f).Decode(&h), "the first gob value in an index file must be the header")
	return h.Version
}

// TestEnsureFinders_RebuildsV2S2IndexAsV3 covers the deployed-v2-file
// scenario: every index file exists (so the dataset load is skipped), but the
// s2 index carries a v2 header. Initialization must recover by loading the
// datasets once, rebuilding, and re-serializing as v3 — including the admin
// id arrays the v2 payload cannot represent. A second ensureFinders then
// warm-starts off the repaired v3 file.
func TestEnsureFinders_RebuildsV2S2IndexAsV3(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f1, err := ensureFinders(cfg, "")
	require.NoError(t, err)
	require.NotNil(t, f1)

	// Replace the fresh v3 s2 index with a v2-headed file.
	s2Path := filepath.Join(dir, cfg.S2.IndexFile)
	writeV2S2Index(t, s2Path)

	// The v2 file is rejected as corrupt by the deserializer itself.
	_, err = coordinates.DeserializeIndex(s2Path)
	require.ErrorIs(t, err, coordinates.ErrCorruptIndex, "a v2 header must fail the version check")

	f2, err := ensureFinders(cfg, "")
	require.NoError(t, err, "a v2 s2 index must trigger a rebuild, not a fatal error")
	require.NotNil(t, f2)

	// The rebuilt finder attributes from the source data (writeTinyDatasets
	// rows: Roc Meler is AD admin1 "02", les Escaldes AD "07"; neither has
	// admin2).
	_, _, attr, err := f2.S2Finder.NearestPlaceWithAdmin(42.50729, 1.53414, coordinates.RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "07", attr.Admin1Code)
	assert.Equal(t, "", attr.Admin2Code)
	assert.Len(t, f2.S2Finder.Admin1Codes, 2, "AD.02 and AD.07 both intern")
	assert.Equal(t, []int32{0, 1}, f2.S2Finder.Admin1IDs)

	// The bad file must have been replaced by a v3 index that decodes cleanly.
	repaired, err := coordinates.DeserializeIndex(s2Path)
	require.NoError(t, err, "the corrupt file must be rewritten with a valid v3 index")
	_, _, attr, err = repaired.NearestPlaceWithAdmin(42.5876, 1.7418, coordinates.RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "02", attr.Admin1Code)

	// Warm start after repair: still v3, no rebuild loop.
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.AllCitiesFile)))
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.PostalCodesFile)))
	f3, err := ensureFinders(cfg, "")
	require.NoError(t, err)
	assert.Equal(t, uint32(3), readFileVersion(t, s2Path), "the repaired file must stay v3 across warm starts")
	_, _, attr, err = f3.S2Finder.NearestPlaceWithAdmin(42.50729, 1.53414, coordinates.RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "07", attr.Admin1Code)
}

// TestEnsureFinders_RebuildsV2PostalIndexAsV3: the same deployed-v2-file
// scenario for the postal index (zstd framing only; payload struct
// unchanged).
func TestEnsureFinders_RebuildsV2PostalIndexAsV3(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	_, err := ensureFinders(cfg, "")
	require.NoError(t, err)

	postalPath := filepath.Join(dir, cfg.PostalCodeIndexFile)
	writeV2PostalIndex(t, postalPath)

	_, err = postalCode.DeserializeIndex(postalPath)
	require.ErrorIs(t, err, postalCode.ErrCorruptIndex, "a v2 header must fail the version check")

	f2, err := ensureFinders(cfg, "")
	require.NoError(t, err, "a v2 postal index must trigger a rebuild, not a fatal error")
	require.NotNil(t, f2)

	c := f2.PostalCodeFinder.CityByPostalCode("AD100", "AD")
	require.NotNil(t, c, "the rebuilt postal finder must contain the source data")
	assert.Equal(t, "Canillo", c.Name)

	assert.Equal(t, uint32(3), readFileVersion(t, postalPath), "the corrupt file must be rewritten as v3")
}

// TestEnsureFinders_NameIndexStaysV2 pins the untouched-format invariant of
// this lane: city.City does not change, so the name index format must NOT
// bump — after a cold build the on-disk header still says version 2, and no
// mixed-version state across the three files breaks initialization (each
// header validates and rebuilds independently).
func TestEnsureFinders_NameIndexStaysV2(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f, err := ensureFinders(cfg, "")
	require.NoError(t, err)
	require.NotNil(t, f)

	namePath := filepath.Join(dir, cfg.NameIndexFile)
	assert.Equal(t, uint32(2), readFileVersion(t, namePath),
		"the name index format must stay v2: City did not change in the admin-attribution lane")
	assert.Equal(t, uint32(3), readFileVersion(t, filepath.Join(dir, cfg.S2.IndexFile)))
	assert.Equal(t, uint32(3), readFileVersion(t, filepath.Join(dir, cfg.PostalCodeIndexFile)))
}

// TestEnsureFinders_Admin1NamesModes covers design gate 3: with the optional
// names file present, warm and cold starts attach the map; with the path set
// but the file absent, initialization degrades to codes-only (nil map) and
// queries still succeed.
func TestEnsureFinders_Admin1NamesModes(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	namesPath := filepath.Join(dir, "admin1CodesASCII.txt")
	require.NoError(t, os.WriteFile(namesPath, []byte(
		"AD.02\tCanillo\tCanillo\t3042004\n"+
			"AD.07\tAndorra la Vella\tAndorra la Vella\t3041563\n"), 0o600))

	// Cold start with names.
	f, err := ensureFinders(cfg, namesPath)
	require.NoError(t, err)
	require.NotNil(t, f.S2Finder.Admin1Names)
	assert.Equal(t, "Andorra la Vella", f.S2Finder.Admin1Names["AD.07"])
	_, _, attr, err := f.S2Finder.NearestPlaceWithAdmin(42.50729, 1.53414, coordinates.RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "Andorra la Vella", attr.Admin1Name)

	// Warm start with names (re-attached per boot, not serialized).
	f2, err := ensureFinders(cfg, namesPath)
	require.NoError(t, err)
	require.NotNil(t, f2.S2Finder.Admin1Names, "warm start must re-attach the names map")
	_, _, attr, err = f2.S2Finder.NearestPlaceWithAdmin(42.50729, 1.53414, coordinates.RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "Andorra la Vella", attr.Admin1Name)

	// Codes-only: configured-but-missing file degrades, never fails.
	missing := filepath.Join(dir, "absent_admin1.txt")
	f3, err := ensureFinders(cfg, missing)
	require.NoError(t, err, "a missing admin1 names file must degrade to codes-only, not fail startup")
	assert.Nil(t, f3.S2Finder.Admin1Names)
	_, _, attr, err = f3.S2Finder.NearestPlaceWithAdmin(42.50729, 1.53414, coordinates.RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "07", attr.Admin1Code)
	assert.Equal(t, "", attr.Admin1Name, "codes-only mode serves the code with no name")
}

// TestEnsureAdmin1Names pins the three modes of the loader gate directly.
func TestEnsureAdmin1Names(t *testing.T) {
	assert.Nil(t, ensureAdmin1Names(""), "empty path = not configured = silent nil")

	assert.Nil(t, ensureAdmin1Names(filepath.Join(t.TempDir(), "absent.txt")),
		"missing file = codes-only degradation")

	path := filepath.Join(t.TempDir(), "admin1CodesASCII.txt")
	require.NoError(t, os.WriteFile(path, []byte("US.CA\tCalifornia\tCalifornia\t5332921\n"), 0o600))
	names := ensureAdmin1Names(path)
	assert.Equal(t, map[string]string{"US.CA": "California"}, names)
}
