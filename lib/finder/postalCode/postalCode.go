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
	"sync"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/klauspost/compress/zstd"
)

// Serialized index file format (version 3):
//
//	gob(indexHeader{Magic: "CFPOSTIDX", Version: 3, Count: total entries})
//	zstd-frame(gob(Finder))  // only the exported PostalCode map is encoded
//
// The leading header lets a truncated or version-skewed file be rejected
// with a descriptive error instead of silently poisoning the finder with
// zero-filled data (gob zero-fills fields it does not find, so an index
// written before a PostalCodeEntry-struct change would otherwise load as
// garbage). The header stays uncompressed so version checks — including v2
// rejection — run before any decompression.
//
// Version history: v2 was a lockstep bump with the name index's City change
// (payload struct unchanged); v3 moves the same payload behind one zstd
// frame (the framing the name index v2 established: SpeedFastest, frame CRC,
// per-call encoder/decoder) — 98 MB raw compresses to ~40 MB. A v2 file
// fails the version check and returns ErrCorruptIndex exactly as truncated
// files do; the initializer rebuilds and rewrites it as v3. Writes go to
// filepath+".part" and are renamed into place only after a complete encode,
// so a crash mid-write never replaces a valid index with a truncated one.
const (
	indexMagic   = "CFPOSTIDX"
	indexVersion = uint32(3)
)

// postalIndexZstdLevel matches the name index v2 framing decision:
// SpeedFastest keeps the decode off the warm-start critical path.
const postalIndexZstdLevel = zstd.SpeedFastest

// postalIndexZstdCRC enables the per-frame checksum so corruption inside a
// structurally valid frame is detected deterministically by the decoder.
const postalIndexZstdCRC = true

// decodeZstdFrame decompresses one complete zstd frame with a per-call
// decoder that is closed immediately afterwards (a shared decoder retains
// its window/worker buffers; see the name index v2 measurement note).
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

// totalEntries counts the postal code records across all countries; this is
// the quantity the header Count records.
func totalEntries(postalCodes map[string]map[string]dataLoader.PostalCodeEntry) int {
	total := 0
	for _, byCountry := range postalCodes {
		total += len(byCountry)
	}
	return total
}

// Finder is a struct that contains the data for postal code lookups
type Finder struct {
	PostalCode map[string]map[string]dataLoader.PostalCodeEntry // Map of country code to postal code to entry
	mutex      sync.RWMutex                                     // Mutex for thread-safe operations
}

// NewPostalCodeFinder creates a new Finder instance
func NewPostalCodeFinder() *Finder {
	return &Finder{
		PostalCode: make(map[string]map[string]dataLoader.PostalCodeEntry),
	}
}

// AddPostalCode adds a postal code entry to the Finder
func (pcf *Finder) AddPostalCode(entry dataLoader.PostalCodeEntry) {
	pcf.mutex.Lock()
	defer pcf.mutex.Unlock()

	if _, exists := pcf.PostalCode[entry.CountryCode]; !exists {
		pcf.PostalCode[entry.CountryCode] = make(map[string]dataLoader.PostalCodeEntry)
	}
	pcf.PostalCode[entry.CountryCode][entry.PostalCode] = entry
}

// BuildIndex creates a postal code index from postal code data
func BuildIndex(postalCodes map[string]map[string]dataLoader.PostalCodeEntry) *Finder {
	finder := NewPostalCodeFinder()

	// Bulk loading - skip progress bar and individual mutex locks for better performance
	finder.mutex.Lock()
	defer finder.mutex.Unlock()

	// Direct assignment for bulk loading - much faster than individual AddPostalCode calls
	finder.PostalCode = postalCodes

	return finder
}

// CityByPostalCode finds the nearest city by postal code and country code
func (pcf *Finder) CityByPostalCode(postalCode, countryCode string) *city.City {
	pcf.mutex.RLock()
	defer pcf.mutex.RUnlock()

	if countryEntries, exists := pcf.PostalCode[countryCode]; exists {
		if entry, exists := countryEntries[postalCode]; exists {
			return &city.City{
				Latitude:  entry.Latitude,
				Longitude: entry.Longitude,
				Name:      entry.PlaceName,
				Country:   countryCode,
			}
		}
	}
	return nil
}

