//go:build unix

package initializer

import (
	"errors"
	"os"
	"syscall"
)

// lockExclusive takes a non-blocking exclusive flock on f. The kernel drops
// it when the holding process exits for ANY reason (crash, OOM kill,
// SIGKILL), so a dead initializer can never leave a lock behind — unlike a
// pid file, whose pid a restarted container reuses (the server runs as PID 1)
// and which therefore looked "alive" forever after an OOM kill.
func lockExclusive(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLockHeld
	}
	return err
}
