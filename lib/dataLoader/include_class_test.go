package dataLoader

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// classAllowlistRows extends the admin-filter fixture with the two rows the
// validation findings were about: the class-L "HHS Region 9" row whose
// synthetic 49.34M population wins western-US rank=population queries, and a
// class-U undersea row (the country-less nearest features of far-from-land
// queries — given a country code here so row counts stay purely class-driven;
// class-based dropping is independent of the mandatory-field check, pinned in
// TestLoadGeoNames_IncludeListCheckPrecedesFieldValidation).
var classAllowlistRows = append([]string{
	featureClassRow("5555555", "HHS Region 9", "37.75", "-122.5", "L", "LCTY", "US", "", "49340000"),
	featureClassRow("6015966", "Mendocino Escarpment", "39.5", "-127.5", "U", "DEPU", "US", "", "0"),
}, fixtureRows...)

// TestLoadGeoNames_IncludeFeatureClassesDefaultIsByteIdentical pins the
// default (knob off) as the historical behavior: a nil or empty
// IncludeFeatureClasses — and, equivalently, an allowlist carrying every
// GeoNames class — returns exactly what LoadGeoNamesCSV returns from the same
// file. No allowlist skip summary may be logged.
func TestLoadGeoNames_IncludeFeatureClassesDefaultIsByteIdentical(t *testing.T) {
	path := writeFixture(t, classAllowlistRows...)

	buf, restore := captureLoaderLogs(t)
	defer restore()

	legacy, err := LoadGeoNamesCSV(path)
	require.NoError(t, err)
	require.Len(t, legacy, 6, "the fixture's six valid rows all load by default")

	nilOpts, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{IncludeFeatureClasses: nil})
	require.NoError(t, err)
	assert.Exactly(t, legacy, nilOpts, "nil IncludeFeatureClasses must be byte-identical to the legacy path")

	emptyOpts, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{IncludeFeatureClasses: []string{}})
	require.NoError(t, err)
	assert.Exactly(t, legacy, emptyOpts, "an empty (zero-length) IncludeFeatureClasses must disable the filter, not drop every row")

	allCities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{IncludeFeatureClasses: GeoNamesFeatureClasses})
	require.NoError(t, err)
	assert.Exactly(t, legacy, allCities, "allowlisting every GeoNames class must be byte-identical to no filter at all")

	assert.NotContains(t, buf.String(), "outside feature class allowlist",
		"no allowlist skip summary may be logged when the knob is off")
}

// TestLoadGeoNames_IncludeFeatureClassesOnlyP pins the populated-places-only
// mode — the mode both validation findings called for: exactly the two P-class
// rows survive (the A, L, and U rows drop), order is kept, and the skip count
// is summarized in ONE log line naming the allowlist, postal-loader style.
func TestLoadGeoNames_IncludeFeatureClassesOnlyP(t *testing.T) {
	path := writeFixture(t, classAllowlistRows...)

	buf, restore := captureLoaderLogs(t)
	defer restore()

	cities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{IncludeFeatureClasses: []string{"P"}})
	require.NoError(t, err)

	require.Len(t, cities, 2, "only the two P-class rows may load")
	assert.Equal(t, []string{"Springfield", "Roc Meler"}, loadedNames(cities))

	logs := buf.String()
	require.Equal(t, 1, strings.Count(logs, "outside feature class allowlist"),
		"the allowlist skip summary must be logged exactly once, got: %q", logs)
	assert.Contains(t, logs, "skipped 4 rows outside feature class allowlist [P]")
	assert.Contains(t, logs, "Loaded 2 cities", "the end-of-load summary reflects the post-filter count")
}

// TestLoadGeoNames_IncludeFeatureClassesPAndA pins the multi-class allowlist:
// "P,A" keeps the P rows and the A rows in order and drops everything else
// (the L "HHS Region 9" and U undersea rows).
func TestLoadGeoNames_IncludeFeatureClassesPAndA(t *testing.T) {
	path := writeFixture(t, classAllowlistRows...)

	buf, restore := captureLoaderLogs(t)
	defer restore()

	cities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{IncludeFeatureClasses: []string{"P", "A"}})
	require.NoError(t, err)

	require.Len(t, cities, 4, "the P and A rows survive; L and U drop")
	assert.Equal(t, []string{"Springfield", "Roc Meler", "California", "United States"}, loadedNames(cities))
	assert.Contains(t, buf.String(), "skipped 2 rows outside feature class allowlist [P,A]")
}

