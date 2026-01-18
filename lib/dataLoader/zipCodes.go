package dataLoader

import (
	"encoding/csv"
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

		lat, _ := strconv.ParseFloat(record[9], 64)
		lon, _ := strconv.ParseFloat(record[10], 64)
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

	return postalCodes, nil
}
