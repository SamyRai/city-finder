package initializer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refuseOpen is an open function whose every call fails with err, standing
// in for a read-only mount or an unwritable directory (which root, and so a
// CI container, cannot produce with chmod).
func refuseOpen(err error) openFileFunc {
	return func(name string, _ int, _ os.FileMode) (*os.File, error) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
}

// TestAcquireInitLock_UnwritableFolderDegradesWhenNothingIsWritten: with every
// index present the boot writes nothing but best-effort migrations that fail
// harmlessly, and with the folder unwritable no other initializer can be
// writing through this mount, so there is nothing to serialize against. The
// lock is skipped, not fatal.
func TestAcquireInitLock_UnwritableFolderDegradesWhenNothingIsWritten(t *testing.T) {
	for name, cause := range map[string]error{"read-only fs": syscall.EROFS, "permission": syscall.EACCES} {
		release, err := acquireInitLockVia(refuseOpen(cause), t.TempDir(), false)
		require.NoError(t, err, name)
		require.NotNil(t, release, name)
		release() // must be callable
	}
}

// TestAcquireInitLock_UnwritableFolderFailsWhenBuilding: a boot that has to
// build an index writes the same paths a concurrent builder would, so an
// unlockable folder stays a clear, fatal error.
func TestAcquireInitLock_UnwritableFolderFailsWhenBuilding(t *testing.T) {
	_, err := acquireInitLockVia(refuseOpen(syscall.EROFS), t.TempDir(), true)
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.EROFS)
	assert.Contains(t, err.Error(), "init lock")
}

// TestAcquireInitLock_OtherErrorsAreNotDegraded: only "cannot write here"
// justifies running unlocked. A folder that is not a directory is a broken
// setup and must still fail (the parent-is-a-regular-file trick, which
// behaves the same for root).
func TestAcquireInitLock_OtherErrorsAreNotDegraded(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	_, err := acquireInitLock(filepath.Join(blocker, "datasets"), false)
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ENOTDIR)

	_, err = acquireInitLockVia(refuseOpen(syscall.EIO), t.TempDir(), false)
	require.Error(t, err)
}

// TestInitialize_ReadOnlyFolderWithAllIndexes: a datasets folder that cannot
// be written (an immutable image or a read-only mount) boots from complete
// prebuilt indexes. Needs a non-root user: root ignores directory modes.
func TestInitialize_ReadOnlyFolderWithAllIndexes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; the degrade path is covered with an injected open")
	}
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)
	_, err := ensureFinders(context.Background(), fastDownloader(), cfg, "")
	require.NoError(t, err)

	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	f, err := Initialize(cfg)
	require.NoError(t, err, "a warm boot must not need to write the datasets folder")
	require.NotNil(t, f.S2Finder)
	require.NotNil(t, f.NameFinder)
	require.NotNil(t, f.PostalCodeFinder)
}

// TestInitialize_ReadOnlyFolderMissingIndexFails: with an index missing the
// boot needs to write, and fails with the lock error rather than half-working.
func TestInitialize_ReadOnlyFolderMissingIndexFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := Initialize(cfg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrPermission), "got %v", err)
}
