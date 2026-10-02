package city

import "testing"

func TestFingerprintDetectsEveryField(t *testing.T) {
	base := []City{{Latitude: 1, Longitude: 2, Population: 3, Name: "Ab", Country: "CD"}, {Name: "E", Country: "FG"}}
	fp := Fingerprint(base)
	if fp != Fingerprint(append([]City(nil), base...)) {
		t.Fatal("fingerprint must be deterministic")
	}
	mutations := []func(c []City){
		func(c []City) { c[0].Latitude = 1.0000001 },
		func(c []City) { c[0].Longitude = -2 },
		func(c []City) { c[0].Population = 4 },
		func(c []City) { c[0].Name = "Ac" },
		func(c []City) { c[1].Country = "FH" },
		// Field boundaries are length-prefixed: moving a byte between Name
		// and Country must change the result.
		func(c []City) { c[0].Name, c[0].Country = "AbC", "D" },
		func(c []City) { c[0], c[1] = c[1], c[0] },
	}
	for i, mutate := range mutations {
		m := append([]City(nil), base...)
		mutate(m)
		if Fingerprint(m) == fp {
			t.Errorf("mutation %d not detected", i)
		}
	}
	if Fingerprint(base[:1]) == fp {
		t.Error("length change not detected")
	}
}
