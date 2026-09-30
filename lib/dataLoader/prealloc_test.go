package dataLoader

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDataRelPath points at the repository test dataset from this package's
// test working directory (lib/dataLoader).
const testDataRelPath = "../../testdata/allCountries.txt"

// BenchmarkLoadGeoNamesCSV measures allocation behavior of the unlimited
// load on the test dataset. It exists to guard the preallocation strategy:
// the slice capacity must be derived from the input file size, not from a
// hardcoded row count.
func BenchmarkLoadGeoNamesCSV(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		cities, err := LoadGeoNamesCSV(testDataRelPath)
		if err != nil {
			b.Fatalf("LoadGeoNamesCSV failed: %v", err)
		}
		if len(cities) == 0 {
			b.Fatal("expected at least one city from the test dataset")
		}
	}
}

func TestEstimatedCityCount(t *testing.T) {
	dir := t.TempDir()

	// 1440 bytes / 120 bytes-per-line + 1 = 13
	small := filepath.Join(dir, "small.txt")
	require.NoError(t, os.WriteFile(small, bytes.Repeat([]byte("x"), 1440), 0o600))
	assert.Equal(t, 13, estimatedCityCount(small, 0))

	// An explicit limit always takes precedence over the size estimate.
	assert.Equal(t, 5, estimatedCityCount(small, 5))

	// Degenerate inputs yield "no estimate" (0), never a huge allocation.
	empty := filepath.Join(dir, "empty.txt")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	assert.Equal(t, 0, estimatedCityCount(empty, 0))
	assert.Equal(t, 0, estimatedCityCount(filepath.Join(dir, "missing.txt"), 0))
}

func TestLoadGeoNamesCSVWithLimit_TestData(t *testing.T) {
	cities, err := LoadGeoNamesCSV(testDataRelPath)
	require.NoError(t, err)
	assert.NotEmpty(t, cities)
	for _, c := range cities {
		assert.NotEmpty(t, c.Name)
		assert.NotEmpty(t, c.Country)
		assert.False(t, c.Rect == nil)
	}

	limited, err := LoadGeoNamesCSVWithLimit(testDataRelPath, 1)
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}
