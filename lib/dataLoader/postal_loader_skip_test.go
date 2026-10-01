package dataLoader

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postalRow joins fields into one TSV row in GeoNames zip format:
// 0:country 1:postal 2:place 3:admin1 4:code1 5:admin2 6:code2 7:admin3
// 8:code3 9:latitude 10:longitude 11:accuracy. Building rows from explicit
// field lists keeps the tested indices (9 and 10) auditable.
func postalRow(fields ...string) string {
	return strings.Join(fields, "\t") + "\n"
}

// writePostalFixture writes content to a temp file and returns its path.
func writePostalFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zipCodes_fixture.txt")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// captureLoaderLogs redirects the standard logger for one test and returns the
// captured buffer plus a restore func.
func captureLoaderLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	return &buf, func() {
		log.SetOutput(old)
	}
}

// malformedPostalCodes returns postal rows with >=12 fields whose record[9]
// (latitude) or record[10] (longitude) cannot be parsed as a float. These are
// the rows the skip-and-log policy must refuse to index.
func malformedPostalCodes() []string {
	return []string{
		postalRow("XX", "10001", "Bad Lat", "", "", "", "", "", "", "not-a-number", "-74.0060", "1"),
		postalRow("XX", "10002", "Empty Lat", "", "", "", "", "", "", "", "-74.0060", "1"),
		postalRow("XX", "10003", "Bad Lon", "", "", "", "", "", "", "40.7128", "not-a-number", "1"),
		postalRow("XX", "10004", "Empty Lon", "", "", "", "", "", "", "40.7128", "", "1"),
	}
}

func validUSPostalRow() string {
	return postalRow("US", "10005", "Valid City", "", "", "", "", "", "", "40.7128", "-74.0060", "1")
}

// TestLoadPostalCodes_SkipsRowsWithUnparsableCoordinates pins the skip-and-log
// policy: a row with >=12 fields whose latitude or longitude fails
// strconv.ParseFloat must NOT be indexed. Before the fix such rows were
// silently indexed at (0,0) (or at lat=0 / the lon value when only lat was
// bad), and /postalCode lookups returned Null-Island coordinates.
func TestLoadPostalCodes_SkipsRowsWithUnparsableCoordinates(t *testing.T) {
	var content strings.Builder
	for _, row := range malformedPostalCodes() {
		content.WriteString(row)
	}
	content.WriteString(validUSPostalRow())

	postalCodes, err := LoadPostalCodes(writePostalFixture(t, content.String()))
	require.NoError(t, err)

	for _, code := range []string{"10001", "10002", "10003", "10004"} {
		entry, exists := postalCodes["XX"][code]
		assert.False(t, exists,
			"row %s has unparsable coordinates and must not be indexed (found entry at lat=%v lon=%v)",
			code, entry.Latitude, entry.Longitude)
	}

	// The mixed file still indexes every valid row, with its real coordinates.
	entry, exists := postalCodes["US"]["10005"]
	require.True(t, exists, "valid row must still be indexed alongside malformed ones")
	assert.InDelta(t, 40.7128, entry.Latitude, 0.0001)
	assert.InDelta(t, -74.0060, entry.Longitude, 0.0001)
}

// TestLoadPostalCodes_SkipSummaryIsLoggedOnce verifies the summary logging:
// exactly one line at end of load, counting only rows skipped for unparsable
// coordinates. No per-row logging is allowed at prod scale (~14M rows).
func TestLoadPostalCodes_SkipSummaryIsLoggedOnce(t *testing.T) {
	content := malformedPostalCodes()[0] + malformedPostalCodes()[2] + // 2 unparsable rows
		validUSPostalRow()

	buf, restore := captureLoaderLogs(t)
	defer restore()

	postalCodes, err := LoadPostalCodes(writePostalFixture(t, content))
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if buf.Len() == 0 {
		lines = nil
	}
	require.Len(t, lines, 1, "expected exactly one summary log line, got: %q", buf.String())
	assert.Contains(t, lines[0], "skipped 2 postal rows with unparsable coordinates")

	// The valid row still loaded.
	assert.True(t, func() bool { _, ok := postalCodes["US"]["10005"]; return ok }())
}

// TestLoadPostalCodes_NoSkipLogWhenAllRowsParse keeps clean loads silent, and
// keeps short rows (<12 fields, rejected by the pre-existing length check
// before coordinate parsing) out of the unparsable-coordinate count.
func TestLoadPostalCodes_NoSkipLogWhenAllRowsParse(t *testing.T) {
	t.Run("all rows valid", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		postalCodes, err := LoadPostalCodes(writePostalFixture(t, validUSPostalRow()))
		require.NoError(t, err)
		assert.NotEmpty(t, postalCodes["US"])
		assert.NotContains(t, buf.String(), "skipped")
	})

	t.Run("short rows only", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		// A standalone stream of short rows: csv.Reader is field-count
		// consistent, records are rejected by len(record) < 12 and must not
		// be reported as coordinate skips.
		_, err := LoadPostalCodes(writePostalFixture(t, "XX\t10006\tToo few fields\n"))
		require.NoError(t, err)
		assert.NotContains(t, buf.String(), "skipped")
	})
}
