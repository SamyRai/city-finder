package dataLoader

import (
	"encoding/csv"
	"log"
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

	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.ReuseRecord = true // Reuse record slice to reduce allocations

	// Pre-allocate with reasonable capacity based on typical postal code data size
	postalCodes := make(map[string]map[string]PostalCodeEntry, 200) // ~200 countries

	skipped := 0

	for {
		record, err := reader.Read()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}

		// Skip malformed records
		if len(record) < 12 {
			continue
		}

		// A row whose latitude or longitude cannot be parsed is not indexed:
		// silently defaulting it to (0,0) put Null-Island coordinates behind
		// /postalCode lookups. Accuracy stays lenient on purpose (it is a
		// 0-6 hint, not a coordinate). One summary line at end of load — no
		// per-row logging, prod files hold ~14M rows.
		lat, err := strconv.ParseFloat(record[9], 64)
		if err != nil {
			skipped++
			continue
		}
		lon, err := strconv.ParseFloat(record[10], 64)
		if err != nil {
			skipped++
			continue
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
	}

	if skipped > 0 {
		log.Printf("skipped %d postal rows with unparsable coordinates in %s", skipped, filepath)
	}

	return postalCodes, nil
}
