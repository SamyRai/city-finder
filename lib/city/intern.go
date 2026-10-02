package city

import "unique"

// InternCountries replaces every Country with its canonical interned copy.
// Decoders (gob allocates a fresh backing per decoded string) and parsers
// would otherwise hold one copy of a 2-letter code per city — ~13.47M copies
// of ~250 values at production scale.
func InternCountries(cities []City) {
	for i := range cities {
		cities[i].Country = unique.Make(cities[i].Country).Value()
	}
}
