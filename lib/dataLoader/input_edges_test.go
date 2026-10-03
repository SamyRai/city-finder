package dataLoader

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const utf8BOM = "\xef\xbb\xbf"

// overlongLine is a single line past maxLineBytes: a corrupt or binary file
// must cost one row, not the whole load.
func overlongLine() string {
	return strings.Repeat("x", maxLineBytes+10)
}

func TestLoadPostalCodes_StripsBOM(t *testing.T) {
	content := utf8BOM + postalRow("DE", "01067", "Dresden", "Sachsen", "SN", "", "", "", "", "51.05", "13.74", "4") +
		postalRow("DE", "01069", "Dresden", "Sachsen", "SN", "", "", "", "", "51.04", "13.73", "4")
	codes, err := LoadPostalCodes(writePostalFixture(t, content))
	require.NoError(t, err)
	assert.NotContains(t, codes, utf8BOM+"DE")
	assert.Contains(t, codes["DE"], "01067", "first row must be reachable under the real country key")
	assert.Equal(t, "DE", codes["DE"]["01067"].CountryCode)
}

func TestLoadAdmin1Names_StripsBOM(t *testing.T) {
	names, err := LoadAdmin1Names(writeAdmin1NamesFile(t,
		utf8BOM+"US.CA\tCalifornia\tCalifornia\t5332921\nUS.NV\tNevada\tNevada\t5509842\n"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"US.CA": "California", "US.NV": "Nevada"}, names)
}

func TestLoadGeoNames_StripsBOM(t *testing.T) {
	path := writeFixture(t, utf8BOM+featureClassRow("1", "First", "42.5", "1.7", "P", "PPL", "AD", "02", "10"))
	cities, err := LoadGeoNamesCSV(path)
	require.NoError(t, err)
	require.Len(t, cities, 1)
	assert.Equal(t, "First", cities[0].City.Name)
}

// TestLoaders_CRLFLineEndings pins that a Windows-style file loads exactly
// like an LF one: no trailing \r in the last field of any loader's rows.
func TestLoaders_CRLFLineEndings(t *testing.T) {
	t.Run("postal", func(t *testing.T) {
		content := strings.ReplaceAll(
			postalRow("US", "10005", "Valid City", "", "", "", "", "", "", "40.7128", "-74.0060", "6")+
				postalRow("US", "10006", "Other", "", "", "", "", "", "", "40.7", "-74.0", "4"), "\n", "\r\n")
		codes, err := LoadPostalCodes(writePostalFixture(t, content))
		require.NoError(t, err)
		assert.Equal(t, 6, codes["US"]["10005"].Accuracy)
		assert.Equal(t, 4, codes["US"]["10006"].Accuracy)
	})
	t.Run("admin1", func(t *testing.T) {
		names, err := LoadAdmin1Names(writeAdmin1NamesFile(t, "US.CA\tCalifornia\r\nUS.NV\tNevada\r\n"))
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"US.CA": "California", "US.NV": "Nevada"}, names)
	})
	t.Run("city", func(t *testing.T) {
		row := featureClassRow("1", "Crlf", "42.5", "1.7", "P", "PPL", "AD", "02", "10")
		path := writeFixture(t, row+"\r")
		cities, err := LoadGeoNamesCSV(path)
		require.NoError(t, err)
		require.Len(t, cities, 1)
		assert.Equal(t, "Crlf", cities[0].City.Name)
	})
}

