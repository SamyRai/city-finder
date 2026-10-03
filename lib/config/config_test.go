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
  "s2": {"index_file": "s2index.gob"}
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

// TestLoadConfigEnvVarHonored covers the deprecated CONFIG_FILE fallback:
// with an empty configPath, LoadConfig reads the env var and resolves it
// under the same rules. (CONFIG_PATH is resolved by LoadRuntime or
// LoadFromEnv, which feed it into LoadConfig as configPath.)
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
	assert.Empty(t, cfg.S2.IndexFile)
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

// TestIndexFilePaths pins the index path resolution shared by index producers
// (cmd/build-index) and consumers (lib/initializer): each
// index file key joined with DatasetsFolder, using the folder value the
// Config carries verbatim. LoadConfig always absolutizes a relative
// datasets_folder against the config file's directory, so configs that
// arrived via LoadConfig join onto an absolute folder; hand-constructed
// configs (as in cmd/server and initializer tests) join onto whatever they
// carry — here a relative folder, passed through by plain filepath.Join.
func TestIndexFilePaths(t *testing.T) {
	// Config loaded from a file: absolute datasets_folder preserved verbatim.
	absData := t.TempDir()
	path := writeConfig(t, t.TempDir(), "config.json", `{
  "datasets_folder": `+strconv.Quote(absData)+`,
  "name_index_file": "name_custom.gob",
  "postal_code_index_file": "postal_custom.gob",
  "s2": {"index_file": "s2_custom.gob"}
}`)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	s2Path, namePath, postalPath := cfg.IndexFilePaths()
	assert.Equal(t, filepath.Join(absData, "s2_custom.gob"), s2Path)
	assert.Equal(t, filepath.Join(absData, "name_custom.gob"), namePath)
	assert.Equal(t, filepath.Join(absData, "postal_custom.gob"), postalPath)

	// Hand-constructed config with a relative folder: plain Join, no
	// absolutization of its own.
	cfg = &Config{
		DatasetsFolder:      "rel_datasets",
		NameIndexFile:       "name_index.gob",
		PostalCodeIndexFile: "postal_code_index.gob",
		S2:                  S2{IndexFile: "s2index.gob"},
	}
	s2Path, namePath, postalPath = cfg.IndexFilePaths()
	assert.Equal(t, filepath.Join("rel_datasets", "s2index.gob"), s2Path)
	assert.Equal(t, filepath.Join("rel_datasets", "name_index.gob"), namePath)
	assert.Equal(t, filepath.Join("rel_datasets", "postal_code_index.gob"), postalPath)
}

// TestLoadConfigIgnoresRemovedS2Keys documents the loader's behavior for a
// config file in the wild that still carries the s2 tuning knobs removed in
// v1.2 (min_level / max_level / max_cells): encoding/json silently ignores
// unknown keys — LoadConfig does not call DisallowUnknownFields — so such a
// file loads cleanly and the surviving s2.index_file key still decodes.
func TestLoadConfigIgnoresRemovedS2Keys(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", `{
  "datasets_folder": "datasets",
  "s2": {"min_level": 10, "max_level": 15, "max_cells": 8, "index_file": "s2index.gob"}
}`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err,
		"removed s2 keys remaining in a config file must be silently ignored, not an error")
	require.NotNil(t, cfg)
	assert.Equal(t, "s2index.gob", cfg.S2.IndexFile,
		"the surviving s2.index_file key must still decode")
}

// TestLoadConfigExcludeAdminDivisions pins the exclude_admin_divisions knob's
// JSON decoding: the key is snake_case like every other key, an absent key
// decodes false (the historical load — existing config files are unaffected),
// and explicit values decode verbatim.
func TestLoadConfigExcludeAdminDivisions(t *testing.T) {
	t.Run("absent key decodes false", func(t *testing.T) {
		// validConfigJSON carries no exclude_admin_divisions key.
		path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.False(t, cfg.ExcludeAdminDivisions,
			"a config file written before the knob existed must decode as off")
	})

	t.Run("explicit false decodes false", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "config.json",
			`{"datasets_folder": "datasets", "exclude_admin_divisions": false}`)

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.False(t, cfg.ExcludeAdminDivisions)
	})

	t.Run("explicit true decodes true", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "config.json",
			`{"datasets_folder": "datasets", "exclude_admin_divisions": true}`)

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.True(t, cfg.ExcludeAdminDivisions)
	})
}

// TestLoadConfigIncludeFeatureClasses pins the include_feature_classes knob's
// JSON semantics: a comma-separated string that UnmarshalJSON trims,
// uppercases, dedupes, and validates against the GeoNames feature-class set;
// absent or empty decodes nil (the historical load — existing config files are
// unaffected), and an invalid entry fails the config LOAD with the offending
// value in the message.
func TestLoadConfigIncludeFeatureClasses(t *testing.T) {
	t.Run("absent key decodes nil", func(t *testing.T) {
		// validConfigJSON carries no include_feature_classes key.
		path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.Empty(t, cfg.IncludeFeatureClasses,
			"a config file written before the knob existed must decode as no filter")
		assert.Nil(t, cfg.IncludeFeatureClasses, "no filter is nil, not an empty slice")
	})

	t.Run("empty string decodes nil (explicit default-off)", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "config.json",
			`{"datasets_folder": "datasets", "include_feature_classes": ""}`)

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.Nil(t, cfg.IncludeFeatureClasses)
	})

	t.Run("single class", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "config.json",
			`{"datasets_folder": "datasets", "include_feature_classes": "P"}`)

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.Equal(t, []string{"P"}, cfg.IncludeFeatureClasses)
	})

	t.Run("multiple classes comma-separated", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "config.json",
			`{"datasets_folder": "datasets", "include_feature_classes": "P,A"}`)

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.Equal(t, []string{"P", "A"}, cfg.IncludeFeatureClasses)
	})

	t.Run("entries are trimmed, uppercased, and deduped", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "config.json",
			`{"datasets_folder": "datasets", "include_feature_classes": " p , A , p "}`)

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.Equal(t, []string{"P", "A"}, cfg.IncludeFeatureClasses)
	})

	t.Run("invalid entry fails the config load naming the value", func(t *testing.T) {
		for name, raw := range map[string]string{
			"unknown letter":      `{"include_feature_classes": "P,X"}`,
			"lowercase multi":     `{"include_feature_classes": "pp"}`,
			"lowercase x":         `{"include_feature_classes": "p,x"}`,
			"empty entry in list": `{"include_feature_classes": "P,,"}`,
		} {
			t.Run(name, func(t *testing.T) {
				path := writeConfig(t, t.TempDir(), "config.json", raw)

				cfg, err := LoadConfig(path)
				require.Error(t, err,
					"an invalid class must be a config LOAD error, never silently ignored")
				assert.Nil(t, cfg)
				assert.Contains(t, err.Error(), "failed to decode config file",
					"the failure surfaces through the normal decode path of LoadConfig")
				assert.Contains(t, err.Error(), "invalid include_feature_classes",
					"the error must name the offending key")
			})
		}

		// The offending VALUE lands in the message: "X" is blamed, "P" is not.
		path := writeConfig(t, t.TempDir(), "config.json", `{"include_feature_classes": "P,X"}`)
		_, err := LoadConfig(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"X"`)
		assert.Contains(t, err.Error(), "A P H L R S T U V",
			"the error must state the valid class set")
	})
}

