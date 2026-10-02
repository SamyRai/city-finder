package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

// Synthetic GeoNames-format dataset, shaped like the production dump on the
// properties that drive index size and query cost:
//   - land-clustered coordinates with 5 decimals (most of the globe empty);
//   - ~250 countries with a heavy skew (a few countries hold most rows);
//   - multi-syllable names incl. multi-word, hyphenated and non-ASCII ones,
//     frequent homonyms within a country, ~25% of rows with 1–3 alternate
//     names;
//   - heavy-tailed populations, mostly zero; feature classes P/A/L/H/T/S/V;
//   - admin1/admin2 codes, an admin1-names file, and a postal dump at the
//     production postal:city row ratio.
// It is NOT the real dump: absolute sizes differ (see docs/performance.md),
// but the same generator feeds both sides of a before/after comparison.

var syllables = []string{
	"ka", "ri", "sa", "to", "ne", "mo", "lu", "be", "an", "er", "os", "ma", "de", "la", "ro", "vi",
	"ville", "burg", "dorf", "pur", "abad", "grad", "ton", "ford", "heim", "stadt", "polis", "ovo",
	"ño", "ür", "é", "ów", "ше", "ка", "го", "яр", "ão", "ç", "ia", "en", "um", "ay", "il", "or",
}

var prefixes = []string{"", "", "", "", "", "", "San ", "Saint-", "Nueva ", "Bad ", "Al ", "Novo", "Port "}
var suffixes = []string{"", "", "", "", "", "", " de Abajo", "-sur-Mer", " Station", " Village", " City"}

func genName(r *rand.Rand) string {
	var b strings.Builder
	b.WriteString(prefixes[r.Intn(len(prefixes))])
	n := 2 + r.Intn(3)
	if r.Float64() < 0.1 {
		n++
	}
	for i := 0; i < n; i++ {
		s := syllables[r.Intn(len(syllables))]
		if i == 0 && b.Len() == 0 {
			rs := []rune(s) // capitalize by rune: syllables can be multi-byte
			s = strings.ToUpper(string(rs[0])) + string(rs[1:])
		}
		b.WriteString(s)
	}
	b.WriteString(suffixes[r.Intn(len(suffixes))])
	return b.String()
}

type continent struct{ lat, lon, latSpread, lonSpread, share float64 }

var continents = []continent{
	{45, -100, 12, 25, 0.16}, {-15, -60, 10, 16, 0.10}, {50, 15, 8, 18, 0.18},
	{5, 20, 15, 20, 0.12}, {35, 90, 12, 25, 0.33}, {-25, 135, 8, 14, 0.06},
	{62, 60, 6, 40, 0.03}, {0, 0, 90, 180, 0.02}, // last: scattered ocean/undersea features
}

func genPoint(r *rand.Rand) (float64, float64) {
	x := r.Float64()
	c := continents[len(continents)-1]
	for _, k := range continents {
		if x < k.share {
			c = k
			break
		}
		x -= k.share
	}
	lat := math.Max(-89.99, math.Min(89.99, c.lat+r.NormFloat64()*c.latSpread))
	lon := c.lon + r.NormFloat64()*c.lonSpread
	lon = math.Mod(lon+540, 360) - 180
	return math.Round(lat*1e5) / 1e5, math.Round(lon*1e5) / 1e5
}

func countryCode(i int) string { return fmt.Sprintf("%c%c", 'A'+i/26, 'A'+i%26) }

// genDataset writes allCountries_dump.txt, allCountries_zip.txt,
// admin1CodesASCII.txt and config.json into dir.
func genDataset(dir string, n int, seed int64) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	r := rand.New(rand.NewSource(seed))
	const nCountries = 250
	pickCountry := func() int { return int(nCountries * math.Pow(r.Float64(), 3)) }
	classes := []struct {
		class, code string
		share       float64
	}{{"P", "PPL", 0.38}, {"H", "STM", 0.20}, {"T", "MT", 0.14}, {"S", "SCH", 0.13}, {"L", "AREA", 0.07}, {"A", "ADM2", 0.05}, {"V", "FRST", 0.03}}

	f, err := os.Create(filepath.Join(dir, "allCountries_dump.txt"))
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	// A per-country pool of recently used names produces realistic homonyms
	// (the same name several times in one country).
	recent := make([][]string, nCountries)
	for i := 0; i < n; i++ {
		ci := pickCountry()
		var nm string
		if pool := recent[ci]; len(pool) > 0 && r.Float64() < 0.15 {
			nm = pool[r.Intn(len(pool))]
		} else {
			nm = genName(r)
			if len(recent[ci]) < 64 {
				recent[ci] = append(recent[ci], nm)
			} else {
				recent[ci][r.Intn(64)] = nm
			}
		}
		var alts []string
		if r.Float64() < 0.25 {
			for k := 0; k < 1+r.Intn(3); k++ {
				alts = append(alts, genName(r))
			}
		}
		lat, lon := genPoint(r)
		x, fc := r.Float64(), classes[0]
		for _, c := range classes {
			if x < c.share {
				fc = c
				break
			}
			x -= c.share
		}
		pop := 0
		if fc.class == "P" && r.Float64() < 0.6 || fc.class == "A" && r.Float64() < 0.5 {
			pop = int(math.Min(3e7, 100*math.Exp(r.ExpFloat64()*1.6)))
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%.5f\t%.5f\t%s\t%s\t%s\t\t%02d\t%03d\t\t\t%d\t\t0\tEtc/UTC\t2026-01-01\n",
			1_000_000+i, nm, nm, strings.Join(alts, ","), lat, lon, fc.class, fc.code, countryCode(ci), r.Intn(40), r.Intn(400), pop)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	f, err = os.Create(filepath.Join(dir, "admin1CodesASCII.txt"))
	if err != nil {
		return err
	}
	w = bufio.NewWriter(f)
	for c := 0; c < nCountries; c++ {
		for a := 0; a < 40; a++ {
			name := genName(r)
			fmt.Fprintf(w, "%s.%02d\t%s\t%s\t%d\n", countryCode(c), a, name, name, 5_000_000+c*40+a)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// Production ratio ≈ 1.8M postal rows per 13.47M cities, over ~100
	// countries; codes are unique per country.
	f, err = os.Create(filepath.Join(dir, "allCountries_zip.txt"))
	if err != nil {
		return err
	}
	w = bufio.NewWriterSize(f, 1<<20)
	np := n * 18 / 135
	for i := 0; i < np; i++ {
		cc := countryCode(i % 100)
		lat, lon := genPoint(r)
		fmt.Fprintf(w, "%s\t%05d\t%s\t%s\t%02d\t%s\t%03d\t\t\t%.4f\t%.4f\t%d\n",
			cc, i/100, genName(r), "Region "+genName(r), r.Intn(40), "County "+genName(r), r.Intn(400), lat, lon, 1+r.Intn(6))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	cfg := map[string]any{
		"datasets_folder": abs, "all_cities_file": "allCountries_dump.txt", "postal_codes_file": "allCountries_zip.txt",
		"admin1_codes_file": "admin1CodesASCII.txt", "name_index_file": "name_index.gob",
		"postal_code_index_file": "postal_code_index.gob", "s2": map[string]string{"index_file": "s2index.gob"},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644)
}
