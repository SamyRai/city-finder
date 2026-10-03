package initializer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexFilesIn lists the serialized indexes (and leftovers) in dir.
func indexFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	for _, pattern := range []string{"*.gob", "*.part"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		require.NoError(t, err)
		found = append(found, matches...)
	}
	return found
}

// TestEnsureFinders_EmptyCityFileFails: a city dataset with no rows (a
// truncated download, a wrong file, an HTML error page saved as the dump)
// used to build, serialize and then serve empty indexes forever: every later
// boot found all indexes present. It must fail the boot and write nothing.
func TestEnsureFinders_EmptyCityFileFails(t *testing.T) {
	for name, content := range map[string]string{
		"empty file":     "",
		"html error":     "<html><body>503 Service Unavailable</body></html>\n",
		"only bad lines": "not\ta\tcity\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := testConfig(dir)
			writeTinyDatasets(t, cfg)
			require.NoError(t, os.WriteFile(filepath.Join(dir, cfg.AllCitiesFile), []byte(content), 0o600))

			_, err := ensureFinders(context.Background(), fastDownloader(), cfg, "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no cities")
			assert.Empty(t, indexFilesIn(t, dir), "no index may be serialized from an empty city load")
		})
	}
}

// TestEnsureFinders_FilterThatMatchesNothingFails: include_feature_classes
// that excludes every row is the same empty load, and the error says why.
func TestEnsureFinders_FilterThatMatchesNothingFails(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)
	cfg.IncludeFeatureClasses = []string{"H"} // the tiny dataset holds only T and P rows

	_, err := ensureFinders(context.Background(), fastDownloader(), cfg, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "include_feature_classes")
	assert.Empty(t, indexFilesIn(t, dir))
}

// TestDatasetSource_EmptyCityLoadIsNotRetried: re-ensuring the datasets
// cannot fix a file that exists and parses to nothing, so the failure is
// reported as it is rather than after a pointless re-download attempt.
func TestDatasetSource_EmptyCityLoadIsNotRetried(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)
	require.NoError(t, os.WriteFile(filepath.Join(dir, cfg.AllCitiesFile), nil, 0o600))
	cfg.AllCitiesURL = "http://127.0.0.1:0/must-not-be-fetched"
	cfg.AllCitiesZip = "allCountries.zip"

	data := &datasetSource{cfg: cfg, dl: fastDownloader()}
	err := data.load(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "additionally")
	assert.False(t, data.loaded)
}

// TestEnsureFinders_EmptyPostalFileIsAllowed: the postal dataset is
// separate and optional in effect; zero postal rows still boots (lookups
// then find nothing), unlike the city table every query path depends on.
func TestEnsureFinders_EmptyPostalFileIsAllowed(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)
	require.NoError(t, os.WriteFile(filepath.Join(dir, cfg.PostalCodesFile), nil, 0o600))

	f, err := ensureFinders(context.Background(), fastDownloader(), cfg, "")
	require.NoError(t, err)
	assert.Equal(t, 0, f.PostalCodeFinder.Len())
}
