package indexfile_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/indexfile"
)

func openHeader(t *testing.T, path string) *indexfile.Reader {
	t.Helper()
	r, err := indexfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	var h hdr
	if err := r.Header(&h); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBoundByEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.gob")
	p := pay{Names: []string{"a", "b", "c"}, IDs: []int32{1, 2, 3}}
	if err := indexfile.Write(path, hdr{"MAGIC", 3}, &p); err != nil {
		t.Fatal(err)
	}
	bounds := indexfile.EntryBounds{MaxBytesPerEntry: 64, MaxEntriesPerFileByte: 10}

	r := openHeader(t, path)
	if err := r.BoundByEntries(3, bounds); err != nil {
		t.Fatalf("honest count rejected: %v", err)
	}
	var got pay
	if err := r.Payload(&got); err != nil || len(got.Names) != 3 {
		t.Fatalf("payload under the derived budget failed: %v", err)
	}

	for _, count := range []int{-1, 1 << 40} {
		if err := openHeader(t, path).BoundByEntries(count, bounds); !errors.Is(err, indexfile.ErrFormat) {
			t.Errorf("count %d: err = %v, want ErrFormat", count, err)
		}
	}
}

// TestBoundByEntriesNeverLoosens: the derived budget is a minimum with the
// current one, so a tightened default stays in force.
func TestBoundByEntriesNeverLoosens(t *testing.T) {
	path := zeroBomb(t, 4_000_000)
	r := openHeader(t, path)
	r.LimitPayload(1 << 10)
	huge := indexfile.EntryBounds{MaxBytesPerEntry: 1 << 20, MaxEntriesPerFileByte: 1 << 30}
	if err := r.BoundByEntries(10, huge); err != nil {
		t.Fatal(err)
	}
	var p pay
	if err := r.Payload(&p); !errors.Is(err, indexfile.ErrFormat) {
		t.Fatalf("err = %v, want ErrFormat from the earlier 1 KiB limit", err)
	}
}
