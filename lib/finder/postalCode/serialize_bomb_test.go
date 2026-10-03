package postalCode

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/indexfile"
	"github.com/SamyRai/cityFinder/lib/indexfile/indexfiletest"
)

// TestDeserializeRejectsBombCheaply: a valid header over four columns of two
// million empty entries (a few hundred bytes of zstd; 24 MB once decoded,
// 100+ MB as slices) is over budget and must come back as ErrCorruptIndex
// quickly, without allocating anywhere near the decoded size.
func TestDeserializeRejectsBombCheaply(t *testing.T) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(old) })

	const n = 2_000_000
	path := filepath.Join(t.TempDir(), "bomb.gob")
	cols := countryColumns{Codes: make([]string, n), Latitude: make([]float64, n), Longitude: make([]float64, n), Place: make([]string, n)}
	err := indexfile.Write(path,
		indexHeader{Magic: indexMagic, Version: indexVersion},
		&payloadV4{Countries: map[string]countryColumns{"US": cols}})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() > 10<<10 {
		t.Fatalf("bomb should be tiny, stat = %v, err = %v", fi, err)
	}
	indexfiletest.TightenBudget(t, 1<<20)
	indexfiletest.Bounded(t, time.Second, 256<<20, func() {
		if _, err := DeserializeIndex(path); !errors.Is(err, ErrCorruptIndex) {
			t.Errorf("err = %v, want ErrCorruptIndex", err)
		}
	})
}

// TestDeserializeRejectsHugeWindowCheaply: a frame declaring a 512 MB zstd
// window after a valid header is ErrCorruptIndex and never allocates it.
func TestDeserializeRejectsHugeWindowCheaply(t *testing.T) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(old) })

	path := filepath.Join(t.TempDir(), "seed.gob")
	finder := BuildIndex(map[string]map[string]dataLoader.PostalCodeEntry{
		"US": {"10001": {Latitude: 40.75, Longitude: -73.99, PlaceName: "New York"}},
	})
	if err := finder.SerializeIndex(path); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bomb := filepath.Join(t.TempDir(), "window.gob")
	if err := os.WriteFile(bomb, indexfiletest.WindowBomb(t, valid), 0o600); err != nil {
		t.Fatal(err)
	}
	indexfiletest.Bounded(t, time.Second, 64<<20, func() {
		if _, err := DeserializeIndex(bomb); !errors.Is(err, ErrCorruptIndex) {
			t.Errorf("err = %v, want ErrCorruptIndex", err)
		}
	})
}