// SerializeIndex saves the postal code index to a file. The stream is
// uncompressed header, then one zstd frame holding the gob payload (see the
// format comment above), written atomically: the bytes land in
// filepath+".part" first and are renamed over filepath only after a complete
// encode, so readers never observe a half-written index. A fresh zstd
// encoder per call: encoders are not reusable, and serialization is a
// one-shot init-path operation.
func (pcf *Finder) SerializeIndex(filepath string) error {
	pcf.mutex.Lock()
	defer pcf.mutex.Unlock()

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

	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(indexHeader{
		Magic:   indexMagic,
		Version: indexVersion,
		Count:   totalEntries(pcf.PostalCode),
	}); err != nil {
		return fail(fmt.Errorf("failed to encode postal code index header: %w", err))
	}
	zw, err := zstd.NewWriter(file, zstd.WithEncoderLevel(postalIndexZstdLevel), zstd.WithEncoderCRC(postalIndexZstdCRC))
	if err != nil {
		return fail(fmt.Errorf("failed to create postal code index zstd writer: %w", err))
	}
	if err := gob.NewEncoder(zw).Encode(pcf); err != nil {
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

// DeserializeIndex loads the postal code index from a file. The header must
// be readable and compatible (magic and version, including the v2 rejection)
// before any decompression runs; every decode failure — bad header,
// truncated or corrupted zstd frame, malformed payload, or an entry count
// that disagrees with the header — wraps ErrCorruptIndex so the initializer
// can fall back to a rebuild. The read/zstd/gob split is logged, mirroring
// the name and S2 indexes, so warm-start time stays attributable.
func DeserializeIndex(filepath string) (*Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}

	corrupt := func(format string, args ...any) error {
		return fmt.Errorf("%w: index file %s appears truncated or from an incompatible version; delete %s or rebuild the index: "+format,
			append([]any{ErrCorruptIndex, filepath, filepath}, args...)...)
	}

	// The bufio.Reader is shared by the header gob decoder and the payload
	// read: gob consumes exactly the header's bytes, and whatever it buffered
	// past them belongs to the zstd frame.
	bufFile := bufio.NewReader(file)
	var header indexHeader
	if err := gob.NewDecoder(bufFile).Decode(&header); err != nil {
		_ = file.Close()
		return nil, corrupt("header decode: %v", err)
	}
	if header.Magic != indexMagic {
		_ = file.Close()
		return nil, corrupt("bad magic %q (want %q)", header.Magic, indexMagic)
	}
	if header.Version != indexVersion {
		_ = file.Close()
		return nil, corrupt("unsupported version %d (want %d)", header.Version, indexVersion)
	}

	readStart := time.Now()
	compressed, err := io.ReadAll(bufFile)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("error reading postal code index payload from %s: %w", filepath, err)
	}
	compressedLen := len(compressed)
	zstdStart := time.Now()
	payloadBytes, err := decodeZstdFrame(compressed)
	if err != nil {
		_ = file.Close()
		return nil, corrupt("payload is not a decodable zstd frame: %v", err)
	}
	zstdDone := time.Now()
	compressed = nil // release the compressed buffer before the gob decode allocates
	gobStart := zstdDone
	var finder Finder
	if err := gob.NewDecoder(bytes.NewReader(payloadBytes)).Decode(&finder); err != nil {
		_ = file.Close()
		return nil, corrupt("payload decode: %v", err)
	}
	if got := totalEntries(finder.PostalCode); header.Count != got {
		_ = file.Close()
		return nil, corrupt("payload holds %d postal code entries but the header recorded %d", got, header.Count)
	}
	gobDone := time.Now()

	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("error closing file: %w", err)
	}
	log.Printf("postal code index %s decoded: read %d B in %s, zstd %d->%d B in %s, gob %s",
		filepath, compressedLen, zstdStart.Sub(readStart), compressedLen, len(payloadBytes),
		zstdDone.Sub(zstdStart), gobDone.Sub(gobStart))

	return &finder, nil
}
