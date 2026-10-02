package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// TestGenDatasetIsValidGeoNames pins that generated files are valid UTF-8 and
// parse with the production loaders.
func TestGenDatasetIsValidGeoNames(t *testing.T) {
	dir := t.TempDir()
	if err := genDataset(dir, 3000, 1); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"allCountries_dump.txt", "allCountries_zip.txt", "admin1CodesASCII.txt"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		s := bufio.NewScanner(f)
		for s.Scan() {
			if !utf8.ValidString(s.Text()) {
				t.Fatalf("%s: invalid UTF-8 line %q", name, s.Text())
			}
		}
		f.Close()
	}
	cities, err := dataLoader.LoadGeoNamesCSV(filepath.Join(dir, "allCountries_dump.txt"))
	if err != nil || len(cities) != 3000 {
		t.Fatalf("loaded %d cities, err %v", len(cities), err)
	}
	postal, err := dataLoader.LoadPostalCodes(filepath.Join(dir, "allCountries_zip.txt"))
	if err != nil || len(postal) == 0 {
		t.Fatalf("postal: %d countries, err %v", len(postal), err)
	}
	alts := 0
	for _, c := range cities {
		if len(c.Country) != 2 || strings.TrimSpace(c.Name) == "" {
			t.Fatalf("malformed row %+v", c)
		}
		alts += len(c.AltNames)
	}
	if alts == 0 {
		t.Fatal("expected alternate names")
	}
}
