// Package indexfiletest holds the helpers the index packages' hostile-file
// tests share: a tightened payload budget, a crafted zstd frame that
// declares a huge window, and a time and allocation bound around a decode.
// Tests that use TightenBudget must not run in parallel.
package indexfiletest

import (
	"bytes"
	"runtime"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/indexfile"
)

// zstdMagic starts every zstd frame; the gob header before it is not zstd.
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// hugeWindowFrame is a frame header only: window descriptor 0x98 declares a
// 512 MB window, the zstd library's default maximum, and no data follows.
var hugeWindowFrame = []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x98, 0x01, 0x00, 0x00}

// TightenBudget lowers indexfile.DefaultMaxPayloadBytes for the test, so a
// bomb of a few million entries is over budget without a multi-GB fixture.
func TightenBudget(t testing.TB, maxBytes int64) {
	t.Helper()
	old := indexfile.DefaultMaxPayloadBytes
	indexfile.DefaultMaxPayloadBytes = maxBytes
	t.Cleanup(func() { indexfile.DefaultMaxPayloadBytes = old })
}

// WindowBomb returns valid, a complete index file, with its zstd frame
// replaced by hugeWindowFrame: the header still passes, the payload must not.
func WindowBomb(t testing.TB, valid []byte) []byte {
	t.Helper()
	i := bytes.Index(valid, zstdMagic)
	if i < 0 {
		t.Fatal("no zstd frame in the file")
	}
	return append(append([]byte{}, valid[:i]...), hugeWindowFrame...)
}

// Bounded runs decode and fails the test if it takes longer than maxTime or
// allocates (cumulatively, TotalAlloc) more than maxAlloc bytes.
func Bounded(t testing.TB, maxTime time.Duration, maxAlloc uint64, decode func()) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	decode()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("decode took %s and allocated %d MB", elapsed, alloc>>20)
	if elapsed > maxTime {
		t.Errorf("decode took %s, want <= %s", elapsed, maxTime)
	}
	if alloc > maxAlloc {
		t.Errorf("decode allocated %d MB, want <= %d MB", alloc>>20, maxAlloc>>20)
	}
}
