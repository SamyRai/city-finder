package coordinates

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzDeserializeIndex feeds arbitrary bytes to DeserializeIndex: it must
// return an index or an error, never panic, whatever the file holds. The
// seeds are a valid index and damaged copies of it, so mutation starts from
// inputs that reach past the header and frame checks.
func FuzzDeserializeIndex(f *testing.F) {
	silenceIndexLogs(f)
	finder := buildBenchIndex(f, weightedCitySet())
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
		if got, err := DeserializeIndex(p); err == nil && got == nil {
			t.Fatal("nil index without an error")
		}
	})
}