// TestLoadGeoNames_IncludeFeatureClassesInvalidEntriesError pins the fail-loud
// contract: every entry must be exactly one of the GeoNames feature classes —
// lowercase, multi-letter, and empty entries are rejected — and validation
// happens BEFORE the file is opened, so a bad option errors even for a
// nonexistent path instead of surfacing an I/O error first.
func TestLoadGeoNames_IncludeFeatureClassesInvalidEntriesError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "no_such_dump.txt")

	for name, classes := range map[string][]string{
		"unknown letter":     {"X"},
		"lowercase":          {"p"},
		"multi-letter":       {"PP"},
		"empty entry":        {""},
		"valid then invalid": {"P", "X", "q"},
	} {
		t.Run(name, func(t *testing.T) {
			cities, err := LoadGeoNamesCSVWithOptions(missingPath, LoadOptions{IncludeFeatureClasses: classes})
			require.Error(t, err, "invalid entries must fail the load, never be silently ignored")
			assert.Nil(t, cities)
			assert.Contains(t, err.Error(), "invalid IncludeFeatureClasses entries",
				"the error must name the offending option")
			assert.Contains(t, err.Error(), "A P H L R S T U V",
				"the error must state the valid class set")
			assert.NotContains(t, err.Error(), "failed to open",
				"validation must precede the file open, so the config error — not an I/O error — wins")
		})
	}

	t.Run("error names every invalid entry", func(t *testing.T) {
		_, err := LoadGeoNamesCSVWithOptions(missingPath, LoadOptions{IncludeFeatureClasses: []string{"P", "X", "q"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"X"`)
		assert.Contains(t, err.Error(), `"q"`)
		assert.NotContains(t, err.Error(), `"P"`, "valid entries are not blamed")
	})
}

// TestLoadGeoNames_IncludeListAppliedBeforeExcludeAdminDivisions pins the
// documented interaction: the include-list runs first, then
// ExcludeAdminDivisions drops class-A survivors. When the include-list already
// excludes A the flag is a no-op — no admin-skip counter, no summary line.
func TestLoadGeoNames_IncludeListAppliedBeforeExcludeAdminDivisions(t *testing.T) {
	path := writeFixture(t, classAllowlistRows...)

	t.Run("include-list without A makes the exclude flag a no-op", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		cities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{
			IncludeFeatureClasses: []string{"P"},
			ExcludeAdminDivisions: true,
		})
		require.NoError(t, err)
		require.Len(t, cities, 2)
		assert.Equal(t, []string{"Springfield", "Roc Meler"}, loadedNames(cities))

		logs := buf.String()
		assert.Contains(t, logs, "skipped 4 rows outside feature class allowlist [P]",
			"the A rows die at the include step and count towards its summary")
		assert.NotContains(t, logs, "admin division rows",
			"no row reaches the exclude check, so the exclude flag is a no-op: no counter, no summary")
	})

	t.Run("include-list with A lets the exclude flag still drop A", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		cities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{
			IncludeFeatureClasses: []string{"P", "A"},
			ExcludeAdminDivisions: true,
		})
		require.NoError(t, err)
		require.Len(t, cities, 2, "A rows survive the include-list but fall to the exclude flag")
		assert.Equal(t, []string{"Springfield", "Roc Meler"}, loadedNames(cities))

		logs := buf.String()
		assert.Contains(t, logs, "skipped 2 rows outside feature class allowlist [P,A]",
			"L and U die at the include step")
		assert.Contains(t, logs, "skipped 2 admin division rows (feature class A)",
			"the exclude flag still applies to class-A survivors")
	})
}

// TestLoadGeoNames_IncludeListCheckPrecedesFieldValidation pins the ordering
// against the pre-existing malformed-row policy: the class check runs before
// the mandatory-field and coordinate checks, so an out-of-class row with a
// broken latitude is counted as an allowlist skip — never reaching (or
// logging) the lat parse error. With the knob off the same row follows the
// historical path: parse error, row dropped.
func TestLoadGeoNames_IncludeListCheckPrecedesFieldValidation(t *testing.T) {
	rows := []string{
		featureClassRow("5555555", "HHS Region 9", "not-a-number", "-122.5", "L", "LCTY", "US", "", "49340000"),
		featureClassRow("3062241", "Springfield", "42.58765", "1.7418", "P", "PPL", "AD", "02", "16316"),
	}
	path := writeFixture(t, rows...)

	t.Run("filtered as out-of-class, no lat error logged", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		cities, err := LoadGeoNamesCSVWithOptions(path, LoadOptions{IncludeFeatureClasses: []string{"P"}})
		require.NoError(t, err)
		require.Len(t, cities, 1)
		assert.Equal(t, "Springfield", cities[0].City.Name)
		assert.Contains(t, buf.String(), "skipped 1 rows outside feature class allowlist [P]")
		assert.NotContains(t, buf.String(), "Error parsing lat",
			"an out-of-class row must not reach coordinate parsing and must not log a per-row error")
	})

	t.Run("default: historical parse-error path", func(t *testing.T) {
		buf, restore := captureLoaderLogs(t)
		defer restore()

		cities, err := LoadGeoNamesCSV(path)
		require.NoError(t, err)
		require.Len(t, cities, 1)
		assert.Equal(t, "Springfield", cities[0].City.Name)
		assert.Contains(t, buf.String(), "Error parsing lat")
		assert.NotContains(t, buf.String(), "outside feature class allowlist")
	})
}
