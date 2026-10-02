package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlags(t *testing.T) {
	o, err := parseFlags([]string{"-rates", "100, 200", "-window", "2s", "-workload", "mixed"})
	require.NoError(t, err)
	assert.Equal(t, []float64{100, 200}, o.rates)
	assert.Equal(t, 2*time.Second, o.window)
	assert.Equal(t, "mixed", o.workload)

	for _, bad := range [][]string{
		{"-rates", "100,x"},
		{"-rates", "0"},
		{"-window", "0s"},
		{"-workload", "nope"},
	} {
		_, err := parseFlags(bad)
		assert.Error(t, err, "%v", bad)
	}
}
