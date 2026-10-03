package testfixture

import (
	"math/rand"
	"reflect"
	"testing"
)

func TestCitiesDefaults(t *testing.T) {
	got := Cities(2, Spec{})
	if len(got) != 2 || got[1].Name != "City 1" || got[1].Country != "XX" || got[1].Latitude != 0 || got[1].AltNames != nil {
		t.Errorf("defaults = %+v", got)
	}
}

func TestCitiesAppliesEveryField(t *testing.T) {
	got := Cities(4, Spec{
		Name:       Format("N%02d"),
		Country:    Cycle([]string{"FR", "DE"}),
		Lat:        func(i int) float64 { return float64(i) },
		Lon:        Const(7.5),
		Alts:       func(i int) []string { return []string{"alt"} },
		Population: func(i int) int32 { return int32(i * 10) },
	})
	c := got[3]
	if c.Name != "N03" || c.Country != "DE" || c.Latitude != 3 || c.Longitude != 7.5 || c.AltNames[0] != "alt" || c.Population != 30 {
		t.Errorf("city 3 = %+v", c)
	}
}

// TestCitiesIsDeterministic: a Spec drawing from a seeded source yields the
// same dataset every time, because the funcs run in a fixed order.
func TestCitiesIsDeterministic(t *testing.T) {
	gen := func() any {
		r := rand.New(rand.NewSource(1))
		return Cities(50, Spec{
			Country: func(int) string { return string(rune('A' + r.Intn(26))) },
			Lat:     func(int) float64 { return r.Float64() },
			Lon:     func(int) float64 { return r.Float64() },
		})
	}
	if !reflect.DeepEqual(gen(), gen()) {
		t.Error("two runs differ")
	}
}
