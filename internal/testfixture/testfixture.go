// Package testfixture builds deterministic synthetic city datasets for tests
// and benchmarks. It is not used by production code.
//
// Every test file used to carry its own loop filling a []city.SpatialCity
// with a different name, country and coordinate pattern. Cities owns the loop;
// a Spec supplies the patterns, so each caller states only what its
// assertions or measurements depend on.
package testfixture

import (
	"fmt"

	"github.com/SamyRai/cityFinder/lib/city"
)

// Spec describes how to derive city i. Every field is optional: a nil Name
// gives "City <i>", a nil Country gives "XX", nil coordinates give 0, nil
// Alts gives no alternate names and a nil Population gives 0.
//
// Within one city the funcs run in the order Name, Country, Lat, Lon, Alts,
// Population, so a Spec drawing from a seeded *rand.Rand reproduces the same
// dataset on every run.
type Spec struct {
	Name       func(i int) string
	Country    func(i int) string
	Lat, Lon   func(i int) float64
	Alts       func(i int) []string
	Population func(i int) int32
}

// Cities returns n cities derived from spec.
func Cities(n int, spec Spec) []city.SpatialCity {
	cities := make([]city.SpatialCity, n)
	for i := range cities {
		c := &cities[i]
		c.Name = fmt.Sprintf("City %d", i)
		if spec.Name != nil {
			c.Name = spec.Name(i)
		}
		c.Country = "XX"
		if spec.Country != nil {
			c.Country = spec.Country(i)
		}
		if spec.Lat != nil {
			c.Latitude = spec.Lat(i)
		}
		if spec.Lon != nil {
			c.Longitude = spec.Lon(i)
		}
		if spec.Alts != nil {
			c.AltNames = spec.Alts(i)
		}
		if spec.Population != nil {
			c.Population = spec.Population(i)
		}
	}
	return cities
}

// Format returns a Name func rendering format with the city index, for
// example Format("Gate%06d").
func Format(format string) func(int) string {
	return func(i int) string { return fmt.Sprintf(format, i) }
}

// Const returns a func that yields v for every city, for a single country or
// a fixed coordinate.
func Const[T any](v T) func(int) T {
	return func(int) T { return v }
}

// Cycle returns a Country func assigning values round-robin.
func Cycle(values []string) func(int) string {
	return func(i int) string { return values[i%len(values)] }
}
