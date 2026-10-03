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
	assert.Contains(t, lines[0], "2 unparsable coordinates (lines 1, 2)")

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

		// A standalone stream of short rows: records are rejected by
		// len(record) < 12, counted in their own summary line, and must not
		// be reported as coordinate skips.
		_, err := LoadPostalCodes(writePostalFixture(t, "XX\t10006\tToo few fields\n"))
		require.NoError(t, err)
		assert.NotContains(t, buf.String(), "unparsable coordinates")
		assert.Contains(t, buf.String(), "1 fewer than 12 fields (line 1)")
	})
}

// TestLoadPostalCodes_ToleratesMalformedCSVStructure pins the loader's
// tolerance policy for field-count damage: a row with the wrong number of
// fields is skipped and counted, never aborts the entire load. Before
// FieldsPerRecord=-1, one such row returned a csv.ParseError that failed the
// whole load — and with it server startup — because of one bad line in a
// ~14M-row hand-curated file.
func TestLoadPostalCodes_ToleratesMalformedCSVStructure(t *testing.T) {
	// A short row must be skipped and counted, never abort the load (the
	// old code failed here with ErrFieldCount once the first row had locked
	// FieldsPerRecord to 12).
	content := validUSPostalRow() +
		"US\t10006\tShort Row\n" +
		postalRow("US", "10008", "Valid After Bad", "", "", "", "", "", "", "51.5074", "-0.1278", "1")
	path := writePostalFixture(t, content)

	buf, restore := captureLoaderLogs(t)
	defer restore()

	codes, err := LoadPostalCodes(path)
	require.NoError(t, err, "a short row must not abort the load")

	require.Contains(t, codes, "US")
	assert.Equal(t, "Valid City", codes["US"]["10005"].PlaceName)
	assert.Equal(t, "Valid After Bad", codes["US"]["10008"].PlaceName)
	assert.NotContains(t, codes["US"], "10006", "a short row must be skipped")
	assert.Contains(t, buf.String(), "1 fewer than 12 fields (line 2)")
}

// TestLoadPostalCodes_QuotesAreOrdinaryCharacters pins that GeoNames postal
// files are plain TSV: a double quote — bare mid-field (5" Rd) or leading —
// is just text. Under encoding/csv the bare quote aborted the whole load
// ("bare \" in non-quoted-field") and the leading one was ambiguous; neither
// may now lose a row or any row after it.
func TestLoadPostalCodes_QuotesAreOrdinaryCharacters(t *testing.T) {
	content := validUSPostalRow() +
		postalRow("US", "10007", `5" Rd`, `Adm "One"`, "", "", "", "", "", "40.7128", "-74.0060", "1") +
		postalRow("US", "10009", `"Quoted Start`, "", "", "", "", "", "", "40.7128", "-74.0060", "1") +
		postalRow("US", "10008", "Survivor", "", "", "", "", "", "", "51.5074", "-0.1278", "1")

	codes, err := LoadPostalCodes(writePostalFixture(t, content))
	require.NoError(t, err)

	assert.Len(t, codes["US"], 4)
	assert.Equal(t, `5" Rd`, codes["US"]["10007"].PlaceName)
	assert.Equal(t, `Adm "One"`, codes["US"]["10007"].AdminName1)
	assert.Equal(t, `"Quoted Start`, codes["US"]["10009"].PlaceName)
	assert.Equal(t, "Survivor", codes["US"]["10008"].PlaceName)
}

// TestLoadPostalCodes_ColumnSemantics pins every column of a loaded row, so
// the TSV split cannot drift from the 12-column GeoNames layout.
func TestLoadPostalCodes_ColumnSemantics(t *testing.T) {
	row := postalRow("GB", "SW1A 1AA", "London", "England", "ENG", "Greater London", "GLA", "Westminster", "E09000033", "51.5014", "-0.1419", "6")
	codes, err := LoadPostalCodes(writePostalFixture(t, row))
	require.NoError(t, err)
	assert.Equal(t, PostalCodeEntry{
		CountryCode: "GB", PostalCode: "SW1A 1AA", PlaceName: "London",
		AdminName1: "England", AdminCode1: "ENG", AdminName2: "Greater London", AdminCode2: "GLA",
		AdminName3: "Westminster", AdminCode3: "E09000033",
		Latitude: 51.5014, Longitude: -0.1419, Accuracy: 6,
	}, codes["GB"]["SW1A 1AA"])
}

// TestLoadPostalCodes_ExtraFieldsAndBlankLines pins that a row with more than
// 12 fields still loads from its first 12, and blank lines are ignored
// without being counted as malformed.
func TestLoadPostalCodes_ExtraFieldsAndBlankLines(t *testing.T) {
	buf, restore := captureLoaderLogs(t)
	defer restore()

	content := "\n" +
		postalRow("US", "10005", "Valid City", "", "", "", "", "", "", "40.7128", "-74.0060", "1", "extra", "more") +
		"\n"
	codes, err := LoadPostalCodes(writePostalFixture(t, content))
	require.NoError(t, err)
	assert.Equal(t, "Valid City", codes["US"]["10005"].PlaceName)
	assert.NotContains(t, buf.String(), "skipped")
}
