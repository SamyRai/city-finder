package city

import (
	"math"
)

// City struct optimized for memory alignment (Go 1.22+ best practice)
// Field ordering: largest types first (float64 = 8 bytes), then pointers/slices (8 bytes), then strings (16 bytes)
// This reduces padding and improves cache locality
// AltNames removed from City struct for memory efficiency - processed during building only
type City struct {
	Latitude  float64 // 8 bytes - aligned to 8-byte boundary
	Longitude float64 // 8 bytes - aligned to 8-byte boundary
	Name      string  // 16 bytes (string header: ptr + len) - aligned to 8-byte boundary
	Country   string  // 16 bytes (string header: ptr + len) - aligned to 8-byte boundary
}

// SpatialCity includes AltNames for building process but embeds compact City
type SpatialCity struct {
	City
	AltNames []string // Used during index building, not stored in final City
}

func EuclideanDistance(p1, p2 []float64) float64 {
	sum := 0.0
	for i := 0; i < len(p1); i++ {
		diff := p1[i] - p2[i]
		sum += diff * diff
	}
	return math.Sqrt(sum)
}

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
