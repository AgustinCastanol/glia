package store

import (
	"errors"
	"testing"
)

func TestErrCorrupt_ErrorsIs(t *testing.T) {
	if !errors.Is(ErrCorrupt, ErrCorrupt) {
		t.Fatal("errors.Is(ErrCorrupt, ErrCorrupt) must be true")
	}
}

func TestErrSchemaMismatch_ErrorsIs(t *testing.T) {
	if !errors.Is(ErrSchemaMismatch, ErrSchemaMismatch) {
		t.Fatal("errors.Is(ErrSchemaMismatch, ErrSchemaMismatch) must be true")
	}
}

func TestErrSchemaTooNew_WrapsErrSchemaMismatch(t *testing.T) {
	// ErrSchemaTooNew is a specific case of ErrSchemaMismatch; both must match.
	if !errors.Is(ErrSchemaTooNew, ErrSchemaMismatch) {
		t.Fatal("errors.Is(ErrSchemaTooNew, ErrSchemaMismatch) must be true — ErrSchemaTooNew must wrap ErrSchemaMismatch")
	}
	if !errors.Is(ErrSchemaTooNew, ErrSchemaTooNew) {
		t.Fatal("errors.Is(ErrSchemaTooNew, ErrSchemaTooNew) must be true")
	}
}

func TestErrCorrupt_NotErrSchemaMismatch(t *testing.T) {
	if errors.Is(ErrCorrupt, ErrSchemaMismatch) {
		t.Fatal("ErrCorrupt must NOT be ErrSchemaMismatch")
	}
}
