package coordinates

import (
	"errors"
	"fmt"
	"log"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/indexfile"
	"github.com/golang/geo/s2"
)

// SerializableS2Finder is the v3 on-disk payload: the city table plus the
// admin attribution arrays and code tables. Admin1IDs/Admin2IDs are parallel
// to Cities (-1 = absent); the code tables hold "CC.CODE" composite keys in
// first-encounter order.
type SerializableS2Finder struct {
	Cities      []city.City
	Admin1IDs   []int32
	Admin2IDs   []int32
	Admin1Codes []string
	Admin2Codes []string
}

// Serialized index file format (version 3):
//
//	gob(indexHeader{Magic: "CFS2IDX", Version: 3, Count: len(Cities)})  // raw
//	zstd-frame(gob(SerializableS2Finder))                               // one frame
//
// The leading header lets a truncated or version-skewed file be rejected
// with a descriptive error instead of silently poisoning the finder with
// zero-filled data (gob zero-fills fields it does not find, so an index
// written before a payload-struct change would otherwise load as garbage).
// The header stays uncompressed so version checks — including v2 rejection —
// run before any decompression.
//
// Version history: v2 embedded Population in City (bumped in lockstep with
// the name index's City change); v3 adds the admin attribution arrays and
// moves the payload behind one zstd frame (the same framing the name index
// v2 established, now owned by lib/indexfile: SpeedFastest, frame CRC). A v2
// file fails the version check below and returns ErrCorruptIndex exactly as
// truncated files do; the initializer's ensure*Index fallback rebuilds from
// the source datasets and rewrites the file as v3 — the proven v1→v2 path.
// Admin1Names is deliberately NOT in the payload: it is optional side data,
// re-attached per boot, so a names-file change never invalidates the index.
//
// Writes are atomic and durable (lib/indexfile.Write): a crash mid-write
// never replaces a valid index with a truncated one.
const (
	indexMagic   = "CFS2IDX"
	indexVersion = uint32(3)
)

// indexHeader is the first gob value of every serialized S2 index.
type indexHeader struct {
	Magic   string
	Version uint32
	Count   int
}

// payloadBounds relates the header's city count to the file, so a bomb is
// rejected before it inflates (see indexfile.EntryBounds). Measured on the
// gob encoding of one city plus its two admin ids:
//   - bytes per entry: 45 B for a typical row, ~1.1 KB with a 1 KiB name;
//     the bound is 2 KiB, since only the average over all cities counts.
//   - entries per file byte: the loader admits only rows with a name, a
//     country, and coordinates. Identical typical rows reach 104 per byte
//     at 8M cities; the degenerate minimum row (one-letter name, 0/0
//     coordinates, no population) reaches 498. A bomb of empty cities
//     (3 B per entry) reaches 1500+. The bound of 1000 sits twice above the
//     degenerate legitimate case and below the bomb.
//
// payload_bounds_test.go re-measures both.
var payloadBounds = indexfile.EntryBounds{MaxBytesPerEntry: 2 << 10, MaxEntriesPerFileByte: 1000}

// ErrCorruptIndex reports an index file that cannot be trusted: truncated,
// undecodable, or written by an incompatible format version. Detect it with
// errors.Is to decide whether a rebuild from source data is possible.
var ErrCorruptIndex = errors.New("s2 index file is corrupt or incompatible")

// SerializeIndex saves the finder's data to a file in the format described
// above, atomically and durably (lib/indexfile.Write).
func (f *S2Finder) SerializeIndex(filepath string) error {
	payload := SerializableS2Finder{
		Cities:      f.Cities,
		Admin1IDs:   f.Admin1IDs,
		Admin2IDs:   f.Admin2IDs,
		Admin1Codes: f.Admin1Codes,
		Admin2Codes: f.Admin2Codes,
	}

	header := indexHeader{Magic: indexMagic, Version: indexVersion, Count: len(f.Cities)}
	if err := indexfile.Write(filepath, header, &payload); err != nil {
		return fmt.Errorf("failed to write s2 index: %w", err)
	}
	return nil
}

