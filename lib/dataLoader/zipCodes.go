package dataLoader

import (
	"os"
	"strconv"
)

// PostalCodeEntry optimized for memory alignment (Go 1.22+ best practice)
// Field ordering: largest types first (float64 = 8 bytes), then int (8 bytes on 64-bit), then strings (16 bytes each)
// This reduces padding and improves cache locality when processing many entries
type PostalCodeEntry struct {
	Latitude    float64 // 8 bytes - aligned to 8-byte boundary
	Longitude   float64 // 8 bytes - aligned to 8-byte boundary
	Accuracy    int     // 8 bytes on 64-bit - aligned to 8-byte boundary
	CountryCode string  // 16 bytes (string header) - aligned to 8-byte boundary
	PostalCode  string  // 16 bytes (string header) - aligned to 8-byte boundary
	PlaceName   string  // 16 bytes (string header) - aligned to 8-byte boundary
	AdminName1  string  // 16 bytes (string header) - aligned to 8-byte boundary
	AdminCode1  string  // 16 bytes (string header) - aligned to 8-byte boundary
	AdminName2  string  // 16 bytes (string header) - aligned to 8-byte boundary
	AdminCode2  string  // 16 bytes (string header) - aligned to 8-byte boundary
	AdminName3  string  // 16 bytes (string header) - aligned to 8-byte boundary
	AdminCode3  string  // 16 bytes (string header) - aligned to 8-byte boundary
}

func LoadPostalCodes(filepath string) (map[string]map[string]PostalCodeEntry, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// GeoNames ships ~14M rows and is hand-curated upstream: a row with a
	// stray extra field must not abort the whole load (and with it server
	// startup) — such rows are counted and skipped below. The file is plain
	// TSV, so lines are split on tabs and quotes mean nothing; encoding/csv's
	// quote handling aborted the load on a place name like 5" Rd.
	// Pre-allocate with reasonable capacity based on typical postal code data size
	postalCodes := make(map[string]map[string]PostalCodeEntry, 200) // ~200 countries

	var skipped skipReport

	var record []string
	err = forEachLine(file, &skipped, func(line int, raw []byte) bool {
		if len(raw) == 0 {
			return true
		}
		record = splitTab(string(raw), record)

		// Skip malformed records
		if len(record) < 12 {
			skipped.add(reasonShortRow, line)
			return true
		}

		// A row whose latitude or longitude cannot be parsed, or is not a
		// finite in-range point, is not indexed: silently defaulting it to
		// (0,0) put Null-Island coordinates behind /postalCode lookups.
		// Accuracy stays lenient on purpose (it is a 0-6 hint, not a
		// coordinate). One summary line at end of load — no per-row
		// logging, prod files hold ~14M rows.
		lat, lon, reason := parseCoordinate(record[9], record[10])
		if reason != "" {
			skipped.add(reason, line)
			return true
		}
		accuracy, _ := strconv.Atoi(record[11])

		postalCode := PostalCodeEntry{
			CountryCode: record[0],
			PostalCode:  record[1],
			PlaceName:   record[2],
			AdminName1:  record[3],
			AdminCode1:  record[4],
			AdminName2:  record[5],
			AdminCode2:  record[6],
			AdminName3:  record[7],
			AdminCode3:  record[8],
			Latitude:    lat,
			Longitude:   lon,
			Accuracy:    accuracy,
		}

		countryCode := postalCode.CountryCode
		if _, exists := postalCodes[countryCode]; !exists {
			postalCodes[countryCode] = make(map[string]PostalCodeEntry, 1000) // Pre-allocate reasonable capacity per country
		}
		postalCodes[countryCode][postalCode.PostalCode] = postalCode
		return true
	})
	if err != nil {
		return nil, err
	}
	skipped.log("postal", filepath)

	return postalCodes, nil
}
