package postalCode

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// FuzzDeserializeIndex feeds arbitrary bytes to DeserializeIndex: it must
// return an index or an error, never panic. Seeds: a valid v4 index and
// damaged copies of it.
func FuzzDeserializeIndex(f *testing.F) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	f.Cleanup(func() { log.SetOutput(old) })

	finder := BuildIndex(map[string]map[string]dataLoader.PostalCodeEntry{
		"US": {"10001": {Latitude: 40.75, Longitude: -73.99, PlaceName: "New York"}, "94103": {Latitude: 37.77, Longitude: -122.41, PlaceName: "San Francisco"}},
		"DE": {"10115": {Latitude: 52.53, Longitude: 13.38, PlaceName: "Berlin"}},
	})
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
		if got, err := DeserializeIndex(p); err == nil {
			// A decoded index must answer lookups without panicking.
			_ = got.CityByPostalCode("10001", "US")
		}
	})
}
