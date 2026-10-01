package initializer

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the v2 format-hop gate for the initializer (design gate 2 in
// docs/design/index-format-v2.md): a v1-headed name index must be rejected
// with name.ErrCorruptIndex and the existing rebuild fallback must rewrite it
// as v2 from the source datasets, carrying the new Population field. It adds
// a new test file only; initializer.go itself is unchanged.

// v1NameHeader mirrors the v1 name index header shape (gob matches fields by
// name, so the local copy is wire-compatible).
type v1NameHeader struct {
	Magic   string
	Version uint32
	Count   int
}

// writeV1HeadedNameIndex synthesizes a faithful v1 name index: CFNAMEIDX
// header with version 1 followed by the raw map payload v1 wrote. The
// version check must reject it before any payload decoding could zero-fill
// Population.
func writeV1HeadedNameIndex(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := gob.NewEncoder(f)
	require.NoError(t, enc.Encode(v1NameHeader{Magic: "CFNAMEIDX", Version: 1, Count: 1}))
	require.NoError(t, enc.Encode(map[string]map[string][]*city.City{
		"AD": {"les Escaldes": {{Name: "les Escaldes", Country: "AD", Latitude: 42.50729, Longitude: 1.53414}}},
	}))
	require.NoError(t, f.Close())
}

// TestEnsureFinders_RebuildsV1NameIndexAsV2 covers the deployed-v1-file
// scenario: every index file exists (so the dataset load is skipped), but the
// name index carries a v1 header. Initialization must recover by loading the
// datasets once, rebuilding, and re-serializing as v2 — including the
// Population field the v1 payload cannot represent. A second Initialize then
// warm-starts off the repaired v2 files.
func TestEnsureFinders_RebuildsV1NameIndexAsV2(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f1, err := ensureFinders(cfg)
	require.NoError(t, err)
	require.NotNil(t, f1)

	// Replace the fresh v2 name index with a v1-headed file.
	namePath := filepath.Join(dir, cfg.NameIndexFile)
	writeV1HeadedNameIndex(t, namePath)

	// The v1 file is rejected as corrupt by the deserializer itself.
	_, err = name.DeserializeIndex(namePath)
	require.ErrorIs(t, err, name.ErrCorruptIndex, "a v1 header must fail the version check")

	f2, err := ensureFinders(cfg)
	require.NoError(t, err, "a v1 name index must trigger a rebuild, not a fatal error")
	require.NotNil(t, f2)

	// The rebuilt finder answers queries from the source data, and the
	// population (GeoNames field 14 = 16316 for les Escaldes) survived the
	// rebuild that the v1 payload could not have provided.
	c := f2.NameFinder.CityByName("les Escaldes", "AD")
	require.NotNil(t, c, "the rebuilt name finder must contain the source data")
	assert.Equal(t, "les Escaldes", c.Name)
	assert.Equal(t, int32(16316), c.Population,
		"the rebuilt v2 index must carry population from the source datasets")

	// The bad file must have been replaced by a v2 index that decodes cleanly
	// and preserves the population end to end.
	repaired, err := name.DeserializeIndex(namePath)
	require.NoError(t, err, "the corrupt file must be rewritten with a valid v2 index")
	rp := repaired.CityByName("les Escaldes", "AD")
	require.NotNil(t, rp)
	assert.Equal(t, int32(16316), rp.Population)

	// With every index valid again, a warm start succeeds even without the
	// raw datasets.
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.AllCitiesFile)))
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.PostalCodesFile)))
	f3, err := ensureFinders(cfg)
	require.NoError(t, err, "warm start must succeed after the v1 file was repaired to v2")
	require.NotNil(t, f3)
	wp := f3.NameFinder.CityByName("les Escaldes", "AD")
	require.NotNil(t, wp)
	assert.Equal(t, int32(16316), wp.Population, "population must survive the v2 warm start too")
}
