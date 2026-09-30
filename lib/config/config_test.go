package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/SamyRai/cityFinder/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeConfig writes a config JSON file and returns its absolute path.
func writeConfig(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

// validConfigJSON is a fully populated config used by the happy-path tests.
const validConfigJSON = `{
  "datasets_folder": "datasets",
  "all_cities_url": "https://example.org/allCountries.zip",
  "postal_codes_url": "https://example.org/zip/allCountries.zip",
  "all_cities_file": "allCountries_dump.txt",
  "postal_codes_file": "allCountries_zip.txt",
  "all_cities_zip": "allCountries.zip",
  "postal_codes_zip": "zipCodes.zip",
  "name_index_file": "name_index.gob",
  "postal_code_index_file": "postal_code_index.gob",
  "s2": {"min_level": 10, "max_level": 15, "max_cells": 8, "index_file": "s2index.gob"}
}`

// projectRoot returns the repository root the same way LoadConfig resolves it
// (FindProjectRoot walks up from the working directory to go.mod).
func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := util.FindProjectRoot()
	require.NoError(t, err)
	return root
}

// TestLoadConfigHappyPath loads a fully populated config from an absolute
// path and checks every field decodes and that a relative datasets_folder is
// resolved against the project root.
func TestLoadConfigHappyPath(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, filepath.Join(projectRoot(t), "datasets"), cfg.DatasetsFolder)
	assert.Equal(t, "https://example.org/allCountries.zip", cfg.AllCitiesURL)
	assert.Equal(t, "https://example.org/zip/allCountries.zip", cfg.PostalCodesURL)
	assert.Equal(t, "allCountries_dump.txt", cfg.AllCitiesFile)
	assert.Equal(t, "allCountries_zip.txt", cfg.PostalCodesFile)
	assert.Equal(t, "allCountries.zip", cfg.AllCitiesZip)
	assert.Equal(t, "zipCodes.zip", cfg.PostalCodesZip)
	assert.Equal(t, "name_index.gob", cfg.NameIndexFile)
	assert.Equal(t, "postal_code_index.gob", cfg.PostalCodeIndexFile)
	assert.Equal(t, 10, cfg.S2.MinLevel)
	assert.Equal(t, 15, cfg.S2.MaxLevel)
	assert.Equal(t, 8, cfg.S2.MaxCells)
	assert.Equal(t, "s2index.gob", cfg.S2.IndexFile)
}

// TestLoadConfigAbsolutePathNotMangled pins the absolute-path contract: an
// absolute configPath must be opened as-is. Joining it onto the project root
// (the previous behavior) would turn /tmp/x/config.json into
// <root>/tmp/x/config.json and fail with a confusing open error.
func TestLoadConfigAbsolutePathNotMangled(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)
	require.True(t, filepath.IsAbs(path), "temp paths must be absolute for this test to mean anything")

	cfg, err := LoadConfig(path)
	require.NoError(t, err, "an absolute config path must be opened as-is, not joined onto the project root")
	require.NotNil(t, cfg)
}

// TestLoadConfigRelativePathResolvedAgainstRoot pins the relative-path
// contract: a relative configPath resolves against the FindProjectRoot
// output, not against the process working directory. The path is derived
// root-relative the same way the cmd/server process tests do.
func TestLoadConfigRelativePathResolvedAgainstRoot(t *testing.T) {
	root := projectRoot(t)
	abs := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)
	rel, err := filepath.Rel(root, abs)
	require.NoError(t, err)

	cfg, err := LoadConfig(rel)
	require.NoError(t, err, "a root-relative config path must resolve against the project root")
	require.NotNil(t, cfg)
}

// TestLoadConfigEnvVarHonored covers the CONFIG_FILE fallback: with an empty
// configPath, LoadConfig reads the env var. (The CONFIG_PATH env var belongs
// to cmd/server's main(), which feeds it into LoadConfig as configPath.)
func TestLoadConfigEnvVarHonored(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)
	t.Setenv("CONFIG_FILE", path)

	cfg, err := LoadConfig("")
	require.NoError(t, err, "CONFIG_FILE must be honored when configPath is empty")
	require.NotNil(t, cfg)
	assert.Equal(t, filepath.Join(projectRoot(t), "datasets"), cfg.DatasetsFolder)
}

// TestLoadConfigNoPathNoEnvErrors: with neither a configPath nor CONFIG_FILE
// set, LoadConfig must fail instead of inventing a configuration.
func TestLoadConfigNoPathNoEnvErrors(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	_, err := LoadConfig("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no config file provided")
}

// TestLoadConfigMissingFileErrors covers the missing-file path for both an
// absolute and a root-relative path.
func TestLoadConfigMissingFileErrors(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "no_such_config.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open config file")

	_, err = LoadConfig(filepath.Join("no", "such", "relative", "config.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open config file")
}

// TestLoadConfigInvalidJSONErrors covers the decode-failure path.
func TestLoadConfigInvalidJSONErrors(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", `{"datasets_folder": `)
	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode config file")
}

// TestLoadConfigNoDefaultsDocuments documents that LoadConfig applies no
// defaults of its own: an empty JSON object yields the zero-value Config, and
// the only normalization is the datasets_folder path joining, where "" maps
// to the project root itself.
func TestLoadConfigNoDefaultsDocuments(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", `{}`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.AllCitiesURL)
	assert.Empty(t, cfg.AllCitiesFile)
	assert.Empty(t, cfg.NameIndexFile)
	assert.Zero(t, cfg.S2.MinLevel)
	assert.Zero(t, cfg.S2.MaxCells)
	assert.Equal(t, projectRoot(t), cfg.DatasetsFolder,
		`datasets_folder "" joins to the project root`)
}

// TestLoadConfigDatasetsFolderPaths pins the datasets_folder joining rules:
// relative values resolve against the project root, absolute values are kept
// verbatim (previously both were joined, which mangled absolute paths).
func TestLoadConfigDatasetsFolderPaths(t *testing.T) {
	root := projectRoot(t)

	rel := writeConfig(t, t.TempDir(), "config.json", `{"datasets_folder": "my_datasets"}`)
	cfg, err := LoadConfig(rel)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "my_datasets"), cfg.DatasetsFolder)

	absData := t.TempDir()
	abs := writeConfig(t, t.TempDir(), "config.json",
		`{"datasets_folder": `+strconv.Quote(absData)+`}`)
	cfg, err = LoadConfig(abs)
	require.NoError(t, err)
	assert.Equal(t, absData, cfg.DatasetsFolder,
		"an absolute datasets_folder must be preserved, not prefixed with the project root")
}
