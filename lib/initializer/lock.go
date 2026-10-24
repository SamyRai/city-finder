package initializer

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// initLockName is the lock file serializing initializers per datasets folder.
const initLockName = ".cityfinder-init.lock"

// errLockHeld reports that another live process holds the init lock.
var errLockHeld = errors.New("init lock held by another process")

// acquireInitLock guards a datasets folder against concurrent boots. Two
// cold-booting processes otherwise write the same fixed "<index>.gob.part"
// paths: the second os.Create truncates the first's in-flight part file and
// both rename, leaving interleaved garbage (a real scenario under a rolling
// update with maxSurge, where two pods share one PVC).
//
// The lock is an exclusive flock on the lock file (see lockExclusive): the
// kernel releases it when the holder dies, so there is no stale-lock
// detection to get wrong. (The previous pid-file lock refused forever after
// an OOM-killed cold build: the restarted container is PID 1 again, and its
// own pid in the file looked like a live foreign owner.) The file stays in
// place between boots and holds the owner's pid as a diagnostic hint only.
// Living holder → fail fast: the caller exits and the orchestrator restarts
// it once the first boot finishes.
//
// writesNeeded says whether this boot is certain to write into the folder
// (some index is missing, so it builds and serializes). Even a warm boot can
// write: a corrupt index is rebuilt and the name/postal formats are migrated
// in place, both through the same fixed "<index>.part" paths a concurrent
// boot would use. So an open failure alone never justifies running unlocked:
// a lock file created 0600 by another uid on a writable shared volume fails
// to open read-write while the folder stays fully writable to us.
//
// The degradation order is therefore:
//  1. open the lock read-write (the normal case);
//  2. the open says "cannot write" but the FOLDER is writable: open the lock
//     read-only and flock that descriptor (flock needs no write access, and
//     it conflicts with the other process's read-write descriptor). This
//     works because the lock is created 0644. It cannot work for a 0600 file
//     another uid created (as releases before 1.5 did), nor on NFS, where
//     flock is emulated with POSIX locks that need a writable descriptor. If
//     the read-only path fails, a boot with every index present continues
//     unlocked with a warning (the pre-1.5 behaviour), and a boot that must
//     build fails;
//  3. the open says "cannot write" and the FOLDER is not writable (read-only
//     mount, immutable image, restrictive mode): no other initializer can be
//     writing through this mount, index files are replaced by atomic rename
//     so a reader never sees a partial file, and the warm boot's only writes
//     fail harmlessly. With every index present the boot continues unlocked;
//     if it must build, it fails with a clear error.
//
// Any other open failure still fails the boot.
func acquireInitLock(datasetsFolder string, writesNeeded bool) (release func(), err error) {
	return acquireInitLockVia(os.OpenFile, folderWritable, datasetsFolder, writesNeeded)
}

// openFileFunc is os.OpenFile's signature, so the unwritable-folder path can
// be tested as root, where no real folder is unwritable.
type openFileFunc func(name string, flag int, perm os.FileMode) (*os.File, error)

// folderWritableFunc reports whether this process can create files in dir.
type folderWritableFunc func(dir string) bool

// folderWritable probes dir by creating and removing a temp file. Probing
// beats an access(2) check: it also sees read-only mounts and ACLs, and it
// behaves the same on every platform.
func folderWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".cityfinder-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func acquireInitLockVia(open openFileFunc, writable folderWritableFunc, datasetsFolder string, writesNeeded bool) (release func(), err error) {
	lockPath := filepath.Join(datasetsFolder, initLockName)
	// 0644, not 0600: another uid sharing the volume must be able to open the
	// lock read-only and flock it (step 2 above). The content is a pid hint.
	f, err := open(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	readOnly := false
	if err != nil && cannotWrite(err) {
		if writable(datasetsFolder) {
			// The folder is ours to write but the lock file is not (another
			// uid's file): lock it through a read-only descriptor.
			var roErr error
			if f, roErr = open(lockPath, os.O_RDONLY, 0); roErr != nil {
				return unlockedOrFail(lockPath, writesNeeded, fmt.Errorf("read-write (%v) or read-only: %w", err, roErr))
			}
			err, readOnly = nil, true
		} else if !writesNeeded {
			log.Printf("datasets folder %s is not writable (%v) and every index is present: continuing without the init lock", datasetsFolder, err)
			return func() {}, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open init lock %s: %w", lockPath, err)
	}
	if err := lockExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockHeld) {
			holder := "unknown pid"
			if pid, perr := readLockPID(lockPath); perr == nil {
				holder = fmt.Sprintf("pid %d", pid)
			}
			return nil, fmt.Errorf("another initializer (%s) is running against datasets folder %s; refusing to race it", holder, datasetsFolder)
		}
		if readOnly {
			// NFS: an exclusive flock on a read-only descriptor fails.
			return unlockedOrFail(lockPath, writesNeeded, fmt.Errorf("read-only flock: %w", err))
		}
		return nil, fmt.Errorf("failed to lock %s: %w", lockPath, err)
	}
	// Best-effort pid hint for the error message above; the lock itself is
	// the flock, not this content.
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return func() { _ = f.Close() }, nil // closing the descriptor releases the flock
}

// unlockedOrFail handles a lock file this process cannot lock in a writable
// folder. A boot that must build fails: it would write indexes unlocked. A
// boot with every index present continues unlocked, as releases before 1.5
// did, and only races another boot if both must rebuild or migrate a file
// (the CRC then rejects an interleaved write and the next boot rebuilds).
// The warning names the fix.
func unlockedOrFail(lockPath string, writesNeeded bool, cause error) (func(), error) {
	if writesNeeded {
		return nil, fmt.Errorf("cannot lock init lock %s (%w); fix its owner or mode, or remove it while no initializer runs", lockPath, cause)
	}
	log.Printf("WARNING: cannot lock init lock %s (%v); every index is present, continuing without the lock. Fix its owner or mode, or remove it while no initializer runs", lockPath, cause)
	return func() {}, nil
}

// cannotWrite reports an error that means the location is not writable by
// this process: permission denied or a read-only filesystem.
func cannotWrite(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}

// readLockPID parses the pid hint stored in the lock file.
func readLockPID(lockPath string) (int, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}