// TestLoaders_OverlongLineSkipsRowNotLoad pins that a line past the cap is
// skipped and counted (real GeoNames lines are under 10 KB; the cap is 1 MiB)
// while every other row loads.
func TestLoaders_OverlongLineSkipsRowNotLoad(t *testing.T) {
	t.Run("city", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()
		path := writeFixture(t,
			featureClassRow("1", "Before", "42.5", "1.7", "P", "PPL", "AD", "02", "10"),
			overlongLine(),
			featureClassRow("2", "After", "42.5", "1.7", "P", "PPL", "AD", "02", "10"))
		cities, err := LoadGeoNamesCSV(path)
		require.NoError(t, err)
		assert.Equal(t, []string{"Before", "After"}, loadedNames(cities))
		assert.Contains(t, buf.String(), "1 line longer than 1 MiB (line 2)")
	})
	t.Run("postal", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()
		content := validUSPostalRow() + overlongLine() + "\n" +
			postalRow("US", "10008", "After", "", "", "", "", "", "", "51.5", "-0.1", "1")
		codes, err := LoadPostalCodes(writePostalFixture(t, content))
		require.NoError(t, err)
		assert.Len(t, codes["US"], 2)
		assert.Contains(t, buf.String(), "1 line longer than 1 MiB (line 2)")
	})
	t.Run("admin1", func(t *testing.T) {
		names, err := LoadAdmin1Names(writeAdmin1NamesFile(t,
			"US.CA\tCalifornia\n"+overlongLine()+"\nUS.NV\tNevada\n"))
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"US.CA": "California", "US.NV": "Nevada"}, names)
	})
	t.Run("last line without newline", func(t *testing.T) {
		content := validUSPostalRow() + overlongLine()
		codes, err := LoadPostalCodes(writePostalFixture(t, content))
		require.NoError(t, err)
		assert.Len(t, codes["US"], 1)
	})
}

// TestForEachLine pins the line reader against buffer-boundary cases: a line
// longer than the 64 KiB read buffer but under the cap is delivered intact,
// one at exactly the cap is delivered, one byte over is skipped, a final
// line may lack its newline, and fn can stop the read.
func TestForEachLine(t *testing.T) {
	collect := func(input string, stopAfter int) ([]string, *skipReport) {
		var got []string
		var skipped skipReport
		require.NoError(t, forEachLine(strings.NewReader(input), &skipped, func(n int, line []byte) bool {
			got = append(got, string(line))
			return stopAfter == 0 || len(got) < stopAfter
		}))
		return got, &skipped
	}

	big := strings.Repeat("a", 200*1024)
	got, sk := collect("x\n"+big+"\ny", 0)
	assert.Equal(t, []string{"x", big, "y"}, got)
	assert.Zero(t, sk.total)

	atCap := strings.Repeat("b", maxLineBytes-1) // plus "\n" == maxLineBytes
	got, sk = collect(atCap+"\nz\n", 0)
	assert.Equal(t, []string{atCap, "z"}, got)
	assert.Zero(t, sk.total)

	got, sk = collect(atCap+"b\nz\n", 0)
	assert.Equal(t, []string{"z"}, got)
	assert.Equal(t, 1, sk.counts[reasonLineTooLong])

	got, _ = collect("a\r\nb\r\n\r\nc", 0)
	assert.Equal(t, []string{"a", "b", "", "c"}, got)

	got, _ = collect("a\nb\nc\n", 2)
	assert.Equal(t, []string{"a", "b"}, got)

	got, _ = collect("", 0)
	assert.Empty(t, got)
}

// TestLoadPostalCodes_DuplicateKeyLastRowWins documents current behaviour:
// GeoNames has legitimate duplicate (country, postal code) rows and the
// later row replaces the earlier one. Changing this changes /postalCode
// answers, so it is pinned, not decided.
func TestLoadPostalCodes_DuplicateKeyLastRowWins(t *testing.T) {
	content := postalRow("US", "10005", "First", "", "", "", "", "", "", "40.0", "-74.0", "1") +
		postalRow("US", "10005", "Second", "", "", "", "", "", "", "41.0", "-75.0", "2")
	codes, err := LoadPostalCodes(writePostalFixture(t, content))
	require.NoError(t, err)
	require.Len(t, codes["US"], 1)
	assert.Equal(t, "Second", codes["US"]["10005"].PlaceName)
	assert.InDelta(t, 41.0, codes["US"]["10005"].Latitude, 1e-9)
}
