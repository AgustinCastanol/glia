// Package adapter defines the provider-agnostic contract for memory adapters.
// Each adapter maps a native provider (e.g. engram) to and from the canonical
// store representation (store.CanonicalRecord).
//
// Import direction (enforced): internal/adapter/engram → internal/adapter → internal/store.
// internal/store MUST NOT import internal/adapter.
package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/agustincastanol/glia/internal/store"
)

// NativeID is an opaque, provider-specific record identifier.
type NativeID string

// CanonicalID is a ULID assigned by the canonical store (consistent with PRD-0).
type CanonicalID string

// NativeRecord is the concrete record type owned by each adapter implementation.
type NativeRecord any

// Sentinel errors returned by adapter methods.
var (
	// ErrUnsupported is returned when the operation is not supported by this provider.
	ErrUnsupported = errors.New("adapter: operation unsupported by this provider")

	// ErrNotFound is returned when the native record does not exist in the provider.
	ErrNotFound = errors.New("adapter: native record not found")

	// ErrUnavailable is returned when the provider is not reachable.
	ErrUnavailable = errors.New("adapter: provider not reachable")
)

// IDMap is a read-only bidirectional mapping between provider-native IDs and
// canonical IDs. It is satisfied by *store.providerIDMap without the store
// importing this package — structural compatibility is enforced at call sites.
//
// All methods are read-only; no implementation may mutate state.
type IDMap interface {
	// CanonicalFromNative returns the canonical ID for the given native ID.
	// Returns ("", false) if no mapping exists.
	CanonicalFromNative(NativeID) (CanonicalID, bool)

	// NativeFromCanonical returns the native ID for the given canonical ID.
	// Returns ("", false) if no mapping exists.
	NativeFromCanonical(CanonicalID) (NativeID, bool)
}

// Adapter is the provider-agnostic interface that every memory adapter must implement.
//
// Purity contract:
//   - ToCanonical and FromCanonical are pure functions: they MUST NOT perform I/O
//     or have observable side effects.
//   - ListNative, ReadNative, WriteNative, and Health are the only methods permitted
//     to interact with the provider.
type Adapter interface {
	// Name returns the stable, lowercase provider identifier (e.g. "engram").
	// This value is stored as origin.provider in every CanonicalRecord.
	Name() string

	// Health performs a single read-only probe of the provider.
	// Returns nil if the provider is reachable, ErrUnavailable (wrapped) otherwise.
	Health(ctx context.Context) error

	// ListNative returns all project-scoped native IDs updated at or after since.
	// Personal-scope records MUST be filtered here and never reach the canonical store.
	ListNative(ctx context.Context, project string, since time.Time) ([]NativeID, error)

	// ReadNative retrieves the full native record for id.
	// Returns ErrNotFound if the record does not exist.
	// Returns ErrUnavailable if the provider is unreachable.
	ReadNative(ctx context.Context, id NativeID) (NativeRecord, error)

	// ToCanonical converts a native record to a store.CanonicalRecord using idmap
	// for ID resolution. Pure: no I/O. Returns ErrUnsupported for relation records.
	ToCanonical(native NativeRecord, idmap IDMap) (store.CanonicalRecord, error)

	// FromCanonical converts a store.CanonicalRecord to a native record.
	// Pure: no I/O. Returns ErrUnsupported for relation records.
	FromCanonical(canonical store.CanonicalRecord) (NativeRecord, error)

	// WriteNative writes the native record to the provider and returns its NativeID.
	// Idempotent: if a record with the same origin.provider_id already exists,
	// it MUST be updated in place rather than duplicated.
	WriteNative(ctx context.Context, record NativeRecord) (NativeID, error)

	// SupportedKinds returns the set of canonical record kinds this adapter can
	// handle via FromCanonical/WriteNative. An empty slice means "all kinds".
	// Pull uses this to skip unsupported record types before calling FromCanonical.
	SupportedKinds() []string

	// WriteCapability returns a human-readable string describing this adapter's
	// write support. Possible values:
	//   "read+write"                                  — writes are fully supported
	//   "read-only (write_enabled=false)"             — disabled by config
	//   "read-only (worker missing POST /api/memory/save)" — endpoint probe returned false
	//   "read-only"                                   — provider has no write surface
	// REQ-CMW-03.
	WriteCapability() string
}

// WriteCapabilityAdapter is a named helper type used in compile-time assertions
// to verify that WriteCapability() string satisfies the expected interface shape.
// It is never instantiated at runtime.
type WriteCapabilityAdapter struct{ Adapter }
