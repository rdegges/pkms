// Package lock provides non-blocking per-vault flock guards so overlapping
// cron runs never double-mutate (SPEC §7).
package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock is a held file lock.
type Lock struct {
	f *os.File
}

// ErrHeld reports the lock is already taken by another process.
type ErrHeld struct{ Path string }

func (e ErrHeld) Error() string {
	return fmt.Sprintf("another pkms run holds %s; exiting", e.Path)
}

// Acquire takes an exclusive non-blocking flock on path.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrHeld{Path: path}
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	defer l.f.Close()
	return syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
}
