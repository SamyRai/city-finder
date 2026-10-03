package indexfile

import (
	"fmt"
	"slices"
)

// Header is the first gob value of every index file. It lets a loader reject
// a file written by an incompatible build (magic or version mismatch) up
// front, before any decompression, instead of failing halfway through a
// half-understood payload.
//
// The field names and types ARE the wire format: gob matches struct fields
// by name, so renaming or retyping one invalidates every stored index.
// Count is the number of entries the index declares (cities, postal codes,
// countries: the caller's unit).
type Header struct {
	Magic   string
	Version uint32
	Count   int
}

// Spec describes one kind of index file for OpenIndex.
type Spec struct {
	// Magic identifies the kind of index.
	Magic string
	// Versions lists every format version the loader still reads.
	Versions []uint32
	// Corrupt is the kind's own ErrCorruptIndex sentinel; every framing or
	// header failure wraps it so callers can tell rebuildable corruption
	// from environmental errors.
	Corrupt error
	// Bounds, when set, checks the header's Count against the file size and
	// tightens the payload budget (see Reader.BoundByEntries).
	Bounds *EntryBounds
}

// Corruptf builds the error for an unusable index file at path: it wraps
// the kind's sentinel, names the file and tells the operator how to recover.
func (s Spec) Corruptf(path, format string, args ...any) error {
	return fmt.Errorf("%w: index file %s appears truncated or from an incompatible version; delete it so the index is rebuilt: "+format,
		append([]any{s.Corrupt, path}, args...)...)
}

// OpenIndex opens path, reads its header and checks magic, version and (when
// the spec carries them) entry bounds, all before any decompression runs.
// An open failure is environmental and returned as is; every other failure
// wraps spec.Corrupt. On success the caller owns the Reader and decodes the
// payload next.
func OpenIndex(path string, spec Spec) (*Reader, Header, error) {
	r, err := Open(path)
	if err != nil {
		return nil, Header{}, err
	}
	var h Header
	if err := checkHeader(r, &h, spec); err != nil {
		_ = r.Close()
		return nil, Header{}, spec.Corruptf(path, "%v", err)
	}
	return r, h, nil
}

// checkHeader is OpenIndex's validation, returning the bare reason.
func checkHeader(r *Reader, h *Header, spec Spec) error {
	if err := r.Header(h); err != nil {
		return fmt.Errorf("not a readable versioned header (legacy or corrupt file): %w", err)
	}
	if h.Magic != spec.Magic {
		return fmt.Errorf("bad magic %q (want %q)", h.Magic, spec.Magic)
	}
	if !slices.Contains(spec.Versions, h.Version) {
		return fmt.Errorf("unsupported version %d (want one of %v)", h.Version, spec.Versions)
	}
	if spec.Bounds != nil {
		return r.BoundByEntries(h.Count, *spec.Bounds)
	}
	return nil
}
