package initializer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteAll_RunsWritesConcurrently: two writes that each wait for the
// other can only finish if they run at the same time.
func TestWriteAll_RunsWritesConcurrently(t *testing.T) {
	aReady, bReady := make(chan struct{}), make(chan struct{})
	rendezvous := func(mine, theirs chan struct{}) pendingWrite {
		return func() error {
			close(mine)
			select {
			case <-theirs:
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("the other write never started: writes are sequential")
			}
		}
	}
	require.NoError(t, writeAll(context.Background(), rendezvous(aReady, bReady), nil, rendezvous(bReady, aReady)))
}

// TestWriteAll_ReturnsFirstErrorAfterAllFinish: a failing write does not
// abandon its siblings, and nothing is still running when writeAll returns.
func TestWriteAll_ReturnsFirstErrorAfterAllFinish(t *testing.T) {
	base := runtime.NumGoroutine()
	boom := errors.New("boom")
	done := make(chan struct{})
	slow := func() error {
		time.Sleep(30 * time.Millisecond)
		close(done)
		return nil
	}
	err := writeAll(context.Background(), slow, func() error { return boom })
	require.ErrorIs(t, err, boom)
	select {
	case <-done:
	default:
		t.Fatal("writeAll returned before the slow write finished")
	}
	settleGoroutines(t, base)
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

// TestLoadData_PostalFailureDrainsAndCitiesErrorWins: the two parses run
// concurrently; a failing one never leaves the other running, and when both
// fail the city error is the one reported, as when they ran in sequence.
func TestLoadData_PostalFailureDrainsAndCitiesErrorWins(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.PostalCodesFile)))

	base := runtime.NumGoroutine()
	_, _, err := loadData(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Postal Code")
	settleGoroutines(t, base)

	require.NoError(t, os.Remove(filepath.Join(dir, cfg.AllCitiesFile)))
	_, _, err = loadData(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GeoNames data")
	settleGoroutines(t, base)
}

// TestLoadData_CancelledContextStartsNothing: a cancelled boot does not parse
// multi-GB files.
func TestLoadData_CancelledContextStartsNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := loadData(ctx, cfg)
	require.ErrorIs(t, err, context.Canceled)
}
