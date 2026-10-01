package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

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

// chdirWithoutGoModAbove chdir's into dir and verifies the precondition the
// project-root-independence tests rely on: no go.mod exists in dir or any of
// its ancestors. Under v1.0 semantics LoadConfig hard-failed in that
// situation ("failed to find project root"), so the tests prove the v1.1
// guarantee: loading never discovers a project root at all. Standard test
// temp directories (TMPDIR and friends) satisfy the precondition; a host
// with a stray go.mod above the temp dir cannot exercise it, so the test
// skips rather than passes vacuously.
func chdirWithoutGoModAbove(t *testing.T, dir string) {
	t.Helper()
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			t.Skipf("cannot verify project-root independence: %s has a go.mod ancestor %s", dir, d)
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	t.Chdir(dir)
}

// TestLoadConfigHappyPath loads a fully populated config from an absolute
// path and checks every field decodes and that a relative datasets_folder is
// resolved against the config file's directory (v1.1 semantics; v1.0 joined
// it onto a discovered project root).
func TestLoadConfigHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config.json", validConfigJSON)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, filepath.Join(dir, "datasets"), cfg.DatasetsFolder)
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
// absolute configPath must be opened as-is. Joining it onto some base
// directory would turn /tmp/x/config.json into <base>/tmp/x/config.json and
// fail with a confusing open error.
func TestLoadConfigAbsolutePathNotMangled(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)
	require.True(t, filepath.IsAbs(path), "temp paths must be absolute for this test to mean anything")

	cfg, err := LoadConfig(path)
	require.NoError(t, err, "an absolute config path must be opened as-is, not joined onto another base")
	require.NotNil(t, cfg)
}

// TestLoadConfigAbsolutePathNeedsNoProjectRoot is the core v1.1 regression
// test: with the process working directory outside any Go module (no go.mod
// up to the filesystem root), an absolute CONFIG_PATH must still load. v1.0
// called util.FindProjectRoot unconditionally and failed here, which is why
// the container image shipped a fake /app/go.mod marker.
func TestLoadConfigAbsolutePathNeedsNoProjectRoot(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config.json", validConfigJSON)
	chdirWithoutGoModAbove(t, t.TempDir())

	cfg, err := LoadConfig(path)
	require.NoError(t, err, "an absolute config path must load without any project-root discovery")
	require.NotNil(t, cfg)
	assert.Equal(t, filepath.Join(dir, "datasets"), cfg.DatasetsFolder)
}

// TestLoadConfigRelativePathResolvedAgainstCWD pins the v1.1 relative-path
// rule: a relative configPath resolves against the process working directory.
// The negative case is differential: the repository root contains a
// config.json, so v1.0's project-root resolution would have loaded it from an
// unrelated CWD — v1.1 must fail to open instead, proving the resolution base
// changed from project root to CWD.
func TestLoadConfigRelativePathResolvedAgainstCWD(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "config.json", validConfigJSON)
	chdirWithoutGoModAbove(t, dir)

	cfg, err := LoadConfig("config.json")
	require.NoError(t, err, "a relative config path must resolve against the CWD")
	require.NotNil(t, cfg)
	assert.Equal(t, filepath.Join(dir, "datasets"), cfg.DatasetsFolder)

	// From a different CWD the same relative path must not resolve at all
	// (v1.0 would have found the repo-root config.json).
	chdirWithoutGoModAbove(t, t.TempDir())
	_, err = LoadConfig("config.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open config file")
}

// TestLoadConfigEnvVarHonored covers the CONFIG_FILE fallback: with an empty
// configPath, LoadConfig reads the env var and resolves it under the same
// rules. (The CONFIG_PATH env var belongs to cmd/server's main(), which feeds
// it into LoadConfig as configPath.)
func TestLoadConfigEnvVarHonored(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config.json", validConfigJSON)
	t.Setenv("CONFIG_FILE", path)

	cfg, err := LoadConfig("")
	require.NoError(t, err, "CONFIG_FILE must be honored when configPath is empty")
	require.NotNil(t, cfg)
	assert.Equal(t, filepath.Join(dir, "datasets"), cfg.DatasetsFolder)
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
// absolute and a relative (CWD-based) path.
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
// to the config file's directory itself.
func TestLoadConfigNoDefaultsDocuments(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config.json", `{}`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.AllCitiesURL)
	assert.Empty(t, cfg.AllCitiesFile)
	assert.Empty(t, cfg.NameIndexFile)
	assert.Zero(t, cfg.S2.MinLevel)
	assert.Zero(t, cfg.S2.MaxCells)
	assert.Equal(t, dir, cfg.DatasetsFolder,
		`datasets_folder "" joins to the config file's directory`)
}

// TestLoadConfigDatasetsFolderPaths pins the datasets_folder joining rules:
// relative values resolve against the config file's directory, absolute
// values are kept verbatim. The relative case also pins that the resolution
// ignores the CWD: the test runs from an unrelated directory.
func TestLoadConfigDatasetsFolderPaths(t *testing.T) {
	dir := t.TempDir()

	rel := writeConfig(t, dir, "config.json", `{"datasets_folder": "my_datasets"}`)
	chdirWithoutGoModAbove(t, t.TempDir())
	cfg, err := LoadConfig(rel)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "my_datasets"), cfg.DatasetsFolder)

	absData := t.TempDir()
	abs := writeConfig(t, t.TempDir(), "config.json",
		`{"datasets_folder": `+strconv.Quote(absData)+`}`)
	cfg, err = LoadConfig(abs)
	require.NoError(t, err)
	assert.Equal(t, absData, cfg.DatasetsFolder,
		"an absolute datasets_folder must be preserved, not prefixed with another base")
}

// TestLoadConfigRepoRootLayoutPreserved documents that the default
// development layout keeps its v1.0 meaning: the repo-root config.json sits
// in the same directory as the "datasets" folder it names, so resolving
// datasets_folder against the config file's directory (v1.1) yields the same
// repo-root/datasets path the old project-root join produced.
func TestLoadConfigRepoRootLayoutPreserved(t *testing.T) {
	cfgDir := t.TempDir()
	writeConfig(t, cfgDir, "config.json", validConfigJSON)
	chdirWithoutGoModAbove(t, cfgDir)

	cfg, err := LoadConfig("config.json")
	require.NoError(t, err, "the config must load relative to the CWD where it lives")
	assert.Equal(t, filepath.Join(cfgDir, "datasets"), cfg.DatasetsFolder)
}
