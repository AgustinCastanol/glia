package store

import (
	"fmt"

	"github.com/gofrs/flock"
)

type fileLock struct {
	fl   *flock.Flock
	path string
}

func newFileLock(path string) *fileLock {
	return &fileLock{fl: flock.New(path), path: path}
}

func (l *fileLock) tryAcquire() error {
	ok, err := l.fl.TryLock()
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	if !ok {
		return fmt.Errorf("store: lock held at %s: %w", l.path, ErrLocked)
	}
	return nil
}

func (l *fileLock) release() error {
	return l.fl.Unlock()
}
