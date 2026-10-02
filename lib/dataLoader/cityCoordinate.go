package dataLoader

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/SamyRai/cityFinder/lib/city"
)

// findField efficiently extracts a specific field from a tab-separated line
// This avoids splitting the entire line which reduces memory allocations
func findField(line string, fieldIndex int, separator byte) string {
	if fieldIndex < 0 {
		return ""
	}

	fieldCount := 0
	start := 0

	for i, char := range []byte(line) {
		if char == separator {
			if fieldCount == fieldIndex {
				return line[start:i]
			}
			fieldCount++
			start = i + 1
		}
	}

	// Handle the last field (no trailing separator)
	if fieldCount == fieldIndex {
		return line[start:]
	}

	return ""
}

func LoadGeoNamesCSV(filepath string) ([]city.SpatialCity, error) {
	return LoadGeoNamesCSVWithLimit(filepath, 0)
}

// averageGeoNamesLineBytes is a deliberately conservative (low) average line
// length for the GeoNames allCountries dump; real lines average ~120-140
// bytes. Assuming fewer bytes per line overestimates the row count, so the
// preallocated slice may be slightly too large but never needs to regrow
// (regrowing at ~12M rows of 80 bytes would briefly double ~1GB of memory).
const averageGeoNamesLineBytes = 120

// estimatedCityCount returns the slice capacity to preallocate for filepath.
// An explicit limit always wins; otherwise the capacity is derived from the
// file size so that a small or partial file no longer allocates a fixed
// 15,000,000-row (~1.2GB) backing array. A return of 0 means "no estimate";
// the append loop then grows the slice naturally.
func estimatedCityCount(filepath string, limit int) int {
	if limit > 0 {
		return limit
	}
	if fi, err := os.Stat(filepath); err == nil && fi.Size() > 0 {
		return int(fi.Size()/averageGeoNamesLineBytes) + 1
	}
	return 0
}

// GeoNamesFeatureClasses lists every GeoNames feature class — the value of
// field index 6 (0-based, tab-separated) in the allCountries dump. The nine
// single uppercase letters cover the whole dump: A administrative divisions
// (countries, states, districts), P populated places, H water bodies
// (streams, lakes, seas), L parks/areas/vegetation, R roads/railroads,
// S spots/buildings/farms, T mountains/hills/rocks, U undersea features,
// V forests. IncludeFeatureClasses entries (loader) and
// include_feature_classes values (config) are validated against this set.
var GeoNamesFeatureClasses = []string{"A", "P", "H", "L", "R", "S", "T", "U", "V"}

// IsValidFeatureClass reports whether class is exactly one of the GeoNames
// feature classes — a member of GeoNamesFeatureClasses, i.e. a single
// uppercase letter from A P H L R S T U V. Anything else (lowercase,
// multi-letter, empty) is rejected.
func IsValidFeatureClass(class string) bool {
	for _, valid := range GeoNamesFeatureClasses {
		if class == valid {
			return true
		}
	}
	return false
}

