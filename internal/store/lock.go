package store

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ErrLocked means another blurip process holds the lock.
var ErrLocked = errors.New("another blurip is already running with this database")

// Lock is an exclusive lock that only one blurip process can hold per
// database. It is released when the process exits, even if it crashes.
type Lock struct{ f *os.File }

// AcquireLock takes the lock for the database at dbPath without waiting.
func AcquireLock(dbPath string) (*Lock, error) {
	f, err := os.OpenFile(dbPath+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("locking: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release releases the lock.
func (l *Lock) Release() error { return l.f.Close() }
