package city

import (
	"math"
)

// City struct optimized for memory alignment (Go 1.22+ best practice)
// Field ordering: largest types first (float64 = 8 bytes), then fixed-width
// scalars (int32 = 4 bytes), then strings (16 bytes); this keeps padding at
// one 4-byte hole after Population.
// AltNames removed from City struct for memory efficiency - processed during building only
//
// Population carries `json:"-"` deliberately: phase 1 of the v2 index-format
// sprint must not change the HTTP API surface (routes embed city.City
// directly and its field set IS the wire contract). Exposing population in
// responses is a routing/API decision owned by the WEIGHTED lane /
// integration, not this format hop.
type City struct {
	Latitude   float64 // 8 bytes - aligned to 8-byte boundary
	Longitude  float64 // 8 bytes - aligned to 8-byte boundary
	Population int32   `json:"-"` // 4 bytes - GeoNames allCountries field 14; 0 when absent
	Name       string  // 16 bytes (string header: ptr + len) - aligned to 8-byte boundary
	Country    string  // 16 bytes (string header: ptr + len) - aligned to 8-byte boundary
}

// SpatialCity includes AltNames for building process but embeds compact City
type SpatialCity struct {
	City
	AltNames   []string // Used during index building, not stored in final City
	Admin1Code string   // GeoNames allCountries field 10 (0-indexed); build-only (see below)
	Admin2Code string   // GeoNames allCountries field 11 (0-indexed); build-only (see below)
}

// Admin1Code/Admin2Code carry the per-country administrative-division codes
// of a row ("CA" for US-CA, "02" for AD-02) from the loader to the S2 index
// build, exactly like AltNames: attribution lives only where it is served (the
// S2 index's admin id arrays + code tables), so city.City — and with it the
// name index and the HTTP wire contract — stays untouched (v1.1 design note).
// Empty string = the row carries no code; the index build records -1.

// HaversineDistance calculates the distance between two geographical points in kilometers
func HaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0 // Earth's radius in kilometers

	dLat := toRadians(lat2 - lat1)
	dLon := toRadians(lon2 - lon1)

	a := sin(dLat/2)*sin(dLat/2) +
		cos(toRadians(lat1))*cos(toRadians(lat2))*
			sin(dLon/2)*sin(dLon/2)
	c := 2 * atan2(sqrt(a), sqrt(1-a))

	return R * c
}

// Helper functions for HaversineDistance
func toRadians(deg float64) float64 {
	return deg * (math.Pi / 180.0)
}

func sin(x float64) float64      { return math.Sin(x) }
func cos(x float64) float64      { return math.Cos(x) }
func sqrt(x float64) float64     { return math.Sqrt(x) }
func atan2(y, x float64) float64 { return math.Atan2(y, x) }