// TestLoadFromEnvHonorsConfigPath pins the binary config resolution: with
// CONFIG_PATH set, LoadFromEnv loads exactly that file — the same resolution
// LoadRuntime performs for cmd/server.
func TestLoadFromEnvHonorsConfigPath(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "custom.json", validConfigJSON)
	t.Setenv("CONFIG_PATH", path)

	cfg, err := LoadFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, filepath.Join(dir, "datasets"), cfg.DatasetsFolder)
}

// TestLoadFromEnvDefaultsToConfigJSON pins the unset case: without CONFIG_PATH
// the loader falls back to "config.json" relative to the process working
// directory, exactly like cmd/server/main.go.
func TestLoadFromEnvDefaultsToConfigJSON(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "config.json", validConfigJSON)
	chdirWithoutGoModAbove(t, dir)

	// CONFIG_PATH must be genuinely unset here: a set-but-empty value would
	// take the LookupEnv "exists" branch and fall through to LoadConfig's
	// CONFIG_FILE handling instead of the "config.json" default.
	if old, had := os.LookupEnv("CONFIG_PATH"); had {
		require.NoError(t, os.Unsetenv("CONFIG_PATH"))
		t.Cleanup(func() { _ = os.Setenv("CONFIG_PATH", old) })
	}

	cfg, err := LoadFromEnv()
	require.NoError(t, err, "unset CONFIG_PATH must default to ./config.json")
	require.NotNil(t, cfg)
	assert.Equal(t, filepath.Join(dir, "datasets"), cfg.DatasetsFolder)
}

// TestLoadConfigRejectsTrailingContent: a file holding more than one JSON
// document fails to load instead of silently using the first.
func TestLoadConfigRejectsTrailingContent(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON+`{"datasets_folder": "other"}`)
	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after the JSON object")

	path = writeConfig(t, t.TempDir(), "config.json", validConfigJSON+"\n\n")
	_, err = LoadConfig(path)
	require.NoError(t, err, "trailing whitespace is fine")
}

// TestUnknownKeysAreReported: misspelled or removed keys are listed (top
// level and under s2) so LoadConfig can warn; known keys never are.
func TestUnknownKeysAreReported(t *testing.T) {
	assert.Empty(t, unknownKeys([]byte(validConfigJSON)))
	assert.Equal(t, []string{"name_idx_file", "s2.max_cells"},
		unknownKeys([]byte(`{"name_idx_file": "x", "s2": {"index_file": "s", "max_cells": 8}}`)))
	assert.Empty(t, unknownKeys([]byte(`[1, 2]`)), "a non-object is Decode's error to report")
}

// TestValidate: the shipped shape passes; missing required names and two
// keys naming the same file are reported together.
func TestValidate(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())

	shipped, err := LoadConfig(filepath.Join("..", "..", "config.json"))
	require.NoError(t, err)
	require.NoError(t, shipped.Validate(), "the shipped config.json must validate")

	bad := *cfg
	bad.NameIndexFile = ""
	bad.PostalCodesZip = bad.AllCitiesZip
	err = bad.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name_index_file is required")
	assert.Contains(t, err.Error(), `all_cities_zip and postal_codes_zip both name "allCountries.zip"`)

	optional := *cfg
	optional.AllCitiesZip, optional.PostalCodesZip, optional.Admin1CodesFile = "", "", ""
	assert.NoError(t, optional.Validate(), "zips and the admin1 file are optional")
}
