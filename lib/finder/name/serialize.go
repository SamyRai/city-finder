package name

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

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/klauspost/compress/zstd"
)

// indexHeader is the first value written into the serialized stream. It lets
// DeserializeIndex reject files written by an incompatible build (magic or
// version mismatch) up front, with an error that tells the caller to rebuild
// instead of failing halfway through a half-understood payload.
type indexHeader struct {
	Magic   string
	Version uint32
	Count   int
}

const (
	// nameIndexMagic identifies name index files.
	nameIndexMagic = "CFNAMEIDX"

	// nameIndexVersionV2 is the previous format, still READ (never written):
	// a distinct-city table numbered by first encounter plus int32 refs.
	// (v2 itself replaced v1's gob-encoded map[string]map[string][]*city.City,
	// which duplicated every City per reference: 1.6 GB and ~54 s of warm
	// start at Oct-2026 GeoNames scale. A v1 file fails the version check with
	// ErrCorruptIndex and is rebuilt from source.)
	nameIndexVersionV2 = uint32(2)

	// nameIndexVersion is the current format, v3: ids are input ROW numbers,
	// and the city table is either embedded (a standalone index) or, for an
	// index whose table is shared with the S2 index (ShareCities), only
	// referenced by row count and city.Fingerprint. The referenced variant
	// drops the ~546 MB (raw, prod scale) city table that v2 duplicated from
	// the S2 index file.
	nameIndexVersion = uint32(3)
)

// ErrCorruptIndex reports an index file that cannot be trusted: truncated,
// undecodable, or written by an incompatible format version. The initializer
// treats it (and only it) as rebuildable; any other error — a wrapped fs
// error from an unreadable file, for example — is fatal.
var ErrCorruptIndex = errors.New("name index file is corrupt or incompatible")

// nameIndexPayloadV2 is the v2 payload, kept for reading existing files:
// Cities[i] is city id i; Refs maps country -> name -> ids into Cities.
type nameIndexPayloadV2 struct {
	Cities []city.City
	Refs   map[string]map[string][]int32
}

// nameIndexPayloadV3 is the v3 payload.
//
// Ids in Refs are row numbers: id < CityCount is row id of the base table,
// id >= CityCount is Extra[id-CityCount] (cities added after the build). Cities
// is the embedded base table (len == CityCount) or nil when the base table is
// external (shared with the S2 index); Fingerprint is city.Fingerprint of the
// base table either way, so an external reference can be verified on attach.
// Per name, ids are in load order: sorted-table ids first, then overflow ids.
type nameIndexPayloadV3 struct {
	CityCount   int
	Fingerprint uint64
	Cities      []city.City
	Extra       []city.City
	Refs        map[string]map[string][]int32
}

// The payload stream is zstd-framed:
//
//	gob(indexHeader)               // uncompressed, so version checks happen
//	                               // before any decompression
//	zstd-frame(gob(payload))       // one frame, CRC on
//
// A truncated or corrupted frame, or a payload that is not a valid frame,
// decodes to ErrCorruptIndex exactly like a malformed raw gob stream.
const (
	// nameIndexZstdLevel trades compression ratio for encode speed;
	// decompression speed is nearly level-independent.
	nameIndexZstdLevel = zstd.SpeedFastest

	// nameIndexZstdCRC enables the per-frame checksum: corruption inside an
	// otherwise structurally valid frame is detected deterministically.
	nameIndexZstdCRC = true
)

// decodeZstdFrame decompresses one complete zstd frame with a per-call
// decoder that is closed immediately afterwards. A shared package-level
// decoder was measured to retain ~900 MB of internal window/worker buffers
// after decoding the prod-scale frame; warm start is a once-per-boot
// operation, so paying decoder setup (µs) to release that memory is strictly
// better.
func decodeZstdFrame(compressed []byte) ([]byte, error) {
	zd, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer zd.Close()
	return zd.DecodeAll(compressed, nil)
}

