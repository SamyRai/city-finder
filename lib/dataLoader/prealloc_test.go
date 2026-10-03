package dataLoader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDataRelPath points at the repository test dataset from this package's
// test working directory (lib/dataLoader).
const testDataRelPath = "../../testdata/allCountries.txt"

// BenchmarkLoadGeoNamesCSV measures allocation behavior of the unlimited
// load on the test dataset. It exists to guard the preallocation strategy:
// the slice capacity must come from the input's line count, not from a
// hardcoded row count.
func BenchmarkLoadGeoNamesCSV(b *testing.B) {
	silenceLoaderLogs(b)
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
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		return p
	}

	// The line count, whatever the line length; an unterminated last line
	// counts too.
	long := write("long.txt", strings.Repeat(strings.Repeat("x", 5000)+"\n", 13))
	assert.Equal(t, 13, estimatedCityCount(long, 0))
	assert.Equal(t, 3, estimatedCityCount(write("unterminated.txt", "a\nb\nc"), 0))
	assert.Equal(t, 2_000_001, estimatedCityCount(write("big.txt", strings.Repeat("\n", 2_000_001)), 0), "counts across read buffers")

	// An explicit limit always takes precedence over the count.
	assert.Equal(t, 5, estimatedCityCount(long, 5))

	// Degenerate inputs yield "no estimate" (0), never a huge allocation.
	assert.Equal(t, 0, estimatedCityCount(write("empty.txt", ""), 0))
	assert.Equal(t, 0, estimatedCityCount(filepath.Join(dir, "missing.txt"), 0))
}

func TestLoadGeoNamesCSVWithLimit_TestData(t *testing.T) {
	cities, err := LoadGeoNamesCSV(testDataRelPath)
	require.NoError(t, err)
	assert.NotEmpty(t, cities)
	for _, c := range cities {
		assert.NotEmpty(t, c.Name)
		assert.NotEmpty(t, c.Country)
	}

	limited, err := LoadGeoNamesCSVWithLimit(testDataRelPath, 1)
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}
