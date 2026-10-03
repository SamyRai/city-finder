package main

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/initializer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// transcriptFor builds a finder over a generated dataset and dumps its
// transcript, returning the bytes.
func transcriptFor(t *testing.T, n int, seed int64) []byte {
	t.Helper()
	cfgPath := genConfig(t, n, seed)
	cfg, err := config.LoadConfig(cfgPath)
	require.NoError(t, err)
	f, err := initializer.Initialize(cfg)
	require.NoError(t, err)
	// No fuzzy wait here: dumpTranscript owns that precondition.
	return dumpTo(t, f, cfg, filepath.Join(t.TempDir(), "answers.txt"))
}

func dumpTo(t *testing.T, f *finder.Finder, cfg *config.Config, path string) []byte {
	t.Helper()
	require.NoError(t, dumpTranscript(context.Background(), f, cfg, path, time.Minute))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestDumpTranscript_SameDatasetIsByteIdentical(t *testing.T) {
	a := transcriptFor(t, 300, 1)
	b := transcriptFor(t, 300, 1)
	require.NotEmpty(t, a)
	assert.True(t, utf8.Valid(a))
	for _, kind := range []string{"nearest ", "name ", "prefix ", "postal "} {
		assert.Contains(t, string(a), "\n"+kind, kind)
	}
	assert.Equal(t, string(a), string(b))
}

func TestDumpTranscript_DifferentDatasetDiffers(t *testing.T) {
	assert.NotEqual(t, string(transcriptFor(t, 300, 1)), string(transcriptFor(t, 300, 2)))
}

func TestDumpTranscript_UnwritablePathFails(t *testing.T) {
	cfg, err := config.LoadConfig(genConfig(t, 50, 1))
	require.NoError(t, err)
	f, err := initializer.Initialize(cfg)
	require.NoError(t, err)
	assert.Error(t, dumpTranscript(context.Background(), f, cfg, filepath.Join(t.TempDir(), "no-dir", "x"), time.Minute))
}

func TestSampleRowsAndPostal(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, genDataset(dir, 200, 1))
	cities := filepath.Join(dir, "allCountries_dump.txt")
	zips := filepath.Join(dir, "allCountries_zip.txt")

	a, err := sampleRows(cities, 20, rand.New(rand.NewSource(5)))
	require.NoError(t, err)
	b, err := sampleRows(cities, 20, rand.New(rand.NewSource(5)))
	require.NoError(t, err)
	require.Len(t, a, 20)
	assert.Equal(t, a, b, "same seed, same sample")

	c, err := sampleRows(cities, 20, rand.New(rand.NewSource(6)))
	require.NoError(t, err)
	assert.NotEqual(t, a, c, "different seed, different sample")

	all, err := sampleRows(cities, 10_000, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.Len(t, all, 200, "k above the row count returns every row")

	pa, err := samplePostal(zips, 15, rand.New(rand.NewSource(5)))
	require.NoError(t, err)
	pb, err := samplePostal(zips, 15, rand.New(rand.NewSource(5)))
	require.NoError(t, err)
	require.Len(t, pa, 15)
	assert.Equal(t, pa, pb)

	_, err = sampleRows(filepath.Join(dir, "missing"), 1, rand.New(rand.NewSource(1)))
	assert.Error(t, err)
	_, err = samplePostal(filepath.Join(dir, "missing"), 1, rand.New(rand.NewSource(1)))
	assert.Error(t, err)

	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))
	rows, err := sampleRows(empty, 5, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestSampleRows_SkipsShortAndCountrylessRows(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dump")
	good := "1\tName\tName\tAlt1,Alt2\t1.5\t2.5\tP\tPPL\tDE\n"
	short := "1\tName\tName\n"
	noCountry := "1\tName\tName\t\t1\t2\tP\tPPL\t\n"
	require.NoError(t, os.WriteFile(p, []byte(short+noCountry+good), 0o644))
	rows, err := sampleRows(p, 5, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, datasetRow{name: "Name", country: "DE", alts: []string{"Alt1", "Alt2"}, lat: 1.5, lon: 2.5}, rows[0])
}

func TestTypo(t *testing.T) {
	for _, s := range []string{"Berlin", "Köln", "São Paulo", "ab", "東京都"} {
		for seed := int64(0); seed < 200; seed++ {
			got := typo(s, rand.New(rand.NewSource(seed)))
			assert.True(t, utf8.ValidString(got), "typo(%q) seed %d = %q", s, seed, got)
			dl := utf8.RuneCountInString(got) - utf8.RuneCountInString(s)
			assert.Contains(t, []int{-1, 0, 1}, dl, "typo(%q) = %q", s, got)
		}
	}
	// Strings of fewer than two runes are extended, never panic.
	assert.Equal(t, "a", typo("", rand.New(rand.NewSource(1))))
	assert.Equal(t, "Xa", typo("X", rand.New(rand.NewSource(1))))
}

// TestDumpTranscript_WaitsForTheFuzzyBuild pins that the dump itself waits: a
// freshly initialised finder is handed over with the fuzzy index unbuilt, and
// afterwards the index must be built.
func TestDumpTranscript_WaitsForTheFuzzyBuild(t *testing.T) {
	cfg, err := config.LoadConfig(genConfig(t, 300, 1))
	require.NoError(t, err)
	f, err := initializer.Initialize(cfg)
	require.NoError(t, err)
	require.NotEqual(t, int32(2), f.FuzzyBuildState(), "precondition: not built yet")
	dumpTo(t, f, cfg, filepath.Join(t.TempDir(), "answers.txt"))
	assert.EqualValues(t, 2, f.FuzzyBuildState())
}
