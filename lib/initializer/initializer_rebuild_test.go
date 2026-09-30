package initializer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureFinders_RebuildsTruncatedS2Index covers the crash-mid-write
// scenario: every index file exists (so the dataset load was skipped), but
// the S2 index is a truncated prefix. Initialization must recover by loading
// the datasets once, rebuilding, and re-serializing over the bad file. A
// second Initialize then warm-starts off the repaired files.
func TestEnsureFinders_RebuildsTruncatedS2Index(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f1, err := ensureFinders(cfg)
	require.NoError(t, err)
	require.NotNil(t, f1)

	// Truncate the serialized S2 index to half its bytes.
	s2Path := filepath.Join(dir, cfg.S2.IndexFile)
	full, err := os.ReadFile(s2Path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(s2Path, full[:len(full)/2], 0o600))

	f2, err := ensureFinders(cfg)
	require.NoError(t, err, "a truncated index must trigger a rebuild, not a fatal error")
	require.NotNil(t, f2)

	// The rebuilt finder must answer queries from the source data.
	cityResult, _, err := f2.S2Finder.NearestPlace(42.5876, 1.7418)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(cityResult.Name, "Roc Meler"),
		"unexpected nearest city %q", cityResult.Name)

	// The bad file must have been replaced by a valid, deserializable one.
	repaired, err := coordinates.DeserializeIndex(s2Path)
	require.NoError(t, err, "the corrupt file must be rewritten with a valid index")
	assert.NotEmpty(t, repaired.Cities)

	// With every index valid again, a warm start succeeds even without the
	// raw datasets.
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.AllCitiesFile)))
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.PostalCodesFile)))
	f3, err := ensureFinders(cfg)
	require.NoError(t, err, "warm start must succeed after the index was repaired")
	require.NotNil(t, f3)
}

// TestEnsureFinders_RebuildsGarbagePostalCodeIndex covers undecodable garbage
// in the postal code index: same rebuild-once contract as the S2 index.
func TestEnsureFinders_RebuildsGarbagePostalCodeIndex(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f1, err := ensureFinders(cfg)
	require.NoError(t, err)
	require.NotNil(t, f1)

	postalPath := filepath.Join(dir, cfg.PostalCodeIndexFile)
	require.NoError(t, os.WriteFile(postalPath, []byte("<html>saved as gob</html>"), 0o600))

	f2, err := ensureFinders(cfg)
	require.NoError(t, err, "an undecodable postal index must trigger a rebuild, not a fatal error")

	c := f2.PostalCodeFinder.CityByPostalCode("AD100", "AD")
	require.NotNil(t, c, "the rebuilt postal finder must contain the source data")
	assert.Equal(t, "Canillo", c.Name)

	repaired, err := postalCode.DeserializeIndex(postalPath)
	require.NoError(t, err, "the corrupt file must be rewritten with a valid index")
	c2 := repaired.CityByPostalCode("AD200", "AD")
	require.NotNil(t, c2)
	assert.Equal(t, "Encamp", c2.Name)
}

// TestEnsureFinders_RebuildsCorruptNameIndex covers the name index path: the
// rebuild fallback is not S2/postal specific.
func TestEnsureFinders_RebuildsCorruptNameIndex(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f1, err := ensureFinders(cfg)
	require.NoError(t, err)
	require.NotNil(t, f1)

	namePath := filepath.Join(dir, cfg.NameIndexFile)
	require.NoError(t, os.WriteFile(namePath, []byte("\x00\x01not gob at all"), 0o600))

	f2, err := ensureFinders(cfg)
	require.NoError(t, err, "an undecodable name index must trigger a rebuild, not a fatal error")

	c := f2.NameFinder.CityByName("les Escaldes", "AD")
	require.NotNil(t, c, "the rebuilt name finder must contain the source data")
	assert.Equal(t, "les Escaldes", c.Name)
}

// TestEnsureFinders_CorruptIndexWithoutDatasetsIsFatal documents the limit of
// the fallback: when the raw datasets are gone and an index is corrupt, there
// is nothing to rebuild from and the error must surface.
func TestEnsureFinders_CorruptIndexWithoutDatasetsIsFatal(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f1, err := ensureFinders(cfg)
	require.NoError(t, err)
	require.NotNil(t, f1)

	require.NoError(t, os.Remove(filepath.Join(dir, cfg.AllCitiesFile)))
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.PostalCodesFile)))

	s2Path := filepath.Join(dir, cfg.S2.IndexFile)
	require.NoError(t, os.WriteFile(s2Path, []byte("garbage"), 0o600))

	_, err = ensureFinders(cfg)
	require.Error(t, err, "a corrupt index with no source data to rebuild from must fail")
	assert.Contains(t, err.Error(), "rebuild", "the error must explain the rebuild attempt failed")
}

// TestEnsureFinders_NameIndexUnreadableFileIsFatal pins the tightened name
// sentinel contract: a non-corrupt decode failure — here a wrapped fs error
// from an index file with no read permission — must NOT be silently rebuilt
// over. Rebuilding is reserved for name.ErrCorruptIndex; everything else is
// fatal, exactly as for the S2 and postal code indexes.
func TestEnsureFinders_NameIndexUnreadableFileIsFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file permissions only")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores file mode bits; the EACCES path cannot be exercised")
	}

	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f1, err := ensureFinders(cfg)
	require.NoError(t, err)
	require.NotNil(t, f1)

	namePath := filepath.Join(dir, cfg.NameIndexFile)
	require.NoError(t, os.Chmod(namePath, 0o000))
	defer func() { _ = os.Chmod(namePath, 0o600) }()

	_, err = ensureFinders(cfg)
	require.Error(t, err, "an unreadable name index must be fatal, not rebuilt over")
	assert.Contains(t, err.Error(), "failed to deserialize name index",
		"the error must identify deserialization as the failing stage")
	assert.NotErrorIs(t, err, name.ErrCorruptIndex,
		"fs errors must never be classified as corruption")
}