// LoadOptions parameterizes a GeoNames city load. The zero value is the
// historical behavior: no row limit, every feature class loaded.
type LoadOptions struct {
	// Limit stops the load once Limit cities have been appended (0 = no limit).
	Limit int

	// IncludeFeatureClasses, when non-empty, is an allowlist of GeoNames
	// feature classes (field index 6, 0-based, tab-separated): a row is loaded
	// ONLY if its class is in the set. A populated-places-only mode (["P"])
	// keeps cities and drops everything else — the validation findings that
	// motivated the knob: a class-L "HHS Region 9" row with a synthetic 49.34M
	// population otherwise wins every rank=population query in the western US,
	// and the nearest raw features to far-from-land queries are country-less
	// undersea/international rows.
	//
	// Every entry is validated at load start and must be exactly one of the
	// GeoNames feature classes A P H L R S T U V (single uppercase letter —
	// lowercase "p" is invalid here; normalize in the config layer, which does
	// trim+uppercase). Anything else is an error returned from the loader
	// before the file is opened: fail loud, never silently ignore a typo that
	// would otherwise mean "no rows loaded". An empty (nil or zero-length)
	// slice disables the filter and keeps the historical load byte-identical.
	//
	// INTERACTION with ExcludeAdminDivisions: the include-list is applied
	// first; ExcludeAdminDivisions then still drops class-A rows that survived
	// it. When the include-list already excludes A (A not in the set), the
	// exclude flag is a no-op — its class-A skip counter stays 0 and its
	// summary line is never logged.
	//
	// SEMANTIC SCOPE: the filter applies at dataset LOAD, which happens when
	// an index is (re)built — warm boots that deserialize existing index
	// files are unaffected until the operator deletes an index file to force
	// a rebuild. Filtered-out rows also disappear from name lookups,
	// /coordinates results and population-rank winners; that is the point of
	// the knob.
	IncludeFeatureClasses []string

	// ExcludeAdminDivisions skips every row whose GeoNames feature class
	// (field index 6, 0-based, tab-separated) is exactly "A" — countries
	// (PCLI), states/provinces (ADM1/ADM2), districts (ADM3/ADM4). Those rows
	// carry huge synthetic populations and otherwise win mid-ocean
	// rank=population queries under the gravity model. The check runs before
	// the mandatory-field and coordinate checks, so a class-A row is always
	// counted here (skippedAdminDivisions) rather than as a malformed row.
	//
	// SEMANTIC SCOPE: the filter applies at dataset LOAD, which happens when
	// an index is (re)built — warm boots that deserialize existing index
	// files are unaffected until the operator deletes an index file to force
	// a rebuild. When enabled, admin-division names (e.g. "California" as an
	// ADM1 row) also disappear from name lookups and /coordinates results;
	// that is the point of the knob.
	ExcludeAdminDivisions bool
}

