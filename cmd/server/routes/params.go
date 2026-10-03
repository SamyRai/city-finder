package routes

import (
	"math"
	"strconv"

	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
)

// parseCoordinate parses and validates a lat/lon query parameter. It rejects
// values that are not finite numbers: NaN passes strconv.ParseFloat but fails
// every range comparison, and ±Inf must not leak into the spatial index.
func parseCoordinate(raw string) (float64, bool) {
	// No log on a parse failure: the 400 is already visible in metrics and
	// the access log, and echoing client-supplied input into the log on
	// every bad request was an unauthenticated log-amplification path.
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// parseRank validates the optional rank query parameter of /nearest. Absent
// or empty selects the default distance ranking (backward compatible); the
// only accepted values are the exact lowercase "distance" and "population"
// (case-sensitive, like every other parameter on this handler).
func parseRank(raw string) (coordinates.Rank, bool) {
	switch raw {
	case "", "distance":
		return coordinates.RankDistance, true
	case "population":
		return coordinates.RankPopulation, true
	default:
		return 0, false
	}
}

// parseInclude validates the optional include query parameter of /nearest.
// Absent or empty requests the plain v1.0 response; the only accepted value
// is the exact lowercase "admin" (case-sensitive, like every other parameter
// on this handler). Any other value is invalid, not ignored.
func parseInclude(raw string) (includeAdmin bool, ok bool) {
	switch raw {
	case "", "admin":
		return raw == "admin", true
	default:
		return false, false
	}
}

// maxNameRunes caps the /coordinates name parameter. The longest real place
// names are well under 100 runes; the cap also bounds the fuzzy path, whose
// query-gram dedup is quadratic in name length.
const maxNameRunes = 200
