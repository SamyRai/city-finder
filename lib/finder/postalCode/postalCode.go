package postalCode

import (
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// Serialized index file format (version 2):
//
//	gob(indexHeader{Magic: "CFPOSTIDX", Version: 2, Count: total entries})
//	gob(Finder)  // only the exported PostalCode map is encoded
//
// The leading header lets a truncated or version-skewed file be rejected
// with a descriptive error instead of silently poisoning the finder with
// zero-filled data (gob zero-fills fields it does not find, so an index
// written before a PostalCodeEntry-struct change would otherwise load as
// garbage). The payload struct is unchanged from v1 (PostalCodeEntry embeds
// no City); the version bump is lockstep consistency with the name and S2
// indexes — all three regenerate together from the same source on first v2
// boot and there is a single version story in logs and docs. Writes go to
// filepath+".part" and are renamed into place only after a complete encode,
// so a crash mid-write never replaces a valid index with a truncated one.
const (
	indexMagic   = "CFPOSTIDX"
	indexVersion = uint32(2)
)

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

// decodeIndexFile decodes and validates the header+payload stream of a
// postal code index. Any failure means the file must not be used.
func decodeIndexFile(decoder *gob.Decoder, header *indexHeader, payload *Finder) error {
	if err := decoder.Decode(header); err != nil {
		return err
	}
	if header.Magic != indexMagic {
		return fmt.Errorf("bad magic %q (want %q)", header.Magic, indexMagic)
	}
	if header.Version != indexVersion {
		return fmt.Errorf("unsupported version %d (want %d)", header.Version, indexVersion)
	}
	if err := decoder.Decode(payload); err != nil {
		return err
	}
	if got := totalEntries(payload.PostalCode); header.Count != got {
		return fmt.Errorf("payload holds %d postal code entries but the header recorded %d", got, header.Count)
	}
	return nil
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
// header-then-payload (see indexHeader) and is written atomically: the bytes
// land in filepath+".part" first and are renamed over filepath only after a
// complete encode, so readers never observe a half-written index.
func (pcf *Finder) SerializeIndex(filepath string) error {
	pcf.mutex.Lock()
	defer pcf.mutex.Unlock()

	partPath := filepath + ".part"
	file, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("failed to create index file: %w", err)
	}

	encoder := gob.NewEncoder(file)
	encodeErr := encoder.Encode(indexHeader{
		Magic:   indexMagic,
		Version: indexVersion,
		Count:   totalEntries(pcf.PostalCode),
	})
	if encodeErr == nil {
		encodeErr = encoder.Encode(pcf)
	}
	closeErr := file.Close()

	if encodeErr != nil {
		_ = os.Remove(partPath) // never leave a partial index behind
		return fmt.Errorf("failed to encode postal code index: %w", encodeErr)
	}
	if closeErr != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to close index file: %w", closeErr)
	}
	if err := os.Rename(partPath, filepath); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to move %s to %s: %w", partPath, filepath, err)
	}
	return nil
}

// DeserializeIndex loads the postal code index from a file.
func DeserializeIndex(filepath string) (*Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}

	var header indexHeader
	var finder Finder
	decodeErr := decodeIndexFile(gob.NewDecoder(file), &header, &finder)
	closeErr := file.Close()

	if decodeErr != nil {
		return nil, fmt.Errorf("%w: index file %s appears truncated or from an incompatible version; delete %s or rebuild the index: %v",
			ErrCorruptIndex, filepath, filepath, decodeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("error closing file: %w", closeErr)
	}

	return &finder, nil
}
