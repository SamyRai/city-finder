package coordinates

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/indexfile"
	"github.com/SamyRai/cityFinder/lib/indexfile/indexfiletest"
)

// TestDeserializeRejectsBombCheaply: a valid header whose count matches a
// payload of three million zero-valued cities (a few hundred bytes of zstd;
// 168 MB once decoded) is over budget and must come back as ErrCorruptIndex
// quickly, without allocating anywhere near the decoded size.
func TestDeserializeRejectsBombCheaply(t *testing.T) {
	silenceIndexLogs(t)
	const n = 3_000_000
	path := filepath.Join(t.TempDir(), "bomb.gob")
	err := indexfile.Write(path,
		indexHeader{Magic: indexMagic, Version: indexVersion, Count: n},
		&SerializableS2Finder{Cities: make([]city.City, n), Admin1IDs: make([]int32, n), Admin2IDs: make([]int32, n)})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() > 10<<10 {
		t.Fatalf("bomb should be tiny, stat = %v, err = %v", fi, err)
	}
	indexfiletest.Bounded(t, time.Second, 256<<20, func() {
		if _, err := DeserializeIndex(path); !errors.Is(err, ErrCorruptIndex) {
			t.Errorf("err = %v, want ErrCorruptIndex", err)
		}
	})
}

// TestDeserializeRejectsHugeWindowCheaply: a frame declaring a 512 MB zstd
// window after a valid header is ErrCorruptIndex and never allocates it.
func TestDeserializeRejectsHugeWindowCheaply(t *testing.T) {
	silenceIndexLogs(t)
	path := filepath.Join(t.TempDir(), "seed.gob")
	if err := buildBenchIndex(t, weightedCitySet()).SerializeIndex(path); err != nil {
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
