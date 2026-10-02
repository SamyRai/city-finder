package dataLoader

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
)

// TestLoadedCitiesDoNotPinSourceLines pins that a loaded City does not keep
// its whole source line alive. Name and Country used to be substrings of the
// per-line string, so the S2 index — which keeps every City for the process
// lifetime — retained the entire dump text, alternate names included. Rows
// here carry ~1 KiB of alternate names; keeping only the City values must
// not keep that text resident.
func TestLoadedCitiesDoNotPinSourceLines(t *testing.T) {
	const rows = 20_000
	alts := strings.Repeat("Alternate Name Variant,", 45) // ~1 KiB per row
	var sb strings.Builder
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&sb, "%d\tTown%d\tTown%d\t%s\t1.5\t2.5\tP\tPPL\tAD\t\t\t\t\t\t0\t\t0\tEtc/UTC\t2026-01-01\n", i, i, i, alts)
	}
	path := filepath.Join(t.TempDir(), "rows.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	sb.Reset()

	settle := func() uint64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	before := settle()
	loaded, err := LoadGeoNamesCSV(path)
	if err != nil || len(loaded) != rows {
		t.Fatalf("loaded %d rows, err %v", len(loaded), err)
	}
	kept := make([]city.City, len(loaded))
	for i := range loaded {
		kept[i] = loaded[i].City
	}
	loaded = nil
	retained := int64(settle()) - int64(before)
	runtime.KeepAlive(kept)

	// Kept values: 56 B struct + ~8 B name per row ≈ 1.3 MB. Pinned lines
	// would add ~20 MB (1 KiB per row).
	if limit := int64(rows * 200); retained > limit {
		t.Fatalf("keeping %d City values retained %d B (> %d): source lines are pinned", rows, retained, limit)
	}
}
