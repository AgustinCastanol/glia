package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileLock_AcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")

	l := newFileLock(lockPath)
	err := l.tryAcquire()
	require.NoError(t, err)

	err = l.release()
	assert.NoError(t, err)
}

func TestFileLock_SecondLockReturnErrLocked(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")

	l1 := newFileLock(lockPath)
	err := l1.tryAcquire()
	require.NoError(t, err)
	defer l1.release()

	l2 := newFileLock(lockPath)
	err = l2.tryAcquire()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLocked))
}

func TestFileLock_ErrorsIsErrLocked(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")

	l1 := newFileLock(lockPath)
	require.NoError(t, l1.tryAcquire())
	defer l1.release()

	l2 := newFileLock(lockPath)
	err := l2.tryAcquire()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLocked), "expected errors.Is(err, ErrLocked) == true")
}

func TestFileLock_ReleaseAllowsReAcquire(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")

	l1 := newFileLock(lockPath)
	require.NoError(t, l1.tryAcquire())
	require.NoError(t, l1.release())

	l2 := newFileLock(lockPath)
	err := l2.tryAcquire()
	require.NoError(t, err)
	require.NoError(t, l2.release())
}
