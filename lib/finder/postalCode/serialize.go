package postalCode

import (
	"errors"
	"fmt"
	"log"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/indexfile"
)

// Serialized index file format:
//
//	gob(indexHeader{Magic: "CFPOSTIDX", Version, Count: total entries})
//	zstd-frame(gob(payload))
//
// The header stays uncompressed so version checks run before any
// decompression; a truncated or version-skewed file is rejected with
// ErrCorruptIndex instead of loading as zero-filled garbage. The framing,
// atomic durable writes and streaming reads belong to lib/indexfile.
//
// Version history:
//   - v2: gob of the loader's map[country]map[code]PostalCodeEntry. Rejected
//     (rebuilt from source).
//   - v3: the same map behind one zstd frame. Still READ: converted to the
//     compact in-memory table; the initializer then rewrites the file as v4.
//   - v4 (current): per country, columns of the served fields only — sorted
//     codes, latitudes, longitudes, place names (see entry).
const (
	indexMagic     = "CFPOSTIDX"
	indexVersionV3 = uint32(3)
	indexVersion   = uint32(4)
)

// indexHeader is the first gob value of every serialized postal code index.
type indexHeader = indexfile.Header

// payloadBounds relates the header's entry count to the file, so a bomb is
// rejected before it inflates (see indexfile.EntryBounds). Measured on the
// gob encoding:
//   - bytes per entry: 11-17 B (v4 columns and the legacy v3 map, with short
//     codes and place names); the bound is 2 KiB, since only the average
//     over all entries counts.
//   - entries per file byte: codes are unique and sorted, which caps how
//     repetitive legitimate data can be. Sequential codes with identical
//     coordinates and place name, the most compressible case, reach 3 per
//     byte (v4) and 0.3 (v3). A bomb of empty columns reaches thousands.
//     The bound of 64 sits above the legitimate case by an order of
//     magnitude and far below the bomb.
//
// payload_bounds_test.go re-measures both.
var payloadBounds = indexfile.EntryBounds{MaxBytesPerEntry: 2 << 10, MaxEntriesPerFileByte: 64}

// ErrCorruptIndex reports an index file that cannot be trusted: truncated,
// undecodable, or written by an incompatible format version. Detect it with
// errors.Is to decide whether a rebuild from source data is possible.
var ErrCorruptIndex = errors.New("postal code index file is corrupt or incompatible")

// countryColumns is one country's v4 payload. Columns (rather than a slice of
// structs) keep like values together, which compresses better.
type countryColumns struct {
	Codes     []string
	Latitude  []float64
	Longitude []float64
	Place     []string
}

// payloadV4 is the v4 payload: country -> columns.
type payloadV4 struct {
	Countries map[string]countryColumns
}

// payloadV3 is the legacy v3 payload (the exported field of the old Finder).
type payloadV3 struct {
	PostalCode map[string]map[string]dataLoader.PostalCodeEntry
}

// indexSpec is what indexfile.OpenIndex checks a file against.
var indexSpec = indexfile.Spec{Magic: indexMagic, Versions: []uint32{indexVersion, indexVersionV3}, Corrupt: ErrCorruptIndex, Bounds: &payloadBounds}

// SerializeIndex saves the postal code index (format v4) atomically.
func (pcf *Finder) SerializeIndex(filepath string) error {
	pcf.mutex.RLock()
	payload := payloadV4{Countries: make(map[string]countryColumns, len(pcf.countries))}
	total := 0
	for country, t := range pcf.countries {
		cols := countryColumns{
			Codes:     t.codes,
			Latitude:  make([]float64, len(t.entries)),
			Longitude: make([]float64, len(t.entries)),
			Place:     make([]string, len(t.entries)),
		}
		for i, e := range t.entries {
			cols.Latitude[i], cols.Longitude[i], cols.Place[i] = e.Latitude, e.Longitude, e.PlaceName
		}
		payload.Countries[country] = cols
		total += len(t.codes)
	}
	pcf.mutex.RUnlock()

	header := indexHeader{Magic: indexMagic, Version: indexVersion, Count: total}
	if err := indexfile.Write(filepath, header, &payload); err != nil {
		return fmt.Errorf("failed to write postal code index: %w", err)
	}
	return nil
}

// DeserializeIndex loads a postal code index file (v4, or the legacy v3).
// Every decode failure — bad header, unsupported version, truncated or
// corrupted zstd frame, malformed payload, inconsistent columns, or an entry
// count that disagrees with the header — wraps ErrCorruptIndex so the
// initializer can fall back to a rebuild. A v3 file loads into the compact
// table and reports LegacyFormat() so the caller can rewrite it.
func DeserializeIndex(filepath string) (*Finder, error) {
	file, header, err := indexfile.OpenIndex(filepath, indexSpec)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	corrupt := func(format string, args ...any) error { return indexSpec.Corruptf(filepath, format, args...) }

	var finder *Finder
	if header.Version == indexVersionV3 {
		var v3 payloadV3
		if err := file.Payload(&v3); err != nil {
			return nil, corrupt("%v", err)
		}
		finder = BuildIndex(v3.PostalCode)
		finder.legacy = true
	} else {
		var v4 payloadV4
		if err := file.Payload(&v4); err != nil {
			return nil, corrupt("%v", err)
		}
		finder = NewPostalCodeFinder()
		for country, cols := range v4.Countries {
			n := len(cols.Codes)
			if len(cols.Latitude) != n || len(cols.Longitude) != n || len(cols.Place) != n {
				return nil, corrupt("country %s has inconsistent column lengths", country)
			}
			t := &countryTable{codes: cols.Codes, entries: make([]entry, n)}
			for i := range t.entries {
				if i > 0 && cols.Codes[i-1] >= cols.Codes[i] {
					return nil, corrupt("country %s codes are not strictly sorted", country)
				}
				t.entries[i] = entry{Latitude: cols.Latitude[i], Longitude: cols.Longitude[i], PlaceName: cols.Place[i]}
			}
			finder.countries[country] = t
		}
	}
	if got := finder.Len(); header.Count != got {
		return nil, corrupt("payload holds %d postal code entries but the header recorded %d", got, header.Count)
	}
	stats := file.Stats()
	log.Printf("postal code index %s decoded (v%d): %d B -> %d B streamed (zstd+gob) in %s",
		filepath, header.Version, stats.FileBytes, stats.PayloadBytes, stats.Decode)
	return finder, nil
}

// LegacyFormat reports whether the finder was loaded from a legacy (v3) file
// that should be rewritten in the current format.
func (pcf *Finder) LegacyFormat() bool { return pcf.legacy }
