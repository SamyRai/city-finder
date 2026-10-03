package name

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// FuzzDeserializeIndex feeds arbitrary bytes to DeserializeIndex: it must
// return an index or an error, never panic, and a decoded index must answer
// lookups without panicking (every stored id was bounds-checked). Seeds: a
// valid standalone v3 index and damaged copies of it.
func FuzzDeserializeIndex(f *testing.F) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	f.Cleanup(func() { log.SetOutput(old) })

	finder := BuildIndex(fuzzyFixtureCities())
	path := filepath.Join(f.TempDir(), "seed.gob")
	if err := finder.SerializeIndex(path); err != nil {
		f.Fatal(err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add(valid[:len(valid)/2])
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "idx.gob")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := DeserializeIndex(p)
		if err != nil {
			return
		}
		_ = got.CityByName("Paris", "FR")
		_ = got.PrefixNames("FR", "Pa", 10)
		assertRoundTripStable(t, got)
	})
}

// assertRoundTripStable re-serializes a decoded index, decodes that, and
// requires the same reduced payload: whatever the decoder accepts, the writer
// must reproduce. (File bytes are not compared — map order makes them
// non-deterministic — and city values are compared by count only, since a
// fuzzed NaN coordinate never equals itself.) A detached index cannot be
// written and is skipped.
func assertRoundTripStable(t *testing.T, got *Finder) {
	t.Helper()
	got.mutex.RLock()
	first, err := got.buildPayloadV3Locked()
	got.mutex.RUnlock()
	if err != nil {
		return
	}
	p := filepath.Join(t.TempDir(), "again.gob")
	if err := got.SerializeIndex(p); err != nil {
		t.Fatalf("re-serializing a decoded index: %v", err)
	}
	again, err := DeserializeIndex(p)
	if err != nil {
		t.Fatalf("decoding a re-serialized index: %v", err)
	}
	again.mutex.RLock()
	second, err := again.buildPayloadV3Locked()
	again.mutex.RUnlock()
	if err != nil {
		t.Fatalf("payload of the round-tripped index: %v", err)
	}
	if first.CityCount != second.CityCount || first.Fingerprint != second.Fingerprint ||
		len(first.Cities) != len(second.Cities) || len(first.Extra) != len(second.Extra) ||
		!reflect.DeepEqual(first.Refs, second.Refs) {
		t.Fatalf("round trip changed the payload: %d/%d cities, %d/%d extras", len(first.Cities), len(second.Cities), len(first.Extra), len(second.Extra))
	}
}
