package postalCode

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/klauspost/compress/zstd"
)

// Serialized index file format:
//
//	gob(indexHeader{Magic: "CFPOSTIDX", Version, Count: total entries})
//	zstd-frame(gob(payload))
//
// The header stays uncompressed so version checks run before any
// decompression; a truncated or version-skewed file is rejected with
// ErrCorruptIndex instead of loading as zero-filled garbage. Writes go to
// filepath+".part" and are renamed into place only after a complete encode.
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

// postalIndexZstdLevel: SpeedFastest keeps encode cheap; decompression speed
// is nearly level-independent.
const postalIndexZstdLevel = zstd.SpeedFastest

// postalIndexZstdCRC enables the per-frame checksum so corruption inside a
// structurally valid frame is detected deterministically by the decoder.
const postalIndexZstdCRC = true

// decodeZstdFrame decompresses one complete zstd frame with a per-call
// decoder that is closed immediately afterwards (a shared decoder retains
// its window/worker buffers).
func decodeZstdFrame(compressed []byte) ([]byte, error) {
	zd, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer zd.Close()
	return zd.DecodeAll(compressed, nil)
}

// indexHeader is the first gob value of every serialized postal code index.
type indexHeader struct {
	Magic   string
	Version uint32
	Count   int
}

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

	partPath := filepath + ".part"
	file, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("failed to create index file: %w", err)
	}
	fail := func(err error) error {
		_ = file.Close()
		_ = os.Remove(partPath)
		return err
	}
	if err := gob.NewEncoder(file).Encode(indexHeader{Magic: indexMagic, Version: indexVersion, Count: total}); err != nil {
		return fail(fmt.Errorf("failed to encode postal code index header: %w", err))
	}
	zw, err := zstd.NewWriter(file, zstd.WithEncoderLevel(postalIndexZstdLevel), zstd.WithEncoderCRC(postalIndexZstdCRC))
	if err != nil {
		return fail(fmt.Errorf("failed to create postal code index zstd writer: %w", err))
	}
	if err := gob.NewEncoder(zw).Encode(&payload); err != nil {
		_ = zw.Close()
		return fail(fmt.Errorf("failed to encode postal code index payload: %w", err))
	}
	if err := zw.Close(); err != nil {
		return fail(fmt.Errorf("failed to finalize postal code index zstd frame: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to close index file: %w", err)
	}
	if err := os.Rename(partPath, filepath); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to move %s to %s: %w", partPath, filepath, err)
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
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	corrupt := func(format string, args ...any) error {
		return fmt.Errorf("%w: index file %s appears truncated or from an incompatible version; delete %s or rebuild the index: "+format,
			append([]any{ErrCorruptIndex, filepath, filepath}, args...)...)
	}

	bufFile := bufio.NewReader(file)
	var header indexHeader
	if err := gob.NewDecoder(bufFile).Decode(&header); err != nil {
		return nil, corrupt("header decode: %v", err)
	}
	if header.Magic != indexMagic {
		return nil, corrupt("bad magic %q (want %q)", header.Magic, indexMagic)
	}
	if header.Version != indexVersion && header.Version != indexVersionV3 {
		return nil, corrupt("unsupported version %d (want %d or %d)", header.Version, indexVersion, indexVersionV3)
	}

	readStart := time.Now()
	compressed, err := io.ReadAll(bufFile)
	if err != nil {
		return nil, fmt.Errorf("error reading postal code index payload from %s: %w", filepath, err)
	}
	compressedLen := len(compressed)
	zstdStart := time.Now()
	payloadBytes, err := decodeZstdFrame(compressed)
	if err != nil {
		return nil, corrupt("payload is not a decodable zstd frame: %v", err)
	}
	compressed = nil // release the compressed buffer before the gob decode allocates
	zstdDone := time.Now()

	var finder *Finder
	if header.Version == indexVersionV3 {
		var v3 payloadV3
		if err := gob.NewDecoder(bytes.NewReader(payloadBytes)).Decode(&v3); err != nil {
			return nil, corrupt("payload decode: %v", err)
		}
		finder = BuildIndex(v3.PostalCode)
		finder.legacy = true
	} else {
		var v4 payloadV4
		if err := gob.NewDecoder(bytes.NewReader(payloadBytes)).Decode(&v4); err != nil {
			return nil, corrupt("payload decode: %v", err)
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
	gobDone := time.Now()
	if got := finder.Len(); header.Count != got {
		return nil, corrupt("payload holds %d postal code entries but the header recorded %d", got, header.Count)
	}
	log.Printf("postal code index %s decoded (v%d): read %d B in %s, zstd %d->%d B in %s, gob %s",
		filepath, header.Version, compressedLen, zstdStart.Sub(readStart), compressedLen, len(payloadBytes),
		zstdDone.Sub(zstdStart), gobDone.Sub(zstdDone))
	return finder, nil
}

// LegacyFormat reports whether the finder was loaded from a legacy (v3) file
// that should be rewritten in the current format.
func (pcf *Finder) LegacyFormat() bool { return pcf.legacy }