// SerializeIndex saves the name index to a file (format v3). A shared city
// table (ShareCities) is referenced, not embedded; an owned one is embedded.
// The payload is written to a sibling ".part" file first and moved into place
// with os.Rename only after the full stream has been written, so a crash
// mid-write can never leave a truncated file where the index used to be.
//
// The fuzzy n-gram index is runtime state and is NOT written. Map iteration
// order makes the file bytes non-deterministic — acceptable, indexes are
// regenerable artifacts gated by count validation and round-trip tests.
func (nf *Finder) SerializeIndex(filepath string) error {
	nf.mutex.Lock()
	payload, err := nf.buildPayloadV3Locked()
	nf.mutex.Unlock()
	if err != nil {
		return err
	}

	partPath := filepath + ".part"
	file, err := os.Create(partPath)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = file.Close()
		_ = os.Remove(partPath)
		return err
	}
	header := indexHeader{Magic: nameIndexMagic, Version: nameIndexVersion, Count: len(payload.Refs)}
	if err := gob.NewEncoder(file).Encode(&header); err != nil {
		return fail(err)
	}
	zw, err := zstd.NewWriter(file, zstd.WithEncoderLevel(nameIndexZstdLevel), zstd.WithEncoderCRC(nameIndexZstdCRC))
	if err != nil {
		return fail(err)
	}
	if err := gob.NewEncoder(zw).Encode(&payload); err != nil {
		_ = zw.Close()
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(partPath)
		return err
	}
	if err := os.Rename(partPath, filepath); err != nil {
		_ = os.Remove(partPath)
		return err
	}
	return nil
}

// buildPayloadV3Locked reduces the flat tables plus the overflow to the v3
// payload. The caller must hold nf.mutex. A country present in both the
// sorted tables and the overflow serializes as one Refs map whose per-name id
// lists carry the sorted-table ids first, then the overflow ids.
func (nf *Finder) buildPayloadV3Locked() (nameIndexPayloadV3, error) {
	t := &nf.cities
	if t.base == nil && t.baseCount > 0 {
		return nameIndexPayloadV3{}, fmt.Errorf("name index is detached from its city table; call ShareCities before SerializeIndex")
	}
	payload := nameIndexPayloadV3{
		CityCount:   t.baseCount,
		Fingerprint: city.Fingerprint(t.base),
		Extra:       make([]city.City, len(t.extra)),
		Refs:        make(map[string]map[string][]int32, len(nf.countries)+len(nf.overflow)),
	}
	if !t.shared {
		payload.Cities = t.base
	}
	for i, c := range t.extra {
		payload.Extra[i] = *c
	}
	for country, table := range nf.countries {
		refs := make(map[string][]int32, len(table.names)+len(nf.overflow[country]))
		for i, name := range table.names {
			refs[name] = append([]int32(nil), table.ids[table.starts[i]:table.starts[i+1]]...)
		}
		for name, ids := range nf.overflow[country] {
			refs[name] = append(refs[name], ids...)
		}
		payload.Refs[country] = refs
	}
	for country, countryOverflow := range nf.overflow {
		if _, ok := nf.countries[country]; ok {
			continue // serialized together with its sorted table above
		}
		refs := make(map[string][]int32, len(countryOverflow))
		for name, ids := range countryOverflow {
			refs[name] = append([]int32(nil), ids...)
		}
		payload.Refs[country] = refs
	}
	return payload, nil
}

