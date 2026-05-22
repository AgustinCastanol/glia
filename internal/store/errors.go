package store

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound       = errors.New("store: canonical_id not found")
	ErrDeleted        = errors.New("store: canonical_id is tombstoned")
	ErrLocked         = errors.New("store: another process holds the lock")
	ErrInvalidRecord  = errors.New("store: record failed validation")
	ErrCorrupt        = errors.New("store: index/log corrupt")
	// ErrSchemaMismatch is the broad sentinel for any schema version mismatch.
	ErrSchemaMismatch = errors.New("store: schema version mismatch")
	// ErrSchemaTooNew wraps ErrSchemaMismatch: the on-disk version exceeds what
	// this binary supports. errors.Is(err, ErrSchemaTooNew) and
	// errors.Is(err, ErrSchemaMismatch) both return true.
	ErrSchemaTooNew   = fmt.Errorf("store: schema.json version is newer than supported: %w", ErrSchemaMismatch)
)
