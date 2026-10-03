package name

import (
	"io"
	"log"
	"os"
	"path/filepath"
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
	})
}