// DeserializeIndex loads the finder's data from a file. The header must be
// readable and compatible (magic and version, including the v2 rejection)
// before any decompression runs; every decode failure — bad header,
// truncated or corrupted zstd frame, malformed payload, or counts that
// disagree with the header — wraps ErrCorruptIndex so the initializer can
// fall back to a rebuild (an open failure is environmental and returned
// unwrapped). The payload streams from the file through zstd into gob
// (lib/indexfile), so no whole-file buffer is held next to the decoded
// cities.
func DeserializeIndex(filepath string) (*S2Finder, error) {
	file, err := indexfile.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	corrupt := func(format string, args ...any) error {
		return fmt.Errorf("%w: index file %s appears truncated or from an incompatible version; delete %s or rebuild the index: "+format,
			append([]any{ErrCorruptIndex, filepath, filepath}, args...)...)
	}

	var header indexHeader
	if err := file.Header(&header); err != nil {
		return nil, corrupt("%v", err)
	}
	if header.Magic != indexMagic {
		return nil, corrupt("bad magic %q (want %q)", header.Magic, indexMagic)
	}
	if header.Version != indexVersion {
		return nil, corrupt("unsupported version %d (want %d)", header.Version, indexVersion)
	}

	if err := file.BoundByEntries(header.Count, payloadBounds); err != nil {
		return nil, corrupt("%v", err)
	}
	var payload SerializableS2Finder
	if err := file.Payload(&payload); err != nil {
		return nil, corrupt("%v", err)
	}
	if header.Count != len(payload.Cities) {
		return nil, corrupt("payload holds %d cities but the header recorded %d", len(payload.Cities), header.Count)
	}
	if len(payload.Admin1IDs) != len(payload.Cities) || len(payload.Admin2IDs) != len(payload.Cities) {
		return nil, corrupt("admin id arrays hold %d/%d entries for %d cities", len(payload.Admin1IDs), len(payload.Admin2IDs), len(payload.Cities))
	}
	stats := file.Stats()
	log.Printf("s2 index %s decoded: %d B -> %d B streamed (zstd+gob) in %s",
		filepath, stats.FileBytes, stats.PayloadBytes, stats.Decode)

	// gob allocates a fresh backing for every decoded string: collapse the
	// per-city copies of the ~250 country codes to one each.
	city.InternCountries(payload.Cities)

	// Rebuild index efficiently without progress bar overhead. The same pass
	// bounds-checks every admin id: a structurally valid frame can still
	// carry ids that point outside the code tables, and such a file must be
	// rejected rather than surfaced as a wrong-code query answer.
	points := make(s2.PointVector, len(payload.Cities))
	for i, c := range payload.Cities {
		points[i] = s2.PointFromLatLng(s2.LatLngFromDegrees(c.Latitude, c.Longitude))
		if id := payload.Admin1IDs[i]; id >= 0 && int(id) >= len(payload.Admin1Codes) {
			return nil, corrupt("admin1 id %d at city %d outside the %d-entry table", id, i, len(payload.Admin1Codes))
		}
		if id := payload.Admin2IDs[i]; id >= 0 && int(id) >= len(payload.Admin2Codes) {
			return nil, corrupt("admin2 id %d at city %d outside the %d-entry table", id, i, len(payload.Admin2Codes))
		}
	}

	index := s2.NewShapeIndex()
	index.Add(&points)
	// Build eagerly so the first query does not pay the construction cost.
	index.Build()

	return &S2Finder{
		Index:          index,
		Cities:         payload.Cities,
		Admin1IDs:      payload.Admin1IDs,
		Admin2IDs:      payload.Admin2IDs,
		Admin1Codes:    payload.Admin1Codes,
		Admin2Codes:    payload.Admin2Codes,
		maxPopulation:  maxPopulationOf(payload.Cities),
		topPopulations: topPopulationsOf(payload.Cities),
	}, nil
}
