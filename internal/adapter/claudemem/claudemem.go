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
	"log"
	"os"
	"time"
	"unicode/utf8"

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

// ---------------------------------------------------------------------------
// T-06: authorAttribution (verbatim copy from engram adapter — ADR-8).
// ---------------------------------------------------------------------------

// authorAttribution returns the value to use for origin.author per REQ-ENG-19:
//  1. WRAPPER_MEMS_AUTHOR env var if non-empty.
//  2. USER@hostname (best-effort; uses whichever parts are available).
func authorAttribution() string {
	if v := os.Getenv("WRAPPER_MEMS_AUTHOR"); v != "" {
		return v
	}
	user := os.Getenv("USER")
	host, _ := os.Hostname()
	switch {
	case user != "" && host != "":
		return user + "@" + host
	case user != "":
		return user
	case host != "":
		return "@" + host
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// T-06: providerIDMapWrapper + WrapIDMap (verbatim copy from engram adapter — ADR-7).
// ---------------------------------------------------------------------------

// providerIDMapWrapper wraps *store.providerIDMap and adapts its plain-string
// method signatures to the adapter.IDMap interface, which uses named types
// adapter.NativeID and adapter.CanonicalID. This is the W-01 boundary wrapper:
//
//   - store.providerIDMap.CanonicalFromNative(string) (string, bool) — plain strings
//   - adapter.IDMap.CanonicalFromNative(NativeID) (CanonicalID, bool) — named types
//
// In Go, named types are NOT interface-assignable to their underlying type, so
// we cast at the boundary here. internal/store is kept free of internal/adapter
// (CON-01).
type providerIDMapWrapper struct {
	inner interface {
		CanonicalFromNative(string) (string, bool)
		NativeFromCanonical(string) (string, bool)
	}
}

// CanonicalFromNative adapts the named-type NativeID to the underlying string.
func (w *providerIDMapWrapper) CanonicalFromNative(id adapter.NativeID) (adapter.CanonicalID, bool) {
	v, ok := w.inner.CanonicalFromNative(string(id))
	return adapter.CanonicalID(v), ok
}

// NativeFromCanonical adapts the named-type CanonicalID to the underlying string.
func (w *providerIDMapWrapper) NativeFromCanonical(id adapter.CanonicalID) (adapter.NativeID, bool) {
	v, ok := w.inner.NativeFromCanonical(string(id))
	return adapter.NativeID(v), ok
}

// WrapIDMap wraps a *store.providerIDMap (or any struct with equivalent
// plain-string method signatures) as an adapter.IDMap, casting at the boundary
// (W-01 resolution). The inner argument must expose
// CanonicalFromNative/NativeFromCanonical with plain string signatures.
func WrapIDMap(inner interface {
	CanonicalFromNative(string) (string, bool)
	NativeFromCanonical(string) (string, bool)
}) adapter.IDMap {
	return &providerIDMapWrapper{inner: inner}
}

// ---------------------------------------------------------------------------
// T-07: rfc3339NanoFixed, normalizeTimestamp, tolerant multi-layout parse (ADR-6).
// ---------------------------------------------------------------------------

// rfc3339NanoFixed is an explicit 9-digit nanosecond format with UTC Z suffix.
// time.RFC3339Nano truncates trailing zeros (e.g. "2026-05-16T01:33:00Z" instead
// of "2026-05-16T01:33:00.000000000Z"), which breaks the byte-comparable invariant
// required by tiebreakWinner in rebuild.go (REQ-TS-03, CON-05).
const rfc3339NanoFixed = "2006-01-02T15:04:05.000000000Z"

// claudeMemSQLiteLayout is the SQLite datetime format ("2006-01-02 15:04:05", UTC)
// observed in real engram exports (DEFECT-LN-01). claude-mem may use the same
// storage layer, so we accept it as a fallback (ADR-6).
const claudeMemSQLiteLayout = "2006-01-02 15:04:05"

// normalizeTimestamp parses a timestamp using the accepted layout set (ADR-6):
//  1. time.RFC3339Nano (canonical, with nanoseconds).
//  2. time.RFC3339 (without nanoseconds).
//  3. claudeMemSQLiteLayout ("2006-01-02 15:04:05", implicitly UTC).
//
// On success, re-formats as rfc3339NanoFixed (UTC, 9-digit nanoseconds) to
// protect the REQ-TS-03 byte-comparable invariant.
//
// Unknown format → returns a wrapped error with %w so callers can errors.Is/As
// the underlying parse failure (ADR-6: NON-SILENT, composable failure).
// ToCanonical rejects the record; ListNative skips + warns.
// Never silently store an unnormalized timestamp.
func normalizeTimestamp(ts string) (string, error) {
	if ts == "" {
		return "", nil
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339, claudeMemSQLiteLayout}
	var lastErr error
	for _, layout := range layouts {
		t, err := time.Parse(layout, ts)
		if err == nil {
			return t.UTC().Format(rfc3339NanoFixed), nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("normalizeTimestamp: cannot parse %q: no accepted layout matched (tried RFC3339Nano, RFC3339, SQLite datetime): %w", ts, lastErr)
}

// titleMaxRunes is the maximum number of Unicode code points used when deriving
// a Title from Summary (REQ-CM-12). Kept intentionally short so titles remain
// scannable; PRD-3 may override this at the orchestration layer.
const titleMaxRunes = 80

// deriveTitle returns the first titleMaxRunes runes of summary, or "" if summary
// is empty. Used when ClaudeMemRecord.Title is absent (REQ-CM-12).
func deriveTitle(summary string) string {
	if summary == "" {
		return ""
	}
	if utf8.RuneCountInString(summary) <= titleMaxRunes {
		return summary
	}
	// Slice at the titleMaxRunes-th rune boundary.
	i := 0
	for n := 0; n < titleMaxRunes; n++ {
		_, size := utf8.DecodeRuneInString(summary[i:])
		i += size
	}
	return summary[:i]
}

// ---------------------------------------------------------------------------
// Compile-time assertion: ClaudeMemAdapter must satisfy adapter.Adapter (REQ-CM-01, CON-03).
// ---------------------------------------------------------------------------

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
//
// Field mapping follows REQ-CM-11 (PROVISIONAL — pre-merge gate applies):
//   - id             → Origin.ProviderID
//   - session_id     → Origin.SessionID
//   - title          → Title (derived from first N chars of summary when empty)
//   - summary        → Content; ContentFormat always "markdown"
//   - tags           → Tags
//   - created_at     → CreatedAt (normalized via rfc3339NanoFixed)
//   - updated_at     → UpdatedAt (normalized via rfc3339NanoFixed)
//   - Kind           always "session_summary" (ADR-9)
//   - Origin.Provider always "claude-mem" (REQ-CM-02)
//   - Origin.Author  authorAttribution()
//   - CanonicalID    IDMap lookup; "" when new (store mints ULID on Append)
//   - Revision       1 if new; priorRevision+1 if known (REQ-CM-14)
//   - SchemaVersion  NOT set — store owns it (REQ-CM-11)
func (a *ClaudeMemAdapter) ToCanonical(native adapter.NativeRecord, idmap adapter.IDMap) (store.CanonicalRecord, error) {
	rec, ok := native.(claudeMemRecord)
	if !ok {
		return store.CanonicalRecord{}, fmt.Errorf("ToCanonical: expected claudeMemRecord, got %T", native)
	}

	// REQ-CM-13: forensic warn when both title and summary are empty.
	if rec.Title == "" && rec.Summary == "" {
		log.Printf("claude-mem adapter: WARN observation id=%q has empty title and summary; raw=%s", rec.ID, rec.Raw)
	}

	// REQ-CM-12: derive title from summary when absent.
	title := rec.Title
	if title == "" {
		title = deriveTitle(rec.Summary)
	}

	// REQ-CM-16: normalize timestamps — reject record on unknown format (ADR-6).
	createdAt, err := normalizeTimestamp(rec.CreatedAt)
	if err != nil {
		return store.CanonicalRecord{}, fmt.Errorf("ToCanonical: %w", err)
	}
	updatedAt, err := normalizeTimestamp(rec.UpdatedAt)
	if err != nil {
		return store.CanonicalRecord{}, fmt.Errorf("ToCanonical: %w", err)
	}

	// REQ-CM-19: ID resolution via IDMap.
	nativeID := adapter.NativeID(rec.ID)
	canonicalID, hasMapping := idmap.CanonicalFromNative(nativeID)

	// REQ-CM-14: revision — 1 for new records; sentinel for known records.
	//
	// Shared cross-adapter revision sentinel convention (ADR-7/ADR-8 spirit):
	//   revision == -1  means "known record; PRD-3 orchestrator must replace
	//                   with priorRevision+1". Consistent with the engram adapter
	//                   (internal/adapter/engram/engram.go ~376-378).
	//   revision ==  1  means genuinely new record (no prior IDMap mapping).
	//
	// The IDMap carries only the ID→CanonicalID mapping; the actual current
	// revision cannot be read without a store lookup (adapter is pure — no I/O).
	// The orchestrator (PRD-3) owns the final revision value on re-ingest.
	//
	// DECISION 2026-05-18 (RESOLVED): shared cross-adapter revision sentinel.
	// design §6 / ADR-12 (obs #77) and spec REQ-CM-11/14 (obs #76) were
	// reconciled to this convention: new→1, known→-1, PRD-3 replaces -1 with
	// priorRevision+1. This is identical to the engram adapter (engram.go ~378)
	// so PRD-3 handles all adapters through one uniform `revision < 0` branch.
	revision := 1
	if hasMapping {
		// Known record: signal "orchestrator must assign final revision".
		// -1 is the shared sentinel matching the engram adapter's convention.
		revision = -1
	}

	// Ensure tags is never nil (empty slice is more correct than nil for JSON).
	tags := rec.Tags
	if tags == nil {
		tags = []string{}
	}

	return store.CanonicalRecord{
		CanonicalID:   string(canonicalID), // "" when new — store mints ULID on Append
		Kind:          "session_summary",   // ALWAYS — ADR-9, D4
		Revision:      revision,
		Title:         title,
		Content:       rec.Summary,
		ContentFormat: "markdown",
		Type:          "",       // claude-mem has no engram-style type vocabulary
		TopicKey:      "",       // not applicable
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		Tags:          tags,
		Origin: store.Origin{
			Provider:   "claude-mem", // ALWAYS — REQ-CM-02
			ProviderID: rec.ID,
			Author:     authorAttribution(),
			SessionID:  rec.SessionID,
		},
		// SchemaVersion: NOT set — store.Append owns this (REQ-CM-11).
	}, nil
}

// FromCanonical converts a store.CanonicalRecord to a native claudeMemRecord.
// Pure: no I/O (REQ-CM-15). Implemented (not stubbed) so that v1.1 bidirectional
// reuse can use it verbatim. NEVER passed to WriteNative in v1 (ADR-10).
//
// NOTE: CreatedAt and UpdatedAt are intentionally omitted from the output.
// claude-mem is read-only in v1 — there is no write surface and timestamps are
// never persisted back. Any future write path (v1.1+) MUST re-read CreatedAt/
// UpdatedAt from the provider, not round-trip from FromCanonical output
// (FromCanonical omits timestamps by design).
func (a *ClaudeMemAdapter) FromCanonical(canonical store.CanonicalRecord) (adapter.NativeRecord, error) {
	return claudeMemRecord{
		ID:        canonical.Origin.ProviderID,
		SessionID: canonical.Origin.SessionID,
		Title:     canonical.Title,
		Summary:   canonical.Content,
		Tags:      canonical.Tags,
		// CreatedAt/UpdatedAt intentionally omitted — see doc comment above.
	}, nil
}

// WriteNative always returns ErrUnsupported. claude-mem has no public write
// surface in v1 (REQ-CM-03, ADR-10). No I/O, no state mutation.
//
// NOTE: any future write path MUST re-read CreatedAt/UpdatedAt from the
// provider, not round-trip from FromCanonical output (FromCanonical omits
// timestamps by design).
func (a *ClaudeMemAdapter) WriteNative(ctx context.Context, record adapter.NativeRecord) (adapter.NativeID, error) {
	return "", fmt.Errorf("%w", adapter.ErrUnsupported)
}
