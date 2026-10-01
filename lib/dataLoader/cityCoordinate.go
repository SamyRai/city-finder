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

// LoadGeoNamesCSVWithLimit loads cities from a GeoNames CSV file with an optional limit
func LoadGeoNamesCSVWithLimit(filepath string, limit int) ([]city.SpatialCity, error) {
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
	cities := make([]city.SpatialCity, 0, estimatedCityCount(filepath, limit))
	lineCount := 0
	for scanner.Scan() {
		line := scanner.Bytes() // Use Bytes() instead of Text() to avoid string allocation
		lineCount++
		// Log progress every 1 million lines to reduce overhead (only if limit is large)
		if limit == 0 && lineCount%1000000 == 0 {
			log.Printf("Processing line %d...", lineCount)
		}

		// Convert to string once for field extraction
		lineStr := string(line)
		// Use strings.Index for more efficient field extraction to reduce allocations
		field2 := findField(lineStr, 1, '\t')   // name
		field4 := findField(lineStr, 3, '\t')   // alternatenames
		field5 := findField(lineStr, 4, '\t')   // latitude
		field6 := findField(lineStr, 5, '\t')   // longitude
		field9 := findField(lineStr, 8, '\t')   // country code
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
			City:     cityObj,
			AltNames: altNames, // AltNames stored in SpatialCity for building only
		}

		cities = append(cities, spatialCity)

		// Check limit
		if limit > 0 && len(cities) >= limit {
			log.Printf("Reached limit of %d cities, stopping early", limit)
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan file: %v, %v", filepath, err)
	}

	log.Printf("Loaded %d cities from %s\n", len(cities), filepath)
	return cities, nil
}
