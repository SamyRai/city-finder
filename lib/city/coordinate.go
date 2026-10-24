package city

import "math"

// ValidCoordinate reports whether lat/lon is a usable point: finite, with
// |lat| <= 90 and |lon| <= 180 — the same bounds the HTTP layer enforces on
// query input. strconv.ParseFloat accepts "NaN", "Inf" and 999, so parsing
// alone does not keep them out of the indexes. NaN fails both comparisons.
//
// It lives here, beside the City record, so the loader (rejecting rows as
// they are read), coordinates.BuildIndex and coordinates.DeserializeIndex
// (rejecting rows that reached an index some other way, or were written
// before the loader checked) all apply the one rule.
func ValidCoordinate(lat, lon float64) bool {
	return math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}
