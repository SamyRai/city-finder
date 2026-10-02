package loadgen

import (
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"slices"
	"strings"
)

// Workload produces the request paths (with query string) a run cycles
// through. Paths are generated once, seeded, before the run: nothing is
// formatted on the hot path, and two runs with the same seed send the same
// sequence.
type Workload struct {
	Name  string
	Paths []string
}

// realPlaces are well-known (name, country) pairs plus 1–2-edit typos of
// them, so name lookups exercise both the exact and the fuzzy path against
// the production dataset.
var realPlaces = [][2]string{
	{"Paris", "FR"}, {"Pars", "FR"}, {"Berlin", "DE"}, {"Berln", "DE"},
	{"London", "GB"}, {"Lodnon", "GB"}, {"Madrid", "ES"}, {"Rome", "IT"},
	{"Tokyo", "JP"}, {"Tokio", "JP"}, {"New York City", "US"}, {"Chicago", "US"},
	{"Chicgo", "US"}, {"Toronto", "CA"}, {"Sydney", "AU"}, {"Sao Paulo", "BR"},
	{"Mumbai", "IN"}, {"Mumbia", "IN"}, {"Cairo", "EG"}, {"Moscow", "RU"},
}

// WorkloadNames lists the built-in workloads.
var WorkloadNames = []string{"nearest", "nearest-admin", "nearest-population", "coordinates", "postal", "mixed"}

// NewWorkload builds the named workload with n paths from seed.
//
//   - nearest*: uniformly random points over the sphere (most fall on open
//     water, like the production latency passes), with the given rank/include.
//   - coordinates: real city names and typos (exact + fuzzy).
//   - postal: real postal codes.
//   - mixed: 80% nearest, 5% nearest-population, 10% coordinates, 5% postal —
//     an assumed blend; replace it with the production route mix when known.
func NewWorkload(name string, n int, seed int64) (Workload, error) {
	if n <= 0 {
		return Workload{}, fmt.Errorf("workload size must be > 0, got %d", n)
	}
	rng := rand.New(rand.NewSource(seed))
	gen := map[string]func() string{
		"nearest":            func() string { return nearestPath(rng, "") },
		"nearest-admin":      func() string { return nearestPath(rng, "&include=admin") },
		"nearest-population": func() string { return nearestPath(rng, "&rank=population") },
		"coordinates":        func() string { return coordinatesPath(rng) },
		"postal":             func() string { return postalPath(rng) },
	}
	gen["mixed"] = func() string {
		switch r := rng.Float64(); {
		case r < 0.80:
			return gen["nearest"]()
		case r < 0.85:
			return gen["nearest-population"]()
		case r < 0.95:
			return gen["coordinates"]()
		default:
			return gen["postal"]()
		}
	}
	g, ok := gen[name]
	if !ok {
		return Workload{}, fmt.Errorf("unknown workload %q (have %s)", name, strings.Join(WorkloadNames, ", "))
	}
	paths := make([]string, n)
	for i := range paths {
		paths[i] = g()
	}
	return Workload{Name: name, Paths: paths}, nil
}

// HasWorkload reports whether name is a built-in workload.
func HasWorkload(name string) bool { return slices.Contains(WorkloadNames, name) }

func nearestPath(rng *rand.Rand, extra string) string {
	lat := math.Asin(2*rng.Float64()-1) * 180 / math.Pi
	lon := rng.Float64()*360 - 180
	return fmt.Sprintf("/nearest?lat=%.5f&lon=%.5f%s", lat, lon, extra)
}

func coordinatesPath(rng *rand.Rand) string {
	p := realPlaces[rng.Intn(len(realPlaces))]
	return "/coordinates?name=" + url.QueryEscape(p[0]) + "&country-code=" + p[1]
}

var realPostal = [][2]string{
	{"10001", "US"}, {"94103", "US"}, {"60601", "US"}, {"75001", "FR"},
	{"10115", "DE"}, {"SW1A", "GB"}, {"100-0001", "JP"}, {"28001", "ES"},
}

func postalPath(rng *rand.Rand) string {
	p := realPostal[rng.Intn(len(realPostal))]
	return "/postalCode?code=" + url.QueryEscape(p[0]) + "&country-code=" + p[1]
}
