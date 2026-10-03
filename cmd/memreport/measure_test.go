package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const statusFixture = `Name:	memreport
VmPeak:	  200000 kB
VmHWM:	   20480 kB
VmRSS:	   10240 kB
Threads:	4
`

func TestProcStatusMB(t *testing.T) {
	assert.Equal(t, 20.0, procStatusMB(strings.NewReader(statusFixture), "VmHWM"))
	assert.Equal(t, 10.0, procStatusMB(strings.NewReader(statusFixture), "VmRSS"))
	assert.Equal(t, -1.0, procStatusMB(strings.NewReader(statusFixture), "VmSwap"), "absent field")
	assert.Equal(t, -1.0, procStatusMB(strings.NewReader(""), "VmRSS"), "empty input")
	assert.Equal(t, -1.0, procStatusMB(strings.NewReader("VmRSS:\tabc kB\n"), "VmRSS"), "malformed value")
}

func TestProcStatusFileMB_ReadsThisProcess(t *testing.T) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("no /proc")
	}
	assert.Greater(t, peakRSSMB(), 0.0)
	assert.Greater(t, rssMB(), 0.0)
}

func TestFileMB(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(p, make([]byte, 1<<20), 0o644))
	assert.Equal(t, 1.0, fileMB(p))
	assert.Equal(t, -1.0, fileMB(p+".missing"))
}

func TestWaitFuzzy_ReturnsWhenBuilt(t *testing.T) {
	for _, final := range []int32{2, 3} { // built, disabled
		var polls atomic.Int32
		state := func() int32 {
			switch polls.Add(1) {
			case 1:
				return 0
			case 2:
				return 1
			}
			return final
		}
		require.NoError(t, waitFuzzy(context.Background(), state, time.Millisecond), "final state %d", final)
		assert.EqualValues(t, 3, polls.Load())
	}
}

func TestWaitFuzzy_NeverReadyTimesOut(t *testing.T) {
	for _, stuck := range []int32{0, 1} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		start := time.Now()
		err := waitFuzzy(ctx, func() int32 { return stuck }, time.Millisecond)
		cancel()
		require.Error(t, err, "stuck state %d", stuck)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Contains(t, err.Error(), "fuzzy index not built")
		assert.Less(t, time.Since(start), 5*time.Second)
	}
}

func TestWaitFuzzy_CancelledContextIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, waitFuzzy(ctx, func() int32 { return 1 }, time.Hour), context.Canceled)
}

func TestWaitFuzzy_AlreadyBuiltDoesNotWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // even a dead context is fine when there is nothing to wait for
	assert.NoError(t, waitFuzzy(ctx, func() int32 { return 2 }, time.Hour))
}

// genConfig generates a small dataset and returns its config path.
func genConfig(t *testing.T, n int, seed int64) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, genDataset(dir, n, seed))
	return filepath.Join(dir, "config.json")
}

func TestMeasure_SmallDatasetPrintsAllKeys(t *testing.T) {
	cfg := genConfig(t, 300, 1)
	dir := filepath.Dir(cfg)
	var out bytes.Buffer
	o := measureOptions{
		cfgPath: cfg, fuzzyTimeout: time.Minute,
		dumpPath: filepath.Join(dir, "answers.txt"), profilePath: filepath.Join(dir, "heap.pprof"),
	}
	require.NoError(t, measure(context.Background(), o, &out))

	for _, key := range []string{
		"mode: cold", "go: ", "init_seconds: ", "heap_after_init_mb: ", "rss_after_init_mb: ",
		"rss_after_release_mb: ", "fuzzy_build_seconds: ", "heap_with_fuzzy_mb: ", "fuzzy_mb: ",
		"file_s2_mb: ", "file_name_mb: ", "file_postal_mb: ", "peak_rss_mb: ",
	} {
		assert.Contains(t, out.String(), key)
	}
	for _, f := range []string{o.dumpPath, o.profilePath} {
		st, err := os.Stat(f)
		require.NoError(t, err)
		assert.NotZero(t, st.Size(), f)
	}

	// Second run on the same directory loads the indexes the first one wrote.
	out.Reset()
	o.dumpPath, o.profilePath = "", ""
	require.NoError(t, measure(context.Background(), o, &out))
	assert.Contains(t, out.String(), "mode: warm")
}

func TestMeasure_MissingConfigFails(t *testing.T) {
	err := measure(context.Background(), measureOptions{cfgPath: filepath.Join(t.TempDir(), "nope.json"), fuzzyTimeout: time.Second}, &bytes.Buffer{})
	assert.Error(t, err)
}
