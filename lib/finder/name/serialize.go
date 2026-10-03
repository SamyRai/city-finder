package name

import (
	"errors"
	"fmt"
	"log"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/indexfile"
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

// The file framing (uncompressed gob header, then one CRC-checked zstd frame
// holding the gob payload), atomic durable writes and streaming reads belong
// to lib/indexfile. A truncated or corrupted frame decodes to ErrCorruptIndex
// exactly like a malformed gob stream.

// SerializeIndex saves the name index to a file (format v3). A shared city
// table (ShareCities) is referenced, not embedded; an owned one is embedded.
// The write is atomic and durable (lib/indexfile.Write), so a crash mid-write
// can never leave a truncated file where the index used to be.
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

	header := indexHeader{Magic: nameIndexMagic, Version: nameIndexVersion, Count: len(payload.Refs)}
	return indexfile.Write(filepath, &header, &payload)
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
	file, err := indexfile.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var header indexHeader
	if err := file.Header(&header); err != nil {
		return nil, fmt.Errorf("%w: name index %s is not a readable versioned index (legacy or corrupt file: %v); delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, err)
	}
	if header.Magic != nameIndexMagic || (header.Version != nameIndexVersion && header.Version != nameIndexVersionV2) {
		return nil, fmt.Errorf("%w: name index %s format mismatch: got magic %q version %d, want magic %q version %d or %d; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, header.Magic, header.Version, nameIndexMagic, nameIndexVersion, nameIndexVersionV2)
	}

	var payload nameIndexPayloadV3
	if header.Version == nameIndexVersionV2 {
		var v2 nameIndexPayloadV2
		if err := file.Payload(&v2); err != nil {
			return nil, fmt.Errorf("%w: decoding name index payload from %s: %v", ErrCorruptIndex, filepath, err)
		}
		// v2 ids number a self-contained distinct-city table: it is exactly
		// an embedded v3 base table.
		payload = nameIndexPayloadV3{CityCount: len(v2.Cities), Cities: v2.Cities, Refs: v2.Refs}
	} else if err := file.Payload(&payload); err != nil {
		return nil, fmt.Errorf("%w: decoding name index payload from %s: %v", ErrCorruptIndex, filepath, err)
	}
	if header.Count != len(payload.Refs) {
		return nil, fmt.Errorf("%w: name index %s payload holds %d countries but the header recorded %d; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, len(payload.Refs), header.Count)
	}
	if payload.Cities != nil && len(payload.Cities) != payload.CityCount {
		return nil, fmt.Errorf("%w: name index %s embeds %d cities but records %d; delete the file so the index is rebuilt",
			ErrCorruptIndex, filepath, len(payload.Cities), payload.CityCount)
	}
	stats := file.Stats()
	log.Printf("name index %s decoded (v%d): %d B -> %d B streamed (zstd+gob) in %s",
		filepath, header.Version, stats.FileBytes, stats.PayloadBytes, stats.Decode)

	finder := NewNameFinder()
	finder.cities = cityTable{base: payload.Cities, baseCount: payload.CityCount, fingerprint: payload.Fingerprint}
	if payload.Cities != nil {
		// gob allocates a fresh backing for every decoded string; one intern
		// pass collapses the country codes to one backing per country.
		city.InternCountries(payload.Cities)
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
