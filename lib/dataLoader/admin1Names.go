package dataLoader

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// LoadAdmin1Names loads the optional GeoNames admin1CodesASCII.txt dataset:
// one `code\tname\tname-ascii\tgeonameId` line per admin1 division, where
// code is the concatenated "CC.CODE" composite key (e.g. "US.CA", "AD.02")
// that matches the S2 index's Admin1Codes table. The returned map carries
// composite key -> name (column 1, the canonical name; the ASCII column
// differs only for non-Latin scripts and JSON marshaling handles both).
//
// The file is OPTIONAL (~120 KB): when it is absent the caller serves
// codes-only responses (v1.1 design note). Malformed lines are counted,
// logged once in summary, and skipped — a truncated tail line (an incomplete
// download, say) must not take the whole names table down with it.
func LoadAdmin1Names(filepath string) (map[string]string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	names := make(map[string]string, 8192) // ~4K distinct admin1 pairs worldwide, with headroom
	var skipped skipReport
	err = forEachLine(file, &skipped, func(n int, raw []byte) bool {
		if len(raw) == 0 {
			return true
		}
		line := string(raw)
		// Fields: code, name, name-ascii, geonameId — tab-separated.
		tab := strings.IndexByte(line, '\t')
		if tab <= 0 {
			skipped.add(reasonMalformedLine, n)
			return true
		}
		key := line[:tab]
		rest := line[tab+1:]
		name := rest
		if end := strings.IndexByte(rest, '\t'); end >= 0 {
			name = rest[:end]
		}
		if key == "" || name == "" {
			skipped.add(reasonMalformedLine, n)
			return true
		}
		names[key] = name
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan file: %v, %v", filepath, err)
	}
	skipped.log("admin1-names", filepath)
	log.Printf("Loaded %d admin1 names from %s\n", len(names), filepath)
	return names, nil
}
