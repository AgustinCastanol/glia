// Package claudemem implements adapter.Adapter for the claude-mem memory
// provider over HTTP (read-only v1, PRD-2).
//
// Intentional divergences from the engram adapter:
//   - D1: HTTP-only, no Commander seam (claude-mem has no usable CLI enumeration).
//   - D2: Read-only — WriteNative returns ErrUnsupported.
//   - D3: Health is HTTP GET /health, not a CLI version probe.
//   - D4: Kind is ALWAYS "session_summary", never "observation".
//   - D5: Provisional JSON field names — HARD PRE-MERGE GATE (see §5 comments).
//   - D6: ListNative paginates GET /api/observations instead of one GET /export dump.
package claudemem

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// claudeMemRecord is the PROVISIONAL internal representation of a single
// claude-mem observation as returned by GET /api/observations.
//
// PROVISIONAL: This struct and all its JSON tags are derived from PRD-2 §5/§6
// and have NOT been verified against a live claude-mem instance with ≥1
// observation. A HARD PRE-MERGE GATE (REQ-CM-20) blocks apply/merge until the
// field names below are reconfirmed against real data.
//
// Every json tag in this type carries a // PRE-MERGE GATE comment. Do NOT
// remove those comments until the integration test (T-16) has discharged the
// gate and field names are reconfirmed.
type claudeMemRecord struct {
	ID        string   `json:"id,omitempty"`          // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	ProjectID string   `json:"project_id,omitempty"`  // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	SessionID string   `json:"session_id,omitempty"`  // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	Title     string   `json:"title,omitempty"`       // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	Summary   string   `json:"summary,omitempty"`     // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	Tags      []string `json:"tags,omitempty"`        // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	CreatedAt string   `json:"created_at,omitempty"`  // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	UpdatedAt string   `json:"updated_at,omitempty"`  // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	// Raw captures the original JSON bytes for forensic inspection and field-name
	// reconfirmation (REQ-CM-17, design §9). Excluded from marshalling.
	Raw json.RawMessage `json:"-"`
}

// observationsPage is the PROVISIONAL envelope returned by GET /api/observations.
// The hasMore/offset/limit field names are also unverified — PRE-MERGE GATE applies.
type observationsPage struct {
	Items   []json.RawMessage `json:"items"`   // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	HasMore bool              `json:"hasMore"` // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	Offset  int               `json:"offset"`  // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
	Limit   int               `json:"limit"`   // PRE-MERGE GATE: provisional field name, reconfirm against live claude-mem
}

// Compile-time assertion: ClaudeMemAdapter must satisfy adapter.Adapter (REQ-CM-01, CON-03).
var _ adapter.Adapter = (*ClaudeMemAdapter)(nil)

// ClaudeMemAdapter implements adapter.Adapter for the claude-mem memory provider.
// It is safe for concurrent use after construction.
type ClaudeMemAdapter struct {
	transport Transport
}

// New constructs a ClaudeMemAdapter with the given Transport. transport is
// injected so that unit tests can substitute a fake without running a real
// daemon. Pass NewHTTPTransport("") for production use.
//
// Construction never fails: a nil transport is accepted and causes I/O methods
// to return adapter.ErrUnavailable without panicking (design §8).
func New(transport Transport) *ClaudeMemAdapter {
	return &ClaudeMemAdapter{transport: transport}
}

// Name returns "claude-mem" — the stable provider identifier stored in
// origin.provider of every canonical record (REQ-CM-02).
func (a *ClaudeMemAdapter) Name() string {
	return "claude-mem"
}

// Health probes the claude-mem daemon (REQ-CM-04).
// Returns nil on 2xx. Returns adapter.ErrUnavailable (wrapped) on connection
// failure or non-2xx. Context cancellation propagates raw.
// Returns adapter.ErrUnavailable immediately when the transport is nil.
func (a *ClaudeMemAdapter) Health(ctx context.Context) error {
	panic("not yet implemented in PR#1 — will be filled in PR#3 (T-12)")
}

// ListNative returns all project-scoped native IDs updated at or after since
// (REQ-CM-06, REQ-CM-07, REQ-CM-08).
// Returns adapter.ErrUnavailable immediately when the transport is nil.
func (a *ClaudeMemAdapter) ListNative(ctx context.Context, project string, since time.Time) ([]adapter.NativeID, error) {
	panic("not yet implemented in PR#1 — will be filled in PR#3 (T-13)")
}

// ReadNative retrieves the full native record for id (REQ-CM-09).
// Primary: GET /api/observations/:id. On ErrUnavailable (endpoint missing/5xx):
// degrades to paginate+scan. On ErrNotFound (clean 404): returns ErrNotFound.
// Returns adapter.ErrUnavailable immediately when the transport is nil.
func (a *ClaudeMemAdapter) ReadNative(ctx context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	panic("not yet implemented in PR#1 — will be filled in PR#3 (T-14)")
}

// ToCanonical converts a native claudeMemRecord to a store.CanonicalRecord
// using idmap for ID resolution. Pure: no I/O (REQ-CM-10).
func (a *ClaudeMemAdapter) ToCanonical(native adapter.NativeRecord, idmap adapter.IDMap) (store.CanonicalRecord, error) {
	panic("not yet implemented in PR#1 — will be filled in PR#2 (T-10)")
}

// FromCanonical converts a store.CanonicalRecord to a native claudeMemRecord.
// Pure: no I/O (REQ-CM-15). Implemented (not stubbed) so that v1.1 bidirectional
// reuse can use it verbatim. NEVER passed to WriteNative in v1 (ADR-10).
func (a *ClaudeMemAdapter) FromCanonical(canonical store.CanonicalRecord) (adapter.NativeRecord, error) {
	panic("not yet implemented in PR#1 — will be filled in PR#2 (T-09)")
}

// WriteNative always returns ErrUnsupported. claude-mem has no public write
// surface in v1 (REQ-CM-03, ADR-10). No I/O, no state mutation.
func (a *ClaudeMemAdapter) WriteNative(ctx context.Context, record adapter.NativeRecord) (adapter.NativeID, error) {
	return "", fmt.Errorf("%w", adapter.ErrUnsupported)
}
