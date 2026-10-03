package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

const (
	tinyCities = "2994701\tRoc Meler\tRoc Meler\tRoc Mele,Roc Meler\t42.58765\t1.7418\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03\n" +
		"3040051\tles Escaldes\tles Escaldes\tEscaldes\t42.50729\t1.53414\tPPLA\tAD\tAD\t\t07\t\t\t\t16316\t\t\t1032\tEurope/Andorra\t2023-10-03\n"
	tinyPostal = "AD\tAD100\tCanillo\tCanillo\t02\t\t\t\t\t42.5833\t1.6667\t6\n" +
		"AD\tAD200\tEncamp\tEncamp\t03\t\t\t\t\t42.5347\t1.5801\t6\n"
)

func TestMain(m *testing.M) {
	gcSettle = 0 // the builds under test need no settled baseline
	os.Exit(m.Run())
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// assertIndexesLoad decodes the three files the way the server does and
// checks the name index attaches to the S2 city table.
func assertIndexesLoad(t *testing.T, s2Path, namePath, postalPath string) {
	t.Helper()
	s2, err := coordinates.DeserializeIndex(s2Path)
	require.NoError(t, err)
	assert.Len(t, s2.Cities, 2)
	nameFinder, err := name.DeserializeIndex(namePath)
	require.NoError(t, err)
	require.NoError(t, nameFinder.ShareCities(s2.Cities))
	_, err = postalCode.DeserializeIndex(postalPath)
	require.NoError(t, err)
}

// prodConfig writes a config whose datasets live in dir and points
// CONFIG_PATH at it.
func prodConfig(t *testing.T, dir string, extra string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, cfgPath, `{
  "datasets_folder": `+strconv.Quote(dir)+`,
  "all_cities_file": "cities.txt",
  "postal_codes_file": "postal.txt",
  "name_index_file": "name.gob",
  "postal_code_index_file": "postal.gob",
  "s2": {"index_file": "s2.gob"}`+extra+`
}`)
	t.Setenv("CONFIG_PATH", cfgPath)
}

func TestRun_TestMode(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFile(t, "testdata/allCountries.txt", tinyCities)
	writeFile(t, "testdata/zipCodes.txt", tinyPostal)

	var stdout, stderr bytes.Buffer
	require.NoError(t, run(nil, &stdout, &stderr), stderr.String())

	out := stdout.String()
	assert.Contains(t, out, "1. Loading Cities Data")
	assert.Contains(t, out, "6. Serializing Indexes")
	assert.Contains(t, out, "SERIALIZING INDEXES TO TESTDATA")
	assert.Contains(t, out, "Total Cities:        2")
	assertIndexesLoad(t,
		filepath.Join("testdata", "s2index_test.gob"),
		filepath.Join("testdata", "name_index_test.gob"),
		filepath.Join("testdata", "postal_code_index_test.gob"))
}

func TestRun_ProdModeUsesConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cities.txt"), tinyCities)
	writeFile(t, filepath.Join(dir, "postal.txt"), tinyPostal)
	prodConfig(t, dir, "")

	var stdout, stderr bytes.Buffer
	require.NoError(t, run([]string{"prod"}, &stdout, &stderr), stderr.String())

	assert.Contains(t, stdout.String(), "Mode:         prod")
	assertIndexesLoad(t, filepath.Join(dir, "s2.gob"), filepath.Join(dir, "name.gob"), filepath.Join(dir, "postal.gob"))
}

func TestRun_ProdModeAppliesConfigFilters(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cities.txt"), tinyCities)
	writeFile(t, filepath.Join(dir, "postal.txt"), tinyPostal)
	// Only the first row has feature class T.
	prodConfig(t, dir, `, "include_feature_classes": "T"`)

	var stdout, stderr bytes.Buffer
	require.NoError(t, run([]string{"prod"}, &stdout, &stderr), stderr.String())

	s2, err := coordinates.DeserializeIndex(filepath.Join(dir, "s2.gob"))
	require.NoError(t, err)
	assert.Len(t, s2.Cities, 1)
}

func TestRun_MissingPostalDatasetIsTolerated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cities.txt"), tinyCities)
	prodConfig(t, dir, "")

	var stdout, stderr bytes.Buffer
	require.NoError(t, run([]string{"prod"}, &stdout, &stderr))

	assert.Contains(t, stderr.String(), "Warning: failed to load postal codes")
	assert.FileExists(t, filepath.Join(dir, "postal.gob"))
}

func TestRun_MissingCitiesFails(t *testing.T) {
	dir := t.TempDir()
	prodConfig(t, dir, "")

	var stdout, stderr bytes.Buffer
	err := run([]string{"prod"}, &stdout, &stderr)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load cities")
	assert.NoFileExists(t, filepath.Join(dir, "s2.gob"))
}

func TestRun_WriteFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cities.txt"), tinyCities)
	writeFile(t, filepath.Join(dir, "postal.txt"), tinyPostal)
	// The name index directory does not exist.
	t.Setenv("CONFIG_PATH", func() string {
		p := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, p, `{"datasets_folder": `+strconv.Quote(dir)+`, "all_cities_file": "cities.txt",
  "postal_codes_file": "postal.txt", "name_index_file": "missing/name.gob",
  "postal_code_index_file": "postal.gob", "s2": {"index_file": "s2.gob"}}`)
		return p
	}())

	var stdout, stderr bytes.Buffer
	err := run([]string{"prod"}, &stdout, &stderr)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to serialize name index")
}

func TestRun_UsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown mode":  {"staging"},
		"extra args":    {"test", "prod"},
		"unknown flag":  {"-bogus"},
		"flag then arg": {"-x", "prod"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(args, &stdout, &stderr)
			require.Error(t, err)
			assert.Contains(t, stderr.String(), "Usage:")
			assert.Empty(t, stdout.String(), "nothing is built on a usage error")
		})
	}
}

func TestRun_HelpPrintsUsageAndSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.NoError(t, run([]string{"-h"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "Usage:")
	assert.Empty(t, stdout.String())
}

func TestParseMode(t *testing.T) {
	mode, err := parseMode(nil)
	require.NoError(t, err)
	assert.Equal(t, "test", mode)

	mode, err = parseMode([]string{"prod"})
	require.NoError(t, err)
	assert.Equal(t, "prod", mode)

	_, err = parseMode([]string{"nope"})
	assert.ErrorContains(t, err, `unknown mode "nope"`)
}
