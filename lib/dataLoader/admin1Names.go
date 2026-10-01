package dataLoader

import (
	"bufio"
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

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	names := make(map[string]string, 8192) // ~4K distinct admin1 pairs worldwide, with headroom
	skipped := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// Fields: code, name, name-ascii, geonameId — tab-separated.
		tab := strings.IndexByte(line, '\t')
		if tab <= 0 {
			skipped++
			continue
		}
		key := line[:tab]
		rest := line[tab+1:]
		name := rest
		if end := strings.IndexByte(rest, '\t'); end >= 0 {
			name = rest[:end]
		}
		if key == "" || name == "" {
			skipped++
			continue
		}
		names[key] = name
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan file: %v, %v", filepath, err)
	}
	if skipped > 0 {
		log.Printf("Skipped %d malformed admin1-names lines in %s", skipped, filepath)
	}
	log.Printf("Loaded %d admin1 names from %s\n", len(names), filepath)
	return names, nil
}
