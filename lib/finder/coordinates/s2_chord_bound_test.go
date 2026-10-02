package coordinates

import (
	"math"
	"math/rand"
	"testing"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
	"github.com/stretchr/testify/require"
)

// referenceOutsideBound is populationOutsideBound without the chord
// lower-bound skip: every outside entry is scored through asin.
func referenceOutsideBound(f *S2Finder, radiusKm float64, target s2.Point) float64 {
	radiusTerm := radiusKm*radiusKm + 1.0
	var popK int32
	if len(f.topPopulations) >= topPopulationK {
		popK = f.topPopulations[topPopulationK-1].population
	}
	bound := float64(popK) / radiusTerm
	limit := kmToChordAngle(radiusKm)
	for _, entry := range f.topPopulations {
		if float64(entry.population)/radiusTerm <= bound {
			break
		}
		if chord := s2.ChordAngleBetweenPoints(entry.point, target); chord > limit {
			d := chord.Angle().Radians() * earthRadiusKm
			if score := float64(entry.population) / (d*d + 1.0); score > bound {
				bound = score
			}
		} else if inside := float64(entry.population) / radiusTerm; inside > bound {
			bound = inside
		}
	}
	return bound
}

// referenceAnchor is anchoredByPopulation's table argmax without the skip.
func referenceAnchor(f *S2Finder, target s2.Point) (float64, float64) {
	sW, dW := 0.0, 0.0
	for _, entry := range f.topPopulations {
		if float64(entry.population) <= sW {
			break
		}
		d := s2.ChordAngleBetweenPoints(entry.point, target).Angle().Radians() * earthRadiusKm
		if score := float64(entry.population) / (d*d + 1.0); score > sW {
			sW, dW = score, d
		}
	}
	return sW, dW
}

// TestChordLowerBoundNeverExceedsDistance checks the bound the skip relies
// on across the whole chord range, including the tiny and antipodal ends.
func TestChordLowerBoundNeverExceedsDistance(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	check := func(chord s1.ChordAngle) {
		require.LessOrEqual(t, chordLowerBoundKm(chord), chord.Angle().Radians()*earthRadiusKm, "chord %v", float64(chord))
	}
	for _, c := range []s1.ChordAngle{0, 1e-300, 1e-30, 1e-16, 1e-8, 1, 3.999999, s1.StraightChordAngle} {
		check(c)
	}
	for range 200000 {
		check(s1.ChordAngle(rng.Float64() * 4))
		check(s1.ChordAngle(math.Pow(10, -rng.Float64()*20)))
	}
}

// TestChordSkipMatchesReference proves the skip is exact: the bound and the
// table anchor are bit-identical to the asin-everywhere versions, over the
// ocean fixture (full top-K table) and every escalation radius.
func TestChordSkipMatchesReference(t *testing.T) {
	silenceIndexLogs(t)
	finder := buildBenchIndex(t, anchoredOceanFixture(t))
	require.Len(t, finder.topPopulations, topPopulationK, "fixture must exercise the popK term")

	rng := rand.New(rand.NewSource(7))
	radii := []float64{populationRankInitialRadiusKm, 50, populationRankAnchoredAfterKm, 1250, 6250}
	for range 3000 {
		lat := math.Asin(2*rng.Float64()-1) * 180 / math.Pi
		target := s2.PointFromLatLng(s2.LatLngFromDegrees(lat, rng.Float64()*360-180))
		for _, r := range radii {
			require.Equal(t, referenceOutsideBound(finder, r, target), finder.populationOutsideBound(r, target))
		}
		wantS, wantD := referenceAnchor(finder, target)
		gotS, gotD := finder.tableAnchor(target)
		require.Equal(t, wantS, gotS)
		require.Equal(t, wantD, gotD)
	}
}
