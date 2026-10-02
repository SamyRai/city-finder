package main

import (
	"bufio"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
)

// dumpTranscript runs a fixed, seeded query set through every public lookup
// and writes one line per query with the full answer (every City field the
// API exposes, distances to 1e-9 km, admin attribution, prefix lists). Two
// transcripts from the same dataset must be byte-identical for a layout
// change to count as "no data regression".
func dumpTranscript(f *finder.Finder, cfg *config.Config, path string) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 1<<20)
	r := rand.New(rand.NewSource(99))
	cityStr := func(c *city.City) string {
		if c == nil {
			return "nil"
		}
		return fmt.Sprintf("%q|%q|%.9f|%.9f|%d", c.Name, c.Country, c.Latitude, c.Longitude, c.Population)
	}

	// Nearest: uniform global points, plus points AT dataset rows (exact ties,
	// zero distances), both ranks, with admin attribution.
	rows, err := sampleRows(filepath.Join(cfg.DatasetsFolder, cfg.AllCitiesFile), 3000, r)
	if err != nil {
		return err
	}
	type pt struct{ lat, lon float64 }
	var pts []pt
	for i := 0; i < 5000; i++ {
		pts = append(pts, pt{math.Asin(2*r.Float64()-1) * 180 / math.Pi, r.Float64()*360 - 180})
	}
	for _, row := range rows {
		pts = append(pts, pt{row.lat, row.lon})
	}
	for i, p := range pts {
		for _, rank := range []coordinates.Rank{coordinates.RankDistance, coordinates.RankPopulation} {
			if rank == coordinates.RankPopulation && i%40 != 0 {
				continue // population rank is costly over ocean; sample it
			}
			c, d, adm, err := f.S2Finder.NearestPlaceWithAdmin(p.lat, p.lon, rank)
			fmt.Fprintf(w, "nearest %d %.5f %.5f -> %s %.9f %s/%s/%s err=%v\n", rank, p.lat, p.lon, cityStr(c), d, adm.Admin1Code, adm.Admin1Name, adm.Admin2Code, err)
		}
	}

	// Names: exact (primary + alternate), 1- and 2-edit typos, misses,
	// unknown countries; prefixes of several lengths.
	for _, row := range rows {
		qs := append([]string{row.name}, row.alts...)
		qs = append(qs, typo(row.name, r), typo(typo(row.name, r), r), row.name+"zzqx")
		for _, q := range qs {
			fmt.Fprintf(w, "name %q %s -> %s\n", q, row.country, cityStr(f.FindCityByName(q, row.country)))
		}
		fmt.Fprintf(w, "name %q ZZ -> %s\n", row.name, cityStr(f.FindCityByName(row.name, "ZZ")))
		for _, n := range []int{1, 2, 3, 5} {
			rs := []rune(row.name)
			if n > len(rs) {
				continue
			}
			var b strings.Builder
			for _, m := range f.PrefixNames(row.country, string(rs[:n]), 10) {
				fmt.Fprintf(&b, "%q=%s;", m.Name, cityStr(m.City))
			}
			fmt.Fprintf(w, "prefix %q %s -> %s\n", string(rs[:n]), row.country, b.String())
		}
	}

	// Postal: hits sampled from the dump, misses, wrong country.
	codes, err := samplePostal(filepath.Join(cfg.DatasetsFolder, cfg.PostalCodesFile), 2000, r)
	if err != nil {
		return err
	}
	for _, c := range codes {
		fmt.Fprintf(w, "postal %q %s -> %s\n", c[1], c[0], cityStr(f.FindCityByPostalCode(c[1], c[0])))
		fmt.Fprintf(w, "postal %q %s -> %s\n", c[1]+"9", c[0], cityStr(f.FindCityByPostalCode(c[1]+"9", c[0])))
		fmt.Fprintf(w, "postal %q ZZ -> %s\n", c[1], cityStr(f.FindCityByPostalCode(c[1], "ZZ")))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return out.Close()
}

type datasetRow struct {
	name, country string
	alts          []string
	lat, lon      float64
}

// sampleRows reservoir-samples k rows of the city dump.
func sampleRows(path string, k int, r *rand.Rand) ([]datasetRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<24)
	var res []datasetRow
	n := 0
	for s.Scan() {
		fields := strings.Split(s.Text(), "\t")
		if len(fields) < 9 || fields[8] == "" {
			continue
		}
		var row datasetRow
		row.name, row.country = fields[1], fields[8]
		if fields[3] != "" {
			row.alts = strings.Split(fields[3], ",")
		}
		fmt.Sscanf(fields[4], "%g", &row.lat)
		fmt.Sscanf(fields[5], "%g", &row.lon)
		n++
		if len(res) < k {
			res = append(res, row)
		} else if j := r.Intn(n); j < k {
			res[j] = row
		}
	}
	return res, s.Err()
}

func samplePostal(path string, k int, r *rand.Rand) ([][2]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	var res [][2]string
	n := 0
	for s.Scan() {
		fields := strings.SplitN(s.Text(), "\t", 3)
		if len(fields) < 2 {
			continue
		}
		n++
		if len(res) < k {
			res = append(res, [2]string{fields[0], fields[1]})
		} else if j := r.Intn(n); j < k {
			res[j] = [2]string{fields[0], fields[1]}
		}
	}
	return res, s.Err()
}

// typo applies one random edit (substitute, delete or insert) to s.
func typo(s string, r *rand.Rand) string {
	rs := []rune(s)
	if len(rs) < 2 {
		return s + "a"
	}
	i := r.Intn(len(rs))
	switch r.Intn(3) {
	case 0:
		rs[i] = 'x'
	case 1:
		rs = append(rs[:i], rs[i+1:]...)
	default:
		rs = append(rs[:i], append([]rune{'q'}, rs[i:]...)...)
	}
	return string(rs)
}
