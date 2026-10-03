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

// TestGenDatasetIsDeterministic pins that one seed always yields the same
// bytes (a before/after comparison is meaningless otherwise) and that another
// seed does not.
func TestGenDatasetIsDeterministic(t *testing.T) {
	files := []string{"allCountries_dump.txt", "allCountries_zip.txt", "admin1CodesASCII.txt"}
	read := func(seed int64) map[string][]byte {
		dir := t.TempDir()
		if err := genDataset(dir, 500, seed); err != nil {
			t.Fatal(err)
		}
		out := map[string][]byte{}
		for _, name := range files {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			out[name] = b
		}
		return out
	}
	a, b, c := read(7), read(7), read(8)
	for _, name := range files {
		if string(a[name]) != string(b[name]) {
			t.Errorf("%s differs between runs with the same seed", name)
		}
		if string(a[name]) == string(c[name]) {
			t.Errorf("%s identical across different seeds", name)
		}
	}
}

// TestGenDatasetUnwritableDirFails pins the error path: a regular file where
// the output directory should be.
func TestGenDatasetUnwritableDirFails(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := genDataset(filepath.Join(blocker, "sub"), 10, 1); err == nil {
		t.Fatal("expected an error")
	}
}

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
