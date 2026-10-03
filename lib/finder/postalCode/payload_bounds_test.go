package postalCode

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/indexfile"
	"github.com/SamyRai/cityFinder/lib/indexfile/indexfiletest"
)

func quietLogs(t *testing.T) {
	t.Helper()
	old := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(old) })
}

func emptyColumns(n int) countryColumns {
	return countryColumns{Codes: make([]string, n), Latitude: make([]float64, n), Longitude: make([]float64, n), Place: make([]string, n)}
}

// TestDeserializeRejectsCountedBomb: a header that honestly declares the
// bomb's two million entries is implausible for a file of a few hundred
// bytes, so it is rejected before inflating, with the default budget.
func TestDeserializeRejectsCountedBomb(t *testing.T) {
	quietLogs(t)
	const n = 2_000_000
	path := filepath.Join(t.TempDir(), "counted.gob")
	err := indexfile.Write(path,
		indexHeader{Magic: indexMagic, Version: indexVersion, Count: n},
		&payloadV4{Countries: map[string]countryColumns{"US": emptyColumns(n)}})
	if err != nil {
		t.Fatal(err)
	}
	indexfiletest.Bounded(t, time.Second, 16<<20, func() {
		if _, err := DeserializeIndex(path); !errors.Is(err, ErrCorruptIndex) {
			t.Errorf("err = %v, want ErrCorruptIndex", err)
		}
	})
}

// TestDeserializeRejectsNegativeCount: a negative declared count is corrupt.
func TestDeserializeRejectsNegativeCount(t *testing.T) {
	quietLogs(t)
	path := filepath.Join(t.TempDir(), "negative.gob")
	err := indexfile.Write(path, indexHeader{Magic: indexMagic, Version: indexVersion, Count: -5}, &payloadV4{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DeserializeIndex(path); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("err = %v, want ErrCorruptIndex", err)
	}
}

// TestPayloadBoundsAdmitRepetitiveData: sequential codes with identical
// coordinates and place name are the most compressible legitimate postal
// data (codes must be unique and sorted); they stay far under the bound, in
// both file versions.
func TestPayloadBoundsAdmitRepetitiveData(t *testing.T) {
	quietLogs(t)
	const n = 300_000
	codes := make([]string, n)
	places := make([]string, n)
	byCode := make(map[string]dataLoader.PostalCodeEntry, n)
	for i := range codes {
		codes[i] = fmt.Sprintf("%06d", i)
		places[i] = "X"
		byCode[codes[i]] = dataLoader.PostalCodeEntry{CountryCode: "US", PlaceName: "X"}
	}
	cols := countryColumns{Codes: codes, Latitude: make([]float64, n), Longitude: make([]float64, n), Place: places}
	files := map[string]struct {
		version uint32
		payload any
	}{
		"v4": {indexVersion, &payloadV4{Countries: map[string]countryColumns{"US": cols}}},
		"v3": {indexVersionV3, &payloadV3{PostalCode: map[string]map[string]dataLoader.PostalCodeEntry{"US": byCode}}},
	}
	for name, f := range files {
		path := filepath.Join(t.TempDir(), name+".gob")
		if err := indexfile.Write(path, indexHeader{Magic: indexMagic, Version: f.version, Count: n}, f.payload); err != nil {
			t.Fatal(err)
		}
		got, err := DeserializeIndex(path)
		if err != nil {
			t.Fatalf("%s: legitimate repetitive data rejected: %v", name, err)
		}
		if got.Len() != n {
			t.Fatalf("%s: loaded %d entries, want %d", name, got.Len(), n)
		}
	}
}

// TestPayloadBoundsCoverWorstEntry: an entry with long strings encodes under
// MaxBytesPerEntry in both layouts.
func TestPayloadBoundsCoverWorstEntry(t *testing.T) {
	const n = 500
	long := strings.Repeat("ß", 256)
	cols := countryColumns{Codes: make([]string, n), Latitude: make([]float64, n), Longitude: make([]float64, n), Place: make([]string, n)}
	byCode := make(map[string]dataLoader.PostalCodeEntry, n)
	for i := range n {
		code := fmt.Sprintf("%020d", i)
		cols.Codes[i], cols.Latitude[i], cols.Longitude[i], cols.Place[i] = code, -89.123456789, -179.123456789, long
		byCode[code] = dataLoader.PostalCodeEntry{
			Latitude: -89.123456789, Longitude: -179.123456789, Accuracy: math.MaxInt32,
			CountryCode: "ZZ", PostalCode: code, PlaceName: long, AdminName1: long, AdminCode1: long,
		}
	}
	for name, payload := range map[string]any{
		"v4": &payloadV4{Countries: map[string]countryColumns{"US": cols}},
		"v3": &payloadV3{PostalCode: map[string]map[string]dataLoader.PostalCodeEntry{"US": byCode}},
	} {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(payload); err != nil {
			t.Fatal(err)
		}
		if per := int64(buf.Len() / n); per > payloadBounds.MaxBytesPerEntry {
			t.Errorf("%s: worst entry encodes to %d B, over MaxBytesPerEntry %d", name, per, payloadBounds.MaxBytesPerEntry)
		}
	}
}
