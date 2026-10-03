package indexfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/SamyRai/cityFinder/lib/indexfile"
	"github.com/SamyRai/cityFinder/lib/indexfile/indexfiletest"
)

type hdr struct {
	Magic   string
	Version uint32
}

type pay struct {
	Names []string
	IDs   []int32
}

// decode reads path with an explicit payload budget (0 keeps the default).
func decode(t *testing.T, path string, limit int64) (pay, indexfile.Stats, error) {
	t.Helper()
	r, err := indexfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var h hdr
	if err := r.Header(&h); err != nil {
		t.Fatal(err)
	}
	if limit != 0 {
		r.LimitPayload(limit)
	}
	var p pay
	err = r.Payload(&p)
	return p, r.Stats(), err
}

// frameHeader parses the zstd frame header of the index file at path.
func frameHeader(t *testing.T, path string) zstd.Header {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range len(b) - 4 {
		if b[i] == 0x28 && b[i+1] == 0xb5 && b[i+2] == 0x2f && b[i+3] == 0xfd {
			var h zstd.Header
			if err := h.Decode(b[i:]); err != nil {
				t.Fatal(err)
			}
			return h
		}
	}
	t.Fatal("no zstd frame in the file")
	return zstd.Header{}
}

// TestDecoderWindowCapCoversWriter pins the coupling between Write and the
// Reader's window cap: a payload several windows long is written, the window
// the frame declares is read back, and the cap must leave 4x headroom.
func TestDecoderWindowCapCoversWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.gob")
	big := pay{}
	for i := range 3_000_000 {
		big.Names = append(big.Names, string([]rune{rune('a' + i%26), rune('a' + i/26%26), rune('a' + i/676%26)}))
		big.IDs = append(big.IDs, int32(i*7919))
	}
	if err := indexfile.Write(path, hdr{"MAGIC", 3}, &big); err != nil {
		t.Fatal(err)
	}
	h := frameHeader(t, path)
	if h.WindowSize == 0 || h.WindowSize*4 > indexfile.MaxDecoderWindow {
		t.Fatalf("writer declares a %d byte window, want 0 < window <= MaxDecoderWindow/4 (%d)", h.WindowSize, indexfile.MaxDecoderWindow/4)
	}
	got, _, err := decode(t, path, 0)
	if err != nil || len(got.Names) != len(big.Names) {
		t.Fatalf("a large legitimate file must still decode: %d names, err = %v", len(got.Names), err)
	}
}

// TestHugeWindowFrameRejectedCheaply: a header-valid file whose frame
// declares 512 MB must fail fast without allocating the window.
func TestHugeWindowFrameRejectedCheaply(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.gob")
	if err := indexfile.Write(good, hdr{"MAGIC", 3}, &pay{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	bomb := filepath.Join(dir, "bomb.gob")
	if err := os.WriteFile(bomb, indexfiletest.WindowBomb(t, b), 0o600); err != nil {
		t.Fatal(err)
	}
	indexfiletest.Bounded(t, time.Second, 64<<20, func() {
		if _, _, err := decode(t, bomb, 0); !errors.Is(err, indexfile.ErrFormat) {
			t.Errorf("err = %v, want ErrFormat", err)
		}
	})
}

// zeroBomb writes a valid file whose payload is n empty strings: a few
// hundred bytes of zstd that inflate to n bytes and n string headers.
func zeroBomb(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bomb.gob")
	if err := indexfile.Write(path, hdr{"MAGIC", 3}, &pay{Names: make([]string, n)}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPayloadBudget(t *testing.T) {
	path := zeroBomb(t, 4_000_000)
	_, stats, err := decode(t, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw := stats.PayloadBytes
	if raw < 4_000_000 {
		t.Fatalf("bomb inflated to only %d bytes", raw)
	}
	if _, _, err := decode(t, path, raw); err != nil {
		t.Errorf("a budget equal to the payload size must pass, got %v", err)
	}
	indexfiletest.Bounded(t, time.Second, 64<<20, func() {
		if _, _, err := decode(t, path, raw/8); !errors.Is(err, indexfile.ErrFormat) {
			t.Errorf("err = %v, want ErrFormat", err)
		}
	})
}

func TestDefaultPayloadBudgetApplies(t *testing.T) {
	path := zeroBomb(t, 4_000_000)
	indexfiletest.TightenBudget(t, 1<<20)
	if _, _, err := decode(t, path, 0); !errors.Is(err, indexfile.ErrFormat) {
		t.Fatalf("err = %v, want ErrFormat", err)
	}
}

// TestBudgetCutsMidStream: a limit hit while gob is mid-message still
// surfaces as ErrFormat, never a panic or a bare io error.
func TestBudgetCutsMidStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.gob")
	p := pay{Names: []string{"a", "b", "c"}, IDs: []int32{1, 2, 3}}
	if err := indexfile.Write(path, hdr{"MAGIC", 3}, &p); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{1, 5, 17} {
		if _, _, err := decode(t, path, limit); !errors.Is(err, indexfile.ErrFormat) {
			t.Errorf("limit %d: err = %v, want ErrFormat", limit, err)
		}
	}
}
