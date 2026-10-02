package city

import (
	"encoding/binary"
	"hash/crc64"
	"math"
)

var fingerprintTable = crc64.MakeTable(crc64.ECMA)

// Fingerprint returns a stable 64-bit checksum over every field of every
// city, in order. Indexes that reference a city table owned by another index
// (by row number, instead of embedding a copy) store it, and verify it before
// attaching to a table, so a mismatched pair of index files is detected
// instead of silently resolving ids to the wrong cities. It is a corruption
// and mismatch check, not a cryptographic one.
func Fingerprint(cities []City) uint64 {
	h := crc64.New(fingerprintTable)
	var buf [8]byte
	writeString := func(s string) {
		binary.LittleEndian.PutUint32(buf[:4], uint32(len(s)))
		_, _ = h.Write(buf[:4])
		_, _ = h.Write([]byte(s))
	}
	for i := range cities {
		c := &cities[i]
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(c.Latitude))
		_, _ = h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(c.Longitude))
		_, _ = h.Write(buf[:])
		binary.LittleEndian.PutUint32(buf[:4], uint32(c.Population))
		_, _ = h.Write(buf[:4])
		writeString(c.Name)
		writeString(c.Country)
	}
	return h.Sum64()
}
