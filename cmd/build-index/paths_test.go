package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolvePathsProdHonorsConfigPath pins the prod-mode contract: with
// CONFIG_PATH pointing at a config whose index file keys differ from the
// defaults, the resolved prod output paths equal each index file key joined
// with that config's datasets_folder. That join is exactly what
// (*config.Config).IndexFilePaths performs and what the initializer's
// IndexFilePaths call performs on the reader side, so build-index writes its
// outputs precisely where the initializer will look for them — for any
// non-default config, not just the shipped one.
func TestResolvePathsProdHonorsConfigPath(t *testing.T) {
	dataDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "custom_config.json")
	cfgJSON := `{
  "datasets_folder": ` + strconv.Quote(dataDir) + `,
  "all_cities_file": "cities_custom.txt",
  "postal_codes_file": "postal_custom.txt",
  "name_index_file": "name_custom.gob",
  "postal_code_index_file": "postal_custom.gob",
  "s2": {"index_file": "s2_custom.gob"}
}`
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgJSON), 0o600))
	t.Setenv("CONFIG_PATH", cfgPath)

	paths := resolvePaths("prod")

	assert.Equal(t, filepath.Join(dataDir, "cities_custom.txt"), paths.dataFile)
	assert.Equal(t, filepath.Join(dataDir, "postal_custom.txt"), paths.postalCodeFile)
	assert.Equal(t, dataDir, paths.outputDir)
	assert.Equal(t, filepath.Join(dataDir, "s2_custom.gob"), paths.s2IndexPath,
		"S2 index output must come from s2.index_file joined with datasets_folder")
	assert.Equal(t, filepath.Join(dataDir, "name_custom.gob"), paths.nameIndexPath,
		"name index output must come from name_index_file joined with datasets_folder")
	assert.Equal(t, filepath.Join(dataDir, "postal_custom.gob"), paths.postalIndexPath,
		"postal index output must come from postal_code_index_file joined with datasets_folder")
}

// TestResolvePathsProdFallsBackToLiteralsWhenConfigMissing pins the fallback:
// when the config file cannot be loaded, prod mode must keep running on the
// legacy literal dataset and index names rather than failing — build-index
// stays usable before a config exists.
func TestResolvePathsProdFallsBackToLiteralsWhenConfigMissing(t *testing.T) {
	t.Setenv("CONFIG_PATH", filepath.Join(t.TempDir(), "definitely_missing.json"))

	paths := resolvePaths("prod")

	assert.Equal(t, "datasets/allCountries.txt", paths.dataFile)
	assert.Equal(t, "datasets/zipCodes.txt", paths.postalCodeFile)
	assert.Equal(t, "datasets", paths.outputDir)
	assert.Equal(t, "datasets/s2index.gob", paths.s2IndexPath)
	assert.Equal(t, "datasets/name_index.gob", paths.nameIndexPath)
	assert.Equal(t, "datasets/postal_code_index.gob", paths.postalIndexPath)
}

// TestResolvePathsTestModeLiterals guards the test-mode layout, including the
// historical *_test.gob filename suffix, against regressions from the prod
// path-resolution logic living in the same function.
func TestResolvePathsTestModeLiterals(t *testing.T) {
	paths := resolvePaths("test")

	assert.Equal(t, "testdata/allCountries.txt", paths.dataFile)
	assert.Equal(t, "testdata/zipCodes.txt", paths.postalCodeFile)
	assert.Equal(t, "testdata", paths.outputDir)
	assert.Equal(t, filepath.Join("testdata", "s2index_test.gob"), paths.s2IndexPath)
	assert.Equal(t, filepath.Join("testdata", "name_index_test.gob"), paths.nameIndexPath)
	assert.Equal(t, filepath.Join("testdata", "postal_code_index_test.gob"), paths.postalIndexPath)
}
