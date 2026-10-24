package city

import (
	"math"
	"testing"
)

func TestValidCoordinate(t *testing.T) {
	cases := []struct {
		lat, lon float64
		want     bool
	}{
		{0, 0, true},
		{90, 180, true},
		{-90, -180, true},
		{90.0001, 0, false},
		{0, 180.0001, false},
		{-91, 0, false},
		{math.NaN(), 0, false},
		{0, math.NaN(), false},
		{math.Inf(1), 0, false},
		{0, math.Inf(-1), false},
	}
	for _, c := range cases {
		if got := ValidCoordinate(c.lat, c.lon); got != c.want {
			t.Errorf("ValidCoordinate(%v, %v) = %v, want %v", c.lat, c.lon, got, c.want)
		}
	}
}
