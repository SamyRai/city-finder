package initializer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settleGoroutines polls until the goroutine count is back at base, so a
// leaked step shows up as a failure rather than as flakiness elsewhere.
func settleGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines outlived the boot: %d > %d", runtime.NumGoroutine(), base)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEnsureFinders_FailedWriteDrainsSiblings: with the name index's folder
// missing, its write fails while the S2 and postal writes proceed; the boot
// reports the failure and leaves no goroutine behind.
func TestEnsureFinders_FailedWriteDrainsSiblings(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.NameIndexFile = filepath.Join("missing-dir", "name_index.gob")
	writeTinyDatasets(t, cfg)

	base := runtime.NumGoroutine()
	_, err := ensureFinders(context.Background(), fastDownloader(), cfg, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to serialize name index")
	settleGoroutines(t, base)

	for _, file := range []string{cfg.S2.IndexFile, cfg.PostalCodeIndexFile} {
		_, statErr := os.Stat(filepath.Join(dir, file))
		assert.NoError(t, statErr, "the sibling write %s must have completed", file)
	}
}
