package name

import (
	"testing"
)

// BenchmarkPrefixNames measures the autocomplete prefix walk (PrefixNames)
// over a single-country table of 1M distinct names — the shape of the large
// per-country tables in production (US alone holds millions of names). Three
// query shapes:
//
//   - sparse: a full 3-syllable prefix matches ~1-8 names (the common
//     autocomplete case: the binary search plus a short walk)
//   - dense: a single-syllable prefix matches ~20k names, so the walk runs
//     to the maxNames cap and must merge the overflow map
//   - miss: a prefix no name starts with, paying only the binary search
//
// Queries sweep a fixed coprime stride so every run profiles the same table
// spread.
func BenchmarkPrefixNames(b *testing.B) {
	const keyCount = 1_000_000
	cities := benchDiverseCities(keyCount)
	for i := range cities { // one big table: prefix space is unconstrained
		cities[i].Country = "AD"
	}
	silenceBuildLogs(b)
	finder := BuildIndex(cities)

	sparse := make([]string, 0, 1024)
	for i := 0; i < 1024; i++ {
		sparse = append(sparse, benchDiverseName((i * 97) % keyCount)[:6])
	}
	dense := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		dense = append(dense, benchDiverseName(i * 97)[:2])
	}
	const miss = "zz"

	b.Run("sparse-hit", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			if got := finder.PrefixNames("AD", sparse[i%len(sparse)], 10); len(got) == 0 {
				b.Fatal("sparse prefix must match its own name")
			}
			i++
		}
	})
	b.Run("dense-hit", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			if got := finder.PrefixNames("AD", dense[i%len(dense)], 10); len(got) != 10 {
				b.Fatalf("dense prefix must fill the cap, got %d", len(got))
			}
			i++
		}
	})
	b.Run("miss", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if got := finder.PrefixNames("AD", miss, 10); len(got) != 0 {
				b.Fatalf("miss prefix must return nothing, got %d", len(got))
			}
		}
	})
}