// validateIncludeFeatureClasses checks every IncludeFeatureClasses entry at
// load start and returns an error naming every invalid entry. Validation
// precedes the os.Open so a bad option fails without touching the filesystem.
func validateIncludeFeatureClasses(classes []string) error {
	if len(classes) == 0 {
		return nil
	}
	var invalid []string
	for _, class := range classes {
		if !IsValidFeatureClass(class) {
			invalid = append(invalid, class)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("invalid IncludeFeatureClasses entries %q: each must be exactly one of the GeoNames feature classes A P H L R S T U V (single uppercase letter)", invalid)
	}
	return nil
}

// LoadGeoNamesCSVWithLimit loads cities from a GeoNames CSV file with an optional limit
func LoadGeoNamesCSVWithLimit(filepath string, limit int) ([]city.SpatialCity, error) {
	return LoadGeoNamesCSVWithOptions(filepath, LoadOptions{Limit: limit})
}

// LoadGeoNamesCSVWithOptions loads cities from a GeoNames CSV file under
// LoadOptions. See LoadOptions for the option semantics.
func LoadGeoNamesCSVWithOptions(filepath string, opts LoadOptions) ([]city.SpatialCity, error) {
	// Allowlist entries are validated before the file is opened: a bad option
	// is a configuration error, not an I/O one.
	if err := validateIncludeFeatureClasses(opts.IncludeFeatureClasses); err != nil {
		return nil, err
	}

	// The include-set is consulted per row; build it once. Only non-empty
	// sets filter (see LoadOptions).
	var includeSet map[string]bool
	if len(opts.IncludeFeatureClasses) > 0 {
		includeSet = make(map[string]bool, len(opts.IncludeFeatureClasses))
		for _, class := range opts.IncludeFeatureClasses {
			includeSet[class] = true
		}
	}

	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	// Use larger buffer for scanner to reduce I/O overhead
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024) // 64KB buffer
	scanner.Buffer(buf, 1024*1024)  // 1MB max line size

	// Preallocate the slice from a file-size-based row estimate (an explicit
	// limit takes precedence) instead of a hardcoded 15M rows.
	cities := make([]city.SpatialCity, 0, estimatedCityCount(filepath, opts.Limit))
	lineCount := 0
	skippedAdminDivisions := 0
	skippedByClassAllowlist := 0
	for scanner.Scan() {
		line := scanner.Bytes() // Use Bytes() instead of Text() to avoid string allocation
		lineCount++
		// Log progress every 1 million lines to reduce overhead (only if limit is large)
		if opts.Limit == 0 && lineCount%1000000 == 0 {
			log.Printf("Processing line %d...", lineCount)
		}

		// Convert to string once for field extraction
		lineStr := string(line)

		// Optional feature-class filtering: the include-list runs FIRST (a
		// row must be in the set), then ExcludeAdminDivisions drops class-A
		// survivors — so when A is not in the include-list the exclude flag
		// is a no-op. Both checks run (and only when enabled) before the
		// mandatory-field and coordinate checks, so a filtered row is counted
		// as a class skip (never as malformed); the default path with both
		// knobs off does zero extra work. Skipped rows are counted and
		// summarized in ONE log line at end of load, never logged per row
		// (prod is 13.5M rows).
		if includeSet != nil || opts.ExcludeAdminDivisions {
			featureClass := findField(lineStr, 6, '\t')
			if includeSet != nil && !includeSet[featureClass] {
				skippedByClassAllowlist++
				continue
			}
			if opts.ExcludeAdminDivisions && featureClass == "A" {
				skippedAdminDivisions++
				continue
			}
		}

		// Use strings.Index for more efficient field extraction to reduce allocations
		field2 := findField(lineStr, 1, '\t')   // name
		field4 := findField(lineStr, 3, '\t')   // alternatenames
		field5 := findField(lineStr, 4, '\t')   // latitude
		field6 := findField(lineStr, 5, '\t')   // longitude
		field9 := findField(lineStr, 8, '\t')   // country code
		field11 := findField(lineStr, 10, '\t') // admin1 code (GeoNames field 10, 0-indexed)
		field12 := findField(lineStr, 11, '\t') // admin2 code (GeoNames field 11, 0-indexed)
		field15 := findField(lineStr, 14, '\t') // population (GeoNames field 14, 0-indexed)

		if field2 == "" || field5 == "" || field6 == "" || field9 == "" {
			continue
		}

		lat, err := strconv.ParseFloat(field5, 64)
		if err != nil {
			log.Printf("Error parsing lat: %v on line: %s\n", err, line)
			continue
		}
		lon, err := strconv.ParseFloat(field6, 64)
		if err != nil {
			log.Printf("Error parsing lon: %v on line: %s\n", err, line)
			continue
		}

		// Population is enhancement data: an empty or unparsable field loads
		// as 0 and never drops the row. Values fit int32 (the largest GeoNames
		// feature populations are < 40M); anything overflowing is treated as
		// unparsable rather than silently wrapped.
		var population int32
		if p, err := strconv.ParseInt(field15, 10, 32); err == nil {
			population = int32(p)
		}

		// Parse alternate names more efficiently - avoid allocation if empty
		var altNames []string
		if field4 != "" {
			altNames = strings.Split(field4, ",")
		}

		cityObj := city.City{
			Latitude:   lat,
			Longitude:  lon,
			Population: population,
			Name:       field2,
			Country:    field9,
			// AltNames removed from City struct for memory optimization
		}

		spatialCity := city.SpatialCity{
			City:       cityObj,
			AltNames:   altNames, // AltNames stored in SpatialCity for building only
			Admin1Code: field11,  // build-only; empty field means "no admin1 code"
			Admin2Code: field12,  // build-only; empty field means "no admin2 code"
		}

		cities = append(cities, spatialCity)

		// Check limit
		if opts.Limit > 0 && len(cities) >= opts.Limit {
			log.Printf("Reached limit of %d cities, stopping early", opts.Limit)
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan file: %v, %v", filepath, err)
	}

	// Skip summaries in the postal loader's style: one line each, end of
	// load, only when rows were actually skipped (never per row).
	if skippedByClassAllowlist > 0 {
		log.Printf("skipped %d rows outside feature class allowlist [%s] in %s",
			skippedByClassAllowlist, strings.Join(opts.IncludeFeatureClasses, ","), filepath)
	}
	if skippedAdminDivisions > 0 {
		log.Printf("skipped %d admin division rows (feature class A) in %s", skippedAdminDivisions, filepath)
	}

	log.Printf("Loaded %d cities from %s\n", len(cities), filepath)
	return cities, nil
}
