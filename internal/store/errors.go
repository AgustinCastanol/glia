package store

import "errors"

var (
	ErrNotFound      = errors.New("store: canonical_id not found")
	ErrDeleted       = errors.New("store: canonical_id is tombstoned")
	ErrLocked        = errors.New("store: another process holds the lock")
	ErrSchemaTooNew  = errors.New("store: schema.json version is newer than supported")
	ErrInvalidRecord = errors.New("store: record failed validation")
)
