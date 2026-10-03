package coordinates

import "strings"

// AdminAttribution is the administrative attribution of one winning city:
// the raw per-country admin1/admin2 codes ("CA", "073" — not the composite
// "US.CA" table keys) and, when the optional names dataset was loaded, the
// admin1 display name. Zero values mean "absent": an empty Admin1Name is the
// codes-only mode, an empty Admin2Code a city without admin2 data.
type AdminAttribution struct {
	Admin1Code string
	Admin1Name string
	Admin2Code string
}

// AttachAdmin1Names sets the admin1 display names from the optional names
// dataset: a map from composite Admin1Codes keys ("US.CA") to names, nil for
// codes-only mode. Names are side data, never serialized, so the initializer
// attaches them on every boot. It is the only way names reach a finder, and
// must be called before the finder serves queries: the map is read without
// locking, and the finder keeps the map it is given.
func (f *S2Finder) AttachAdmin1Names(names map[string]string) {
	f.Admin1Names = names
}

// rawAdminCode strips the "CC." country prefix from a composite table key.
// Country codes contain no '.', so the first dot is the separator; a key
// without one (defensive) is returned unchanged.
func rawAdminCode(composite string) string {
	if i := strings.IndexByte(composite, '.'); i >= 0 {
		return composite[i+1:]
	}
	return composite
}

// adminOf returns the attribution of the city at cityIndex. Out-of-range
// indexes or a finder without admin arrays (none of the constructors produce
// one, but a zero-value S2Finder is representable) yield the zero
// attribution: attribution is enhancement data, never a query failure.
func (f *S2Finder) adminOf(cityIndex int) AdminAttribution {
	var attr AdminAttribution
	if cityIndex < 0 || cityIndex >= len(f.Cities) {
		return attr
	}
	if cityIndex < len(f.Admin1IDs) {
		if id := f.Admin1IDs[cityIndex]; id >= 0 && int(id) < len(f.Admin1Codes) {
			composite := f.Admin1Codes[id]
			attr.Admin1Code = rawAdminCode(composite)
			attr.Admin1Name = f.Admin1Names[composite] // "" in codes-only mode
		}
	}
	if cityIndex < len(f.Admin2IDs) {
		if id := f.Admin2IDs[cityIndex]; id >= 0 && int(id) < len(f.Admin2Codes) {
			attr.Admin2Code = rawAdminCode(f.Admin2Codes[id])
		}
	}
	return attr
}
