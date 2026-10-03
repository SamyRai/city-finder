package coordinates

import (
	"bytes"
	"encoding/gob"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/indexfile"
	"github.com/SamyRai/cityFinder/lib/indexfile/indexfiletest"
)

func writeCities(t *testing.T, path string, declared int, cities []city.City) {
	t.Helper()
	ids := make([]int32, len(cities))
	for i := range ids {
		ids[i] = -1
	}
	err := indexfile.Write(path,
		indexHeader{Magic: indexMagic, Version: indexVersion, Count: declared},
		&SerializableS2Finder{Cities: cities, Admin1IDs: ids, Admin2IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
}

// TestDeserializeRejectsUndercountedBomb: a header that declares few cities
// over a payload of millions is stopped by the count-derived byte budget,
// with the default budget, long before the payload is inflated.
func TestDeserializeRejectsUndercountedBomb(t *testing.T) {
	silenceIndexLogs(t)
	path := filepath.Join(t.TempDir(), "undercount.gob")
	writeCities(t, path, 1000, make([]city.City, 3_000_000))
	indexfiletest.Bounded(t, time.Second, 256<<20, func() {
		if _, err := DeserializeIndex(path); !errors.Is(err, ErrCorruptIndex) {
			t.Errorf("err = %v, want ErrCorruptIndex", err)
		}
	})
}

// TestDeserializeRejectsNegativeCount: a negative declared count is corrupt.
func TestDeserializeRejectsNegativeCount(t *testing.T) {
	silenceIndexLogs(t)
	path := filepath.Join(t.TempDir(), "negative.gob")
	writeCities(t, path, -1, nil)
	if _, err := DeserializeIndex(path); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("err = %v, want ErrCorruptIndex", err)
	}
}

// TestPayloadBoundsAdmitRepetitiveData: the bounds never reject the most
// repetitive data the loader admits (identical rows, down to the degenerate
// one-letter name at 0/0), at a scale where the ratio has converged, and
// leave a factor of margin below the limit.
func TestPayloadBoundsAdmitRepetitiveData(t *testing.T) {
	const n = 1_500_000
	rows := map[string]city.City{
		"typical":    {Name: "Springfield", Country: "US", Latitude: 39.78, Longitude: -89.65, Population: 116250},
		"degenerate": {Name: "A", Country: "US"},
	}
	for name, row := range rows {
		cities := make([]city.City, n)
		for i := range cities {
			cities[i] = row
		}
		path := filepath.Join(t.TempDir(), name+".gob")
		writeCities(t, path, n, cities)
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		perByte := float64(n) / float64(fi.Size())
		t.Logf("%s: %d cities in %d bytes = %.0f entries per file byte", name, n, fi.Size(), perByte)
		if perByte*1.5 > float64(payloadBounds.MaxEntriesPerFileByte) {
			t.Errorf("%s: %.0f entries per byte leaves under 1.5x margin below the bound %d", name, perByte, payloadBounds.MaxEntriesPerFileByte)
		}

		r, err := indexfile.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var h indexHeader
		if err := r.Header(&h); err != nil {
			t.Fatal(err)
		}
		if err := r.BoundByEntries(h.Count, payloadBounds); err != nil {
			t.Errorf("%s: legitimate repetitive data rejected: %v", name, err)
		}
		var p SerializableS2Finder
		if err := r.Payload(&p); err != nil {
			t.Errorf("%s: payload over the derived budget: %v", name, err)
		}
		_ = r.Close()
	}
}

// TestPayloadBoundsRejectEmptyCityBomb: the bomb's density is above the bound.
func TestPayloadBoundsRejectEmptyCityBomb(t *testing.T) {
	const n = 3_000_000
	path := filepath.Join(t.TempDir(), "bomb.gob")
	writeCities(t, path, n, make([]city.City, n))
	r, err := indexfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var h indexHeader
	if err := r.Header(&h); err != nil {
		t.Fatal(err)
	}
	if err := r.BoundByEntries(h.Count, payloadBounds); !errors.Is(err, indexfile.ErrFormat) {
		t.Fatalf("err = %v, want ErrFormat", err)
	}
}

// TestPayloadBoundsCoverWorstEntry: one entry with a 1 KiB name and extreme
// numbers encodes under MaxBytesPerEntry (the bound is on the average, so a
// single such entry fits many times over).
func TestPayloadBoundsCoverWorstEntry(t *testing.T) {
	worst := city.City{
		Name:       strings.Repeat("ß", 512),
		Country:    "ZZ",
		Latitude:   -89.123456789012,
		Longitude:  -179.123456789012,
		Population: math.MaxInt32,
	}
	const n = 1000
	cities := make([]city.City, n)
	ids := make([]int32, n)
	for i := range cities {
		cities[i] = worst
		ids[i] = math.MaxInt32
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&SerializableS2Finder{Cities: cities, Admin1IDs: ids, Admin2IDs: ids}); err != nil {
		t.Fatal(err)
	}
	if per := int64(buf.Len() / n); per > payloadBounds.MaxBytesPerEntry {
		t.Fatalf("worst entry encodes to %d B, over MaxBytesPerEntry %d", per, payloadBounds.MaxBytesPerEntry)
	}
}
