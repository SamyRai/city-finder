package initializer

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// foreignOwnedOpen stands in for a lock file created 0600 by another uid:
// opening it read-write is refused, opening it read-only works.
func foreignOwnedOpen(name string, flag int, perm os.FileMode) (*os.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.EACCES}
	}
	return os.OpenFile(name, flag, perm)
}

// TestAcquireInitLock_WritableFolderForeignLockFileStillLocks: a warm boot can
// still write (corrupt-index rebuild, format migration), so a lock file we
// cannot open read-write in a WRITABLE folder must be locked read-only rather
// than skipped; otherwise two boots run unlocked and race on <index>.part.
func TestAcquireInitLock_WritableFolderForeignLockFileStillLocks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, initLockName), nil, 0o600))

	release, err := acquireInitLockVia(foreignOwnedOpen, folderWritable, dir, false)
	require.NoError(t, err)
	defer release()

	_, err = acquireInitLock(dir, false)
	require.Error(t, err, "a second boot must be refused while the first holds the lock")
	assert.Contains(t, err.Error(), "another initializer")
}

// unwritable is a folder probe for a read-only mount, which root (and so a CI
// container) cannot produce with chmod.
func unwritable(string) bool { return false }

// TestAcquireInitLock_WritableFolderUnopenableLockFails: when the folder is
// writable but the lock file cannot be opened even read-only, running
// unlocked would race, so the boot fails with a clear error.
func TestAcquireInitLock_WritableFolderUnopenableLockFails(t *testing.T) {
	for _, writesNeeded := range []bool{false, true} {
		_, err := acquireInitLockVia(refuseOpen(syscall.EACCES), folderWritable, t.TempDir(), writesNeeded)
		require.Error(t, err)
		assert.ErrorIs(t, err, syscall.EACCES)
		assert.Contains(t, err.Error(), "init lock")
	}
}

func TestFolderWritable(t *testing.T) {
	dir := t.TempDir()
	assert.True(t, folderWritable(dir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the probe must clean up after itself")
	assert.False(t, folderWritable(filepath.Join(dir, "missing")))
}
