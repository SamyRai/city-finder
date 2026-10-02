package postalCode

import (
	"fmt"
	"io"
	"log"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// benchPostalEntries returns count distinct US-style postal entries.
func benchPostalEntries(count int) []dataLoader.PostalCodeEntry {
	entries := make([]dataLoader.PostalCodeEntry, count)
	for i := range entries {
		entries[i] = dataLoader.PostalCodeEntry{
			CountryCode: "US",
			PostalCode:  fmt.Sprintf("%05d", i),
			PlaceName:   fmt.Sprintf("City%d", i),
			Latitude:    float64(i%9000) / 100.0,
			Longitude:   float64(i%18000) / 100.0,
			Accuracy:    1,
		}
	}
	return entries
}

// benchPostalFinder returns a finder holding count distinct entries.
func benchPostalFinder(count int) *Finder {
	finder := NewPostalCodeFinder()
	for _, e := range benchPostalEntries(count) {
		finder.AddPostalCode(e)
	}
	return finder
}

// silencePostalLogs discards (de)serialization timing logs for the rest of
// the benchmark so the output stays benchstat-parseable; the log calls still
// run and format.
func silencePostalLogs(b *testing.B) {
	b.Helper()
	old := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(old) })
}

// BenchmarkAddPostalCode measures inserting one NEW distinct entry into a
// finder holding at most addBatch entries. Re-adding one fixed entry — the
// former shape — measured an overwrite of an existing key, not an insert.
// The finder is replaced off the clock every addBatch iterations so its size
// (and map growth state) stays bounded.
func BenchmarkAddPostalCode(b *testing.B) {
	const addBatch = 4096
	entries := benchPostalEntries(addBatch)

	b.ReportAllocs()
	finder := NewPostalCodeFinder()
	i := 0
	for b.Loop() {
		if i == addBatch {
			b.StopTimer()
			finder, i = NewPostalCodeFinder(), 0
			b.StartTimer()
		}
		finder.AddPostalCode(entries[i])
		i++
	}
}

// BenchmarkCityByPostalCode measures hits over 1k distinct keys. Keys are
// precomputed: formatting them inside the loop would add a Sprintf
// allocation to every measured lookup.
func BenchmarkCityByPostalCode(b *testing.B) {
	const count = 1000
	finder := benchPostalFinder(count)
	codes := make([]string, count)
	for i := range codes {
		codes[i] = fmt.Sprintf("%05d", (i*97)%count)
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if finder.CityByPostalCode(codes[i%count], "US") == nil {
			b.Fatalf("code %s must hit", codes[i%count])
		}
		i++
	}
}

// BenchmarkSerializeIndex measures serialization of a 10k-entry index to one
// fixed path (part file + rename, as every rebuild does).
func BenchmarkSerializeIndex(b *testing.B) {
	silencePostalLogs(b)
	finder := benchPostalFinder(10000)
	path := filepath.Join(b.TempDir(), "bench_postal_index.gob")

	b.ReportAllocs()
	for b.Loop() {
		if err := finder.SerializeIndex(path); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDeserializeIndex measures deserialization of a 10k-entry index
// through a warm OS page cache (decode CPU, not cold disk I/O).
func BenchmarkDeserializeIndex(b *testing.B) {
	silencePostalLogs(b)
	finder := benchPostalFinder(10000)
	path := filepath.Join(b.TempDir(), "bench_postal_index.gob")
	if err := finder.SerializeIndex(path); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := DeserializeIndex(path); err != nil {
			b.Fatal(err)
		}
	}
}
