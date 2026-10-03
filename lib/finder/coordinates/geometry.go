package coordinates

import (
	"math"

	"github.com/golang/geo/s1"
)

// chordBoundSlack shrinks chordLowerBoundKm by a relative margin far above
// the rounding error of the asin path (~1e-16), so the computed lower bound
// never exceeds the computed true distance.
const chordBoundSlack = 1e-9

// chordLowerBoundKm is a cheap lower bound on the great-circle distance a
// chord spans: a chord is never longer than its arc (2*asin(c/2) >= c), so
// sqrt(length2) on the unit sphere bounds the angle from below without the
// asin. Scoring an entry at this distance gives an upper bound on its exact
// gravity score; float division and multiplication are monotone, so when
// that upper bound cannot beat a running maximum, neither can the exact
// score, and the entry is skipped with no change to any answer.
func chordLowerBoundKm(chord s1.ChordAngle) float64 {
	return math.Sqrt(float64(chord)) * earthRadiusKm * (1 - chordBoundSlack)
}

// kmToChordAngle converts a great-circle kilometer radius on the finder's
// sphere into the ChordAngle distance the s2 query API uses.
func kmToChordAngle(km float64) s1.ChordAngle {
	return s1.ChordAngleFromAngle(s1.Angle(km / earthRadiusKm))
}
