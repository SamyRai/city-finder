package initializer

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// foreignOwnedOpen stands in for a lock file another uid created 0644 (the
// mode this package creates it with): opening it read-write is refused,
// opening it read-only works.
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

// TestAcquireInitLock_WritableFolderUnopenableLock: the folder is writable
// but the lock file cannot be opened even read-only (another uid's 0600 file
// from a release before 1.5). A boot that must build fails rather than write
// unlocked; a warm boot with every index present continues unlocked, as
// before 1.5, instead of crash-looping on a permission problem.
func TestAcquireInitLock_WritableFolderUnopenableLock(t *testing.T) {
	_, err := acquireInitLockVia(refuseOpen(syscall.EACCES), folderWritable, t.TempDir(), true)
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.EACCES)
	assert.Contains(t, err.Error(), "cannot lock init lock")

	release, err := acquireInitLockVia(refuseOpen(syscall.EACCES), folderWritable, t.TempDir(), false)
	require.NoError(t, err)
	require.NotNil(t, release)
	release()
}

// TestAcquireInitLock_CreatesLockWorldReadable: another uid can only take the
// read-only flock if it can read the file, so the lock is created 0644.
func TestAcquireInitLock_CreatesLockWorldReadable(t *testing.T) {
	var created os.FileMode
	open := func(name string, flag int, perm os.FileMode) (*os.File, error) {
		if flag&os.O_CREATE != 0 {
			created = perm
		}
		return os.OpenFile(name, flag, perm)
	}
	release, err := acquireInitLockVia(open, folderWritable, t.TempDir(), true)
	require.NoError(t, err)
	defer release()
	assert.Equal(t, os.FileMode(0o644), created)
}

func TestFolderWritable(t *testing.T) {
	dir := t.TempDir()
	assert.True(t, folderWritable(dir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the probe must clean up after itself")
	assert.False(t, folderWritable(filepath.Join(dir, "missing")))
}
