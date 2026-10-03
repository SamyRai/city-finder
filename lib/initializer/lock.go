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
// writesNeeded says whether this boot will write into the folder (some
// index is missing, so it builds and serializes). When it will not, an
// unwritable folder (read-only mount, immutable image, restrictive mode)
// must not stop the boot: a warm start only reads, and its one write, the
// best-effort format migration, fails harmlessly there. Running unlocked is
// safe in that case because nothing can race: the folder is unwritable
// through this mount, and index files are replaced by atomic rename, so a
// reader never sees a partial file. Only "cannot write here" errors take
// this path; any other failure to open the lock still fails the boot.
func acquireInitLock(datasetsFolder string, writesNeeded bool) (release func(), err error) {
	return acquireInitLockVia(os.OpenFile, datasetsFolder, writesNeeded)
}

// openFileFunc is os.OpenFile's signature, so the unwritable-folder path can
// be tested as root, where no real folder is unwritable.
type openFileFunc func(name string, flag int, perm os.FileMode) (*os.File, error)

func acquireInitLockVia(open openFileFunc, datasetsFolder string, writesNeeded bool) (release func(), err error) {
	lockPath := filepath.Join(datasetsFolder, initLockName)
	f, err := open(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		if !writesNeeded && cannotWrite(err) {
			log.Printf("datasets folder %s is not writable (%v) and every index is present: continuing without the init lock", datasetsFolder, err)
			return func() {}, nil
		}
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
		return nil, fmt.Errorf("failed to lock %s: %w", lockPath, err)
	}
	// Best-effort pid hint for the error message above; the lock itself is
	// the flock, not this content.
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return func() { _ = f.Close() }, nil // closing the descriptor releases the flock
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
