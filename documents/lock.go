package documents

import (
	"errors"
	"fmt"
	"os"

	"github.com/mycophonic/primordium/fault"
	"github.com/mycophonic/primordium/filesystem"
	"github.com/mycophonic/primordium/filesystem/flock"
)

// Locked runs function holding the documents directory's advisory lock:
// exclusive for a command that writes the files or the console, shared for
// one that only reads, so two plans run together and a plan waits for an
// apply. Two applies at once would plan from the same snapshot and both
// create the same documents; in turn, the second finds the first's work
// and does nothing. The lock is this host's: it orders the invocations on
// one checkout. The directory is the lock, so Locked creates it when it is
// missing: dump takes it before the first file exists.
func Locked(dir string, exclusive bool, function func() error) error {
	if err := os.MkdirAll(dir, filesystem.DirPermissionsDefault); err != nil {
		return fmt.Errorf("%w: creating %s: %w", fault.ErrWriteFailure, dir, err)
	}

	defer flock.Cleanup(dir)

	with := flock.WithReadOnlyLock
	if exclusive {
		with = flock.WithLock
	}

	err := with(dir, function)
	if errors.Is(err, flock.ErrLockFail) {
		return fmt.Errorf("locking %s: %w", dir, err)
	}

	return err
}
