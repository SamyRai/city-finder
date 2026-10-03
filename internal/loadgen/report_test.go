package loadgen

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteTable_Golden(t *testing.T) {
	steps := []Summary{
		{
			OfferedRPS: 500, AchievedRPS: 499.6, ErrorRate: 0, Dropped: 0, NotFound: 12,
			P50: 180*time.Microsecond + 400, P90: 420 * time.Microsecond, P99: 1234567 * time.Nanosecond,
			P999: 9876543 * time.Nanosecond, Max: 15 * time.Millisecond,
		},
		{
			OfferedRPS: 4000, AchievedRPS: 3100.04, ErrorRate: 0.0625, Dropped: 321, NotFound: 0,
			P50: 2 * time.Millisecond, P90: 40 * time.Millisecond, P99: 1500 * time.Millisecond,
			P999: 2345678901 * time.Nanosecond, Max: 5 * time.Second,
		},
	}
	var buf bytes.Buffer
	require.NoError(t, WriteTable(&buf, steps))
	want := "" +
		"  offered/s  achieved/s  err%  dropped  404s    p50    p90     p99   p99.9   max\n" +
		"        500       499.6  0.00        0    12  180µs  420µs  1.23ms  9.88ms  15ms\n" +
		"       4000      3100.0  6.25      321     0    2ms   40ms    1.5s  2.346s    5s\n"
	assert.Equal(t, want, buf.String())
}

func TestWriteTable_EmptyIsHeaderOnly(t *testing.T) {
	for _, steps := range [][]Summary{nil, {}} {
		var buf bytes.Buffer
		require.NoError(t, WriteTable(&buf, steps))
		assert.Equal(t, "  offered/s  achieved/s  err%  dropped  404s  p50  p90  p99  p99.9  max\n", buf.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestWriteTable_PropagatesWriteError(t *testing.T) {
	assert.Error(t, WriteTable(failingWriter{}, []Summary{{OfferedRPS: 1}}))
}

func TestRound_Boundaries(t *testing.T) {
	for _, tc := range []struct{ in, want time.Duration }{
		{0, 0},
		{499 * time.Nanosecond, 0},
		{500 * time.Nanosecond, time.Microsecond},
		{999*time.Microsecond + 499, 999 * time.Microsecond},
		{999*time.Microsecond + 500, time.Millisecond}, // rounds up into the next tier
		{time.Millisecond, time.Millisecond},
		{time.Millisecond + 4999, time.Millisecond},
		{time.Millisecond + 5000, time.Millisecond + 10*time.Microsecond},
		{9990 * time.Microsecond, 9990 * time.Microsecond},
		{999*time.Millisecond + 999*time.Microsecond, 999*time.Millisecond + 990*time.Microsecond + 10*time.Microsecond},
		{time.Second, time.Second},
		{time.Second + 400*time.Microsecond, time.Second},
		{time.Second + 500*time.Microsecond, time.Second + time.Millisecond},
		{-time.Millisecond, -time.Millisecond},
	} {
		assert.Equal(t, tc.want, round(tc.in), "round(%v)", tc.in)
	}
}
