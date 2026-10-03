package name

import (
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stagingFixture builds a deterministic staging index with countries of very
// different sizes, homonym lists of varying length, and one empty country.
func stagingFixture() map[string]map[string][]int32 {
	rng := rand.New(rand.NewSource(7))
	index := map[string]map[string][]int32{"ZZ": {}}
	for c := 0; c < 40; c++ {
		country := fmt.Sprintf("C%02d", c)
		names := 1 + rng.Intn(1+c*60)
		m := make(map[string][]int32, names)
		for n := 0; n < names; n++ {
			ids := make([]int32, 1+rng.Intn(3))
			for i := range ids {
				ids[i] = int32(rng.Intn(1000))
			}
			m[fmt.Sprintf("name-%04d", rng.Intn(names*2))] = ids
		}
		index[country] = m
	}
	return index
}

// TestBuildTablesParallelEqualsSequential pins that the worker count never
// shows in the output: tables are byte-identical to a plain serial
// buildTable per country.
func TestBuildTablesParallelEqualsSequential(t *testing.T) {
	index := stagingFixture()

	want := make(map[string]*nameTable, len(index))
	for country, refs := range index {
		table, err := buildTable(refs, nil)
		require.NoError(t, err)
		want[country] = table
	}

	for _, workers := range []int{1, 2, 3, 8, 64} {
		got, err := buildTables(index, workers, nil)
		require.NoError(t, err)
		assert.Equalf(t, want, got, "workers=%d", workers)
	}

	for country, table := range want {
		assert.Truef(t, sort.StringsAreSorted(table.names), "%s: names must be sorted", country)
	}
}

func TestBuildTablesEmpty(t *testing.T) {
	got, err := buildTables(nil, 8, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestBuildTablesFirstErrorWins(t *testing.T) {
	index := stagingFixture()
	boom := errors.New("boom")
	got, err := buildTables(index, 8, func(refs map[string][]int32) error {
		if len(refs) > 100 {
			return boom
		}
		return nil
	})
	assert.ErrorIs(t, err, boom)
	assert.Nil(t, got, "a failed build returns no tables")
}

// TestDeserializeIndexRejectsOutOfRangeIDInAnyCountry plants one bad id in a
// small country among many and requires ErrCorruptIndex, whichever worker
// reaches it.
func TestDeserializeIndexRejectsOutOfRangeIDInAnyCountry(t *testing.T) {
	cities := gateCities(50)
	good := BuildIndex(cities)
	for _, bad := range []int32{-1, 50, 1 << 30} {
		finder := BuildIndex(cities)
		for i := 0; i < 20; i++ {
			finder.countries[fmt.Sprintf("X%02d", i)] = good.countries["GC"]
		}
		small := &nameTable{names: []string{"Oops"}, starts: []int32{0, 1}, ids: []int32{bad}}
		finder.countries["BAD"] = small

		path := filepath.Join(t.TempDir(), "bad.idx")
		require.NoError(t, finder.SerializeIndex(path))
		_, err := DeserializeIndex(path)
		require.ErrorIs(t, err, ErrCorruptIndex, "id %d", bad)
		assert.ErrorContains(t, err, "outside the")
	}
}

// TestBuildIndexEmptyCountryFirst: a first city with an empty country code
// used to hit the staging loop's "same country as the previous city" cache
// while it was still nil.
func TestBuildIndexEmptyCountryFirst(t *testing.T) {
	cities := gateCities(3)
	cities[0].Country = ""
	finder := BuildIndex(cities)
	assert.NotNil(t, finder.CityByName("GateCity000000", ""), "city under the empty country code must be indexed")
	assert.NotNil(t, finder.CityByName("GateCity000001", "GC"))
}
