package initializer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
func acquireInitLock(datasetsFolder string) (release func(), err error) {
	lockPath := filepath.Join(datasetsFolder, initLockName)
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
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
		return nil, fmt.Errorf("failed to lock %s: %w", lockPath, err)
	}
	// Best-effort pid hint for the error message above; the lock itself is
	// the flock, not this content.
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return func() { _ = f.Close() }, nil // closing the descriptor releases the flock
}

// readLockPID parses the pid hint stored in the lock file.
func readLockPID(lockPath string) (int, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}
