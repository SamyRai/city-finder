package loadgen

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWorkloadIsSeededAndValid(t *testing.T) {
	for _, name := range WorkloadNames {
		a, err := NewWorkload(name, 500, 7)
		require.NoError(t, err, name)
		b, _ := NewWorkload(name, 500, 7)
		assert.Equal(t, a.Paths, b.Paths, "%s: same seed, same sequence", name)
		for _, p := range a.Paths {
			u, err := url.Parse(p)
			require.NoError(t, err, p)
			if u.Path != "/nearest" {
				continue
			}
			lat, err1 := strconv.ParseFloat(u.Query().Get("lat"), 64)
			lon, err2 := strconv.ParseFloat(u.Query().Get("lon"), 64)
			require.NoError(t, err1, p)
			require.NoError(t, err2, p)
			assert.True(t, lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180, p)
		}
	}
}

func TestMixedWorkloadCoversRoutes(t *testing.T) {
	w, err := NewWorkload("mixed", 4000, 1)
	require.NoError(t, err)
	seen := map[string]int{}
	for _, p := range w.Paths {
		seen[strings.SplitN(p, "?", 2)[0]]++
	}
	assert.Greater(t, seen["/nearest"], 3000)
	assert.Greater(t, seen["/coordinates"], 200)
	assert.Greater(t, seen["/postalCode"], 100)
}

func TestNewWorkloadRejectsBadInput(t *testing.T) {
	_, err := NewWorkload("nope", 10, 1)
	assert.Error(t, err)
	_, err = NewWorkload("nearest", 0, 1)
	assert.Error(t, err)
	assert.False(t, HasWorkload("nope"))
	assert.True(t, HasWorkload("mixed"))
}
