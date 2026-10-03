package name

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptionsDefaultsApplyWhenOmitted(t *testing.T) {
	assert.Equal(t, DefaultOptions(), BuildIndex(fuzzyFixtureCities()).opts)
	assert.Equal(t, DefaultOptions(), NewNameFinder().opts)
	assert.Equal(t, DefaultOptions(), NewFinderWithCapacity(5).opts)
}

// TestOptionsArePerFinder pins that two Finders in one process carry their
// own limits: one gated off, the other built, with no shared state.
func TestOptionsArePerFinder(t *testing.T) {
	gated := BuildIndex(fuzzyFixtureCities(), Options{FuzzyMaxNames: 1, FuzzyMaxCandidates: DefaultFuzzyMaxCandidates})
	open := BuildIndex(fuzzyFixtureCities(), Options{FuzzyMaxNames: 100, FuzzyMaxCandidates: 7})

	gated.WarmFuzzy()
	open.WarmFuzzy()
	assert.EqualValues(t, fuzzyDisabled, gated.FuzzyBuildState())
	waitFuzzyBuilt(t, open)

	open.mutex.RLock()
	budget := open.ngrams.budget
	open.mutex.RUnlock()
	assert.Equal(t, 7, budget, "the built index must carry its Finder's candidate budget")
}

func TestDeserializeIndexHonorsOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.idx")
	require.NoError(t, BuildIndex(fuzzyFixtureCities()).SerializeIndex(path))

	opts := Options{FuzzyMaxNames: 3, FuzzyMaxCandidates: 11}
	loaded, err := DeserializeIndex(path, opts)
	require.NoError(t, err)
	assert.Equal(t, opts, loaded.opts)

	loaded.WarmFuzzy()
	assert.EqualValues(t, fuzzyDisabled, loaded.FuzzyBuildState(), "5 keys exceed the per-Finder gate of 3")
}
