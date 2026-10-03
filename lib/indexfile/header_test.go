package indexfile

import (
	"encoding/gob"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var errTestCorrupt = errors.New("test index is corrupt")

func testSpec() Spec {
	return Spec{Magic: "TESTIDX", Versions: []uint32{2, 3}, Corrupt: errTestCorrupt}
}

func TestOpenIndex(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, h Header) string {
		path := filepath.Join(dir, name)
		if err := Write(path, h, testPayload{}); err != nil {
			t.Fatal(err)
		}
		return path
	}

	r, h, err := OpenIndex(write("ok", Header{Magic: "TESTIDX", Version: 3, Count: 7}), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if h != (Header{Magic: "TESTIDX", Version: 3, Count: 7}) {
		t.Errorf("header = %+v", h)
	}

	for name, header := range map[string]Header{
		"magic":   {Magic: "OTHER", Version: 3},
		"version": {Magic: "TESTIDX", Version: 1},
	} {
		if _, _, err := OpenIndex(write(name, header), testSpec()); !errors.Is(err, errTestCorrupt) {
			t.Errorf("%s: err = %v, want the spec's sentinel", name, err)
		}
	}

	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("not gob"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenIndex(garbage, testSpec()); !errors.Is(err, errTestCorrupt) {
		t.Errorf("garbage: err = %v, want the spec's sentinel", err)
	}

	_, _, err = OpenIndex(filepath.Join(dir, "missing"), testSpec())
	if err == nil || errors.Is(err, errTestCorrupt) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v, want an environmental error", err)
	}
}

func TestOpenIndexAppliesEntryBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bomb")
	if err := Write(path, Header{Magic: "TESTIDX", Version: 3, Count: 1 << 40}, testPayload{}); err != nil {
		t.Fatal(err)
	}
	spec := testSpec()
	spec.Bounds = &EntryBounds{MaxBytesPerEntry: 1 << 10, MaxEntriesPerFileByte: 10}
	if _, _, err := OpenIndex(path, spec); !errors.Is(err, errTestCorrupt) {
		t.Errorf("err = %v, want the spec's sentinel", err)
	}
}

// TestHeaderWireFormat pins the header's gob field names and types: an
// independent struct with the same fields must decode what Write produced.
func TestHeaderWireFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h")
	if err := Write(path, Header{Magic: "M", Version: 9, Count: 4}, testPayload{}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got struct {
		Magic   string
		Version uint32
		Count   int
	}
	if err := gob.NewDecoder(f).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Magic != "M" || got.Version != 9 || got.Count != 4 {
		t.Errorf("decoded %+v", got)
	}
}