// DeserializeIndex loads a name index file (format v3, or the legacy v2).
//
// The header must be readable and compatible (magic and version) before any
// decompression runs. Every decode failure — bad header, unsupported version,
// truncated or corrupted zstd frame, malformed payload, out-of-range id —
// wraps ErrCorruptIndex so callers can distinguish rebuildable corruption from
// environmental errors (open/close and read failures are returned unwrapped).
//
// A v3 file that references an external city table yields a DETACHED finder:
// its lookups return nil until ShareCities attaches the matching table (the
// initializer does this with the S2 index's Cities). Files that embed their
// table (v2, standalone v3) are fully usable as returned.
func DeserializeIndex(filepath string) (*Finder, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// The bufio.Reader is shared by the header gob decoder and the payload
	// read: gob consumes exactly the header's bytes, and whatever it buffered
	// past them belongs to the zstd frame.
	bufFile := bufio.NewReader(file)
	var header indexHeader
	if err := gob.NewDecoder(bufFile).Decode(&header); err != nil {
		return nil, fmt.Errorf("%w: name index %s is not a readable versioned index (legacy or corrupt file: %v); delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, err)
	}
	if header.Magic != nameIndexMagic || (header.Version != nameIndexVersion && header.Version != nameIndexVersionV2) {
		return nil, fmt.Errorf("%w: name index %s format mismatch: got magic %q version %d, want magic %q version %d or %d; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, header.Magic, header.Version, nameIndexMagic, nameIndexVersion, nameIndexVersionV2)
	}

	readStart := time.Now()
	compressed, err := io.ReadAll(bufFile)
	if err != nil {
		return nil, fmt.Errorf("reading name index payload from %s: %w", filepath, err)
	}
	compressedLen := len(compressed)
	zstdStart := time.Now()
	payloadBytes, err := decodeZstdFrame(compressed)
	if err != nil {
		return nil, fmt.Errorf("%w: name index %s payload is not a decodable zstd frame: %v; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, err)
	}
	compressed = nil // release the compressed buffer before the gob decode allocates
	zstdDone := time.Now()

	var payload nameIndexPayloadV3
	if header.Version == nameIndexVersionV2 {
		var v2 nameIndexPayloadV2
		if err := gob.NewDecoder(bytes.NewReader(payloadBytes)).Decode(&v2); err != nil {
			return nil, fmt.Errorf("%w: decoding name index payload from %s: %v", ErrCorruptIndex, filepath, err)
		}
		// v2 ids number a self-contained distinct-city table: it is exactly
		// an embedded v3 base table.
		payload = nameIndexPayloadV3{CityCount: len(v2.Cities), Cities: v2.Cities, Refs: v2.Refs}
	} else if err := gob.NewDecoder(bytes.NewReader(payloadBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: decoding name index payload from %s: %v", ErrCorruptIndex, filepath, err)
	}
	gobDone := time.Now()
	if header.Count != len(payload.Refs) {
		return nil, fmt.Errorf("%w: name index %s payload holds %d countries but the header recorded %d; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, len(payload.Refs), header.Count)
	}
	if payload.Cities != nil && len(payload.Cities) != payload.CityCount {
		return nil, fmt.Errorf("%w: name index %s embeds %d cities but records %d; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, len(payload.Cities), payload.CityCount)
	}
	log.Printf("name index %s decoded (v%d): read %d B in %s, zstd %d->%d B in %s, gob %s",
		filepath, header.Version, compressedLen, zstdStart.Sub(readStart), compressedLen, len(payloadBytes),
		zstdDone.Sub(zstdStart), gobDone.Sub(zstdDone))

	finder := NewNameFinder()
	finder.cities = cityTable{base: payload.Cities, baseCount: payload.CityCount, fingerprint: payload.Fingerprint}
	if payload.Cities != nil {
		// gob allocates a fresh backing for every decoded string, so each
		// City carries its own copy of a country code shared by millions of
		// cities; one intern pass collapses them to one backing per country.
		internDecodedCountries(payload.Cities)
	}
	for i := range payload.Extra {
		finder.cities.add(payload.Extra[i])
	}
	for country, refs := range payload.Refs {
		for _, ids := range refs {
			for _, id := range ids {
				if id < 0 || int(id) >= finder.cities.size() {
					return nil, fmt.Errorf("%w: name index %s references id %d outside the %d-city table; delete the file so the index is rebuilt",
						ErrCorruptIndex, filepath, id, finder.cities.size())
				}
			}
		}
		finder.countries[country] = buildTable(refs)
	}
	return finder, nil
}

// internDecodedCountries interns the Country field of every decoded city.
// gob transmits each string occurrence with its own backing array, so the
// table carries ~13.47M copies of ~250 country codes; the pass collapses them
// to one shared backing per country. It must run before the finder is shared.
func internDecodedCountries(cities []city.City) {
	for i := range cities {
		cities[i].Country = internString(cities[i].Country)
	}
}
