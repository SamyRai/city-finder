package zz

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/indexfile"
)

type h struct{ Count int }
type p struct {
	Cities    []city.City
	Admin1IDs []int32
	Admin2IDs []int32
}

func TestM(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]city.City{
		"zero":      {},
		"minimal":   {Name: "A", Country: "US"},
		"realistic": {Name: "Springfield", Country: "US", Latitude: 39.78, Longitude: -89.65, Population: 116250},
	}
	for name, c := range cases {
		for _, n := range []int{200_000, 2_000_000, 8_000_000} {
			cs := make([]city.City, n)
			a := make([]int32, n)
			for i := range cs {
				cs[i] = c
				a[i] = -1
			}
			path := filepath.Join(dir, "x")
			if err := indexfile.Write(path, h{n}, &p{cs, a, a}); err != nil {
				t.Fatal(err)
			}
			fi, _ := os.Stat(path)
			r, _ := indexfile.Open(path)
			var hh h
			r.Header(&hh)
			var pp p
			r.LimitPayload(1 << 40)
			r.Payload(&pp)
			s := r.Stats()
			r.Close()
			fmt.Printf("%s n=%d file=%d payload=%d ratio=%.0f entries/B=%.0f B/entry=%.1f\n", name, n, fi.Size(), s.PayloadBytes, float64(s.PayloadBytes)/float64(s.FileBytes), float64(n)/float64(s.FileBytes), float64(s.PayloadBytes)/float64(n))
		}
	}
}
