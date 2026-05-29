// Package claudemem implements adapter.Adapter for the claude-mem memory
// provider over HTTP (read-only v1, PRD-2).
//
// Intentional divergences from the engram adapter:
//   - D1: HTTP-only, no Commander seam (claude-mem has no usable CLI enumeration).
//   - D2: Read-only — WriteNative returns ErrUnsupported.
//   - D3: Health is HTTP GET /health, not a CLI version probe.
//   - D4: Kind is ALWAYS "session_summary", never "observation" (ADR-9).
//   - D5: Field names verified live 2026-05-20 against worker :37701 — pre-merge
//     gate DISCHARGED; provisional comments removed.
//   - D6: ListNative paginates GET /api/observations instead of one GET /export dump.
//   - D7: claude-mem observations are append-only — no updated_at exists.
//     Since-filter is rebased on created_at and canonical UpdatedAt mirrors CreatedAt.
package claudemem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/agustincastanol/glia/internal/adapter"
	engramidentity "github.com/agustincastanol/glia/internal/identity"
	"github.com/agustincastanol/glia/internal/store"
)

// claudeMemRecord is the internal representation of a single claude-mem
// observation as returned by GET /api/observations. Field names were verified
// live against the claude-mem v13 worker on 2026-05-20 (pre-merge gate
// discharged).
//
// Fields NOT in the real API and dropped from the prior provisional struct:
// session_id, project_id, summary, tags, updated_at.
//
// Forensic fields (subtitle, created_at_epoch, prompt_number, etc.) are not
// surfaced through ToCanonical in v1 but are decoded so they remain available
// via rec.Raw for downstream tooling.
type claudeMemRecord struct {
	ID             int64           `json:"id"`
	MemorySessionID string         `json:"memory_session_id,omitempty"`
	Project        string          `json:"project,omitempty"`
	Type           string          `json:"type,omitempty"`
	Title          string          `json:"title,omitempty"`
	Subtitle       string          `json:"subtitle,omitempty"`
	Narrative      string          `json:"narrative,omitempty"`
	CreatedAt      string          `json:"created_at,omitempty"`
	CreatedAtEpoch int64           `json:"created_at_epoch,omitempty"`
	PromptNumber   int             `json:"prompt_number,omitempty"`
	// Raw captures the original JSON bytes for forensic inspection and access to
	// untyped fields (subtitle, facts/concepts/files_* — the latter are
	// JSON-encoded string blobs in the live API). Excluded from marshalling.
	Raw json.RawMessage `json:"-"`
}

// nativeIDString returns the canonical string form of the numeric ID used by
// adapter.NativeID and store.Origin.ProviderID. Centralized so int64↔string
// conversion is consistent across List/Read/ToCanonical.
func (r claudeMemRecord) nativeIDString() string {
	return strconv.FormatInt(r.ID, 10)
}

// observationsPage is the envelope returned by GET /api/observations. Field
// names verified live 2026-05-20.
type observationsPage struct {
	Items   []json.RawMessage `json:"items"`
	HasMore bool              `json:"hasMore"`
	Offset  int               `json:"offset"`
	Limit   int               `json:"limit"`
}

// ---------------------------------------------------------------------------
// Config holds the claudemem adapter construction parameters. The wiring
// helper (cmd/glia/cmd/wiring.go) translates *config.Config →
// claudemem.Config. Adapters never import internal/config (ADR-D3).
// ---------------------------------------------------------------------------

// Config holds all construction-time parameters for ClaudeMemAdapter.
type Config struct {
	// Enabled controls whether this provider is active. The wiring helper
	// skips disabled providers; New() always builds a functional adapter.
	Enabled bool
	// HTTPBaseURL is passed to NewHTTPTransport. Empty triggers auto-resolve
	// (supervisor.json → fallback 37701).
	HTTPBaseURL string
	// WorkerPIDPath is the path to the claude-mem worker PID file.
	WorkerPIDPath string
	// ProjectPathMapping maps project names to filesystem paths (for local lookup).
	ProjectPathMapping map[string]string
	// ExcludedSessionIDs lists session IDs whose records must never be returned
	// by ListNative (REQ-PRV-01). Filtering is O(1) via a set built at New().
	ExcludedSessionIDs []string
	// Author is pre-resolved from identity.Resolve() by the wiring helper.
	// If empty, New() resolves it via identity.Resolve("").
	Author string
	// WriteEnabled controls whether write operations are permitted. When false,
	// WriteNative returns ErrUnsupported and WriteCapability returns the
	// "read-only (write_enabled=false)" string. Defaults to true when not set
	// by the wiring layer; the wiring layer reads this from config.ClaudeMemProviderConfig.WriteEnabled.
	WriteEnabled bool
	// Project is the effective project name resolved at wiring time via
	// config.ResolveProject (PRD-6). Precedence: CLI flag > per-provider
	// providers.claude-mem.project > global Config.Project.
	// When set, ListNative uses this value as the project filter instead of
	// the project parameter.
	// NOTE: the write path payload (POST /api/memory/save) does not include a
	// project field — only the ListNative filter uses this resolved value.
	Project string
}

// ---------------------------------------------------------------------------
// providerIDMapWrapper + WrapIDMap (verbatim copy from engram adapter — ADR-7).
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
// rfc3339NanoFixed, normalizeTimestamp, tolerant multi-layout parse (ADR-6).
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

// normalizeTimestamp parses a timestamp using the accepted layout set (ADR-6)
// and re-formats as rfc3339NanoFixed (UTC, 9-digit nanoseconds) to protect the
// REQ-TS-03 byte-comparable invariant. Unknown format → wrapped error.
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
// a Title fallback (REQ-CM-12). Kept short so titles remain scannable.
const titleMaxRunes = 80

// deriveTitle returns the first titleMaxRunes runes of source, or "" if source
// is empty. Used when claudeMemRecord.Title is absent (REQ-CM-12). Falls back
// to Subtitle then Narrative in caller.
func deriveTitle(source string) string {
	if source == "" {
		return ""
	}
	if utf8.RuneCountInString(source) <= titleMaxRunes {
		return source
	}
	i := 0
	for range titleMaxRunes {
		_, size := utf8.DecodeRuneInString(source[i:])
		i += size
	}
	return source[:i]
}

// ---------------------------------------------------------------------------
// Compile-time assertion: ClaudeMemAdapter must satisfy adapter.Adapter.
// ---------------------------------------------------------------------------

var _ adapter.Adapter = (*ClaudeMemAdapter)(nil)

// ClaudeMemAdapter implements adapter.Adapter for the claude-mem memory provider.
// It is safe for concurrent use after construction.
type ClaudeMemAdapter struct {
	cfg         Config
	transport   Transport
	excludedSet map[string]struct{} // O(1) lookup for ExcludedSessionIDs (REQ-PRV-01)
}

// New constructs a ClaudeMemAdapter. transport is injected so that unit tests
// can substitute a fake without running a real daemon. Pass
// NewHTTPTransport("") for production use.
//
// If cfg.Author is empty, it is resolved via identity.Resolve("") at
// construction time. The excludedSet is built once from cfg.ExcludedSessionIDs
// for O(1) lookup in ListNative (ADR-D5, REQ-PRV-01).
//
// Construction never fails: a nil transport is accepted and causes I/O methods
// to return adapter.ErrUnavailable without panicking.
func New(cfg Config, transport Transport) *ClaudeMemAdapter {
	if cfg.Author == "" {
		cfg.Author = engramidentity.Resolve("")
	}
	excluded := make(map[string]struct{}, len(cfg.ExcludedSessionIDs))
	for _, id := range cfg.ExcludedSessionIDs {
		excluded[id] = struct{}{}
	}
	return &ClaudeMemAdapter{cfg: cfg, transport: transport, excludedSet: excluded}
}

// isExcluded reports whether sessionID appears in the exclusion set.
func (a *ClaudeMemAdapter) isExcluded(sessionID string) bool {
	if len(a.excludedSet) == 0 {
		return false
	}
	_, ok := a.excludedSet[sessionID]
	return ok
}

// Name returns "claude-mem" — the stable provider identifier stored in
// origin.provider of every canonical record (REQ-CM-02).
func (a *ClaudeMemAdapter) Name() string {
	return "claude-mem"
}

// EffectiveProject returns the project name that this adapter will use for
// filtering, as resolved at construction time (PRD-6).
func (a *ClaudeMemAdapter) EffectiveProject() string {
	return a.cfg.Project
}

// Health probes the claude-mem daemon (REQ-CM-04).
func (a *ClaudeMemAdapter) Health(ctx context.Context) error {
	if a.transport == nil {
		return fmt.Errorf("%w: transport is nil", adapter.ErrUnavailable)
	}
	if err := a.transport.Health(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: health check failed: %v", adapter.ErrUnavailable, err)
	}
	return nil
}

// ListNative returns all project-scoped native IDs created at or after since
// (REQ-CM-06, REQ-CM-07, REQ-CM-08).
//
// claude-mem observations are append-only (no updated_at field exists in the
// API). The since-filter is rebased on created_at (D7).
//
// PRD-6: if cfg.Project is non-empty (resolved at wiring time via ResolveProject),
// it overrides the project parameter so the per-provider override takes effect.
func (a *ClaudeMemAdapter) ListNative(ctx context.Context, project string, since time.Time) ([]adapter.NativeID, error) {
	// PRD-6: per-provider project wins over the engine-supplied parameter.
	if a.cfg.Project != "" {
		project = a.cfg.Project
	}
	if a.transport == nil {
		return nil, fmt.Errorf("%w: transport is nil", adapter.ErrUnavailable)
	}

	sinceStr := since.UTC().Format(rfc3339NanoFixed)

	const pageLimit = 100
	var ids []adapter.NativeID

	for offset := 0; ; offset += pageLimit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		body, err := a.transport.ListPage(ctx, pageLimit, offset)
		if err != nil {
			return nil, err
		}

		var page observationsPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("%w: decode page at offset %d: %v", adapter.ErrUnavailable, offset, err)
		}

		for _, raw := range page.Items {
			var rec claudeMemRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				log.Printf("claude-mem adapter: WARN skipping unparseable item at offset %d: %v; raw=%s", offset, err, raw)
				continue
			}
			rec.Raw = raw

			// REQ-CM-07: strict project equality filter on the `project` field
			// (project name, NOT path — verified 2026-05-20).
			if rec.Project != project {
				continue
			}

			// REQ-PRV-01: excluded sessions return zero records, no error, no log.
			if a.isExcluded(rec.MemorySessionID) {
				continue
			}

			// REQ-CM-08 (rebased on created_at — D7). Unparseable → WARN+skip.
			normalizedCreatedAt, err := normalizeTimestamp(rec.CreatedAt)
			if err != nil {
				log.Printf("claude-mem adapter: WARN skipping obs id=%d unparseable created_at %q: %v", rec.ID, rec.CreatedAt, err)
				continue
			}

			if normalizedCreatedAt < sinceStr {
				continue
			}

			ids = append(ids, adapter.NativeID(rec.nativeIDString()))
		}

		// Guard: a server that returns HasMore:true with an empty Items slice
		// would cause an infinite loop. Treat empty pages as terminal regardless
		// of the HasMore flag.
		if len(page.Items) == 0 || !page.HasMore {
			break
		}
	}

	return ids, nil
}

// ReadNative retrieves the full native record for id (REQ-CM-09).
// Primary: GET /api/observations/:id. On ErrUnavailable (endpoint missing/5xx):
// degrades to paginate+scan. On ErrNotFound (clean 404): returns ErrNotFound.
func (a *ClaudeMemAdapter) ReadNative(ctx context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	if a.transport == nil {
		return nil, fmt.Errorf("%w: transport is nil", adapter.ErrUnavailable)
	}

	body, found, err := a.transport.GetByID(ctx, string(id))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if errors.Is(err, adapter.ErrNotFound) {
			return nil, adapter.ErrNotFound
		}
		// ErrUnavailable: degrade to paginate+scan below.
	}

	if found && err == nil {
		var rec claudeMemRecord
		if unmarshalErr := json.Unmarshal(body, &rec); unmarshalErr != nil {
			return nil, fmt.Errorf("%w: decode record %q: %v", adapter.ErrUnavailable, id, unmarshalErr)
		}
		rec.Raw = body
		return rec, nil
	}

	// Degrade: scan paginated list for matching id (string compare on numeric form).
	const pageLimit = 100
	target := string(id)
	for offset := 0; ; offset += pageLimit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		pageBody, pageErr := a.transport.ListPage(ctx, pageLimit, offset)
		if pageErr != nil {
			if errors.Is(pageErr, context.Canceled) || errors.Is(pageErr, context.DeadlineExceeded) {
				return nil, pageErr
			}
			return nil, fmt.Errorf("%w: scan for %q at offset %d: %v", adapter.ErrUnavailable, id, offset, pageErr)
		}

		var page observationsPage
		if unmarshalErr := json.Unmarshal(pageBody, &page); unmarshalErr != nil {
			return nil, fmt.Errorf("%w: decode scan page at offset %d: %v", adapter.ErrUnavailable, offset, unmarshalErr)
		}

		for _, raw := range page.Items {
			var rec claudeMemRecord
			if unmarshalErr := json.Unmarshal(raw, &rec); unmarshalErr != nil {
				continue
			}
			if rec.nativeIDString() == target {
				rec.Raw = raw
				return rec, nil
			}
		}

		if len(page.Items) == 0 || !page.HasMore {
			break
		}
	}

	return nil, adapter.ErrNotFound
}

// ToCanonical converts a native claudeMemRecord to a store.CanonicalRecord
// using idmap for ID resolution. Pure: no I/O (REQ-CM-10).
//
// Field mapping (verified 2026-05-20):
//   - id (int64)         → strconv → Origin.ProviderID
//   - memory_session_id  → Origin.SessionID
//   - title              → Title; falls back to Subtitle then derived-from-Narrative
//   - narrative          → Content; ContentFormat "markdown"
//   - type               → Type (claude-mem vocab: discovery/feature/bugfix/...)
//   - created_at         → CreatedAt (normalized via rfc3339NanoFixed)
//   - UpdatedAt          mirrors CreatedAt (claude-mem records are append-only, D7)
//   - Kind               always "session_summary" (ADR-9, D4)
//   - Origin.Provider    always "claude-mem" (REQ-CM-02)
//   - Origin.Author      authorAttribution()
//   - CanonicalID        IDMap lookup; "" when new (store mints ULID on Append)
//   - Revision           1 if new; -1 sentinel if known (ADR-12)
//   - Tags               []string{} (claude-mem has no native tags)
//   - SchemaVersion      NOT set — store owns it (REQ-CM-11)
func (a *ClaudeMemAdapter) ToCanonical(native adapter.NativeRecord, idmap adapter.IDMap) (store.CanonicalRecord, error) {
	rec, ok := native.(claudeMemRecord)
	if !ok {
		return store.CanonicalRecord{}, fmt.Errorf("ToCanonical: expected claudeMemRecord, got %T", native)
	}

	// REQ-CM-13: forensic warn when both title and narrative are empty.
	if rec.Title == "" && rec.Narrative == "" {
		log.Printf("claude-mem adapter: WARN observation id=%d has empty title and narrative; raw=%s", rec.ID, rec.Raw)
	}

	// Title fallback: Title → Subtitle → derive from Narrative.
	title := rec.Title
	if title == "" {
		if rec.Subtitle != "" {
			title = deriveTitle(rec.Subtitle)
		} else {
			title = deriveTitle(rec.Narrative)
		}
	}

	// REQ-CM-16: normalize timestamps — reject record on unknown format (ADR-6).
	createdAt, err := normalizeTimestamp(rec.CreatedAt)
	if err != nil {
		return store.CanonicalRecord{}, fmt.Errorf("ToCanonical: %w", err)
	}
	// D7: claude-mem has no updated_at — canonical UpdatedAt mirrors CreatedAt.
	updatedAt := createdAt

	nativeID := adapter.NativeID(rec.nativeIDString())
	canonicalID, hasMapping := idmap.CanonicalFromNative(nativeID)

	// ADR-12: shared cross-adapter revision sentinel.
	//   new (no IDMap mapping)  → 1
	//   known (prior mapping)   → -1, signalling PRD-3 orchestrator to replace
	//                             with priorRevision+1 on re-ingest.
	revision := 1
	if hasMapping {
		revision = -1
	}

	return store.CanonicalRecord{
		CanonicalID:   string(canonicalID),
		Kind:          "session_summary",
		Revision:      revision,
		Title:         title,
		Content:       rec.Narrative,
		ContentFormat: "markdown",
		Type:          rec.Type,
		TopicKey:      "",
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		Tags:          []string{},
		Origin: store.Origin{
			Provider:   "claude-mem",
			ProviderID: rec.nativeIDString(),
			Author:     a.cfg.Author,
			SessionID:  rec.MemorySessionID,
		},
	}, nil
}

// FromCanonical converts a store.CanonicalRecord to a native claudeMemRecord.
// Pure: no I/O (REQ-CM-15). Implemented (not stubbed) so v1.1 bidirectional
// reuse can use it verbatim. NEVER passed to WriteNative in v1 (ADR-10).
//
// NOTE: CreatedAt is intentionally omitted from the output — claude-mem is
// read-only in v1, no write surface exists, timestamps are never persisted back.
// Any future write path (v1.1+) MUST re-read CreatedAt from the provider, not
// round-trip from FromCanonical output.
func (a *ClaudeMemAdapter) FromCanonical(canonical store.CanonicalRecord) (adapter.NativeRecord, error) {
	// Parse ProviderID back to int64 best-effort; 0 on malformed input. The
	// resulting record is never persisted in v1 (ADR-10), so the loss-of-info
	// path is acceptable.
	var id int64
	if canonical.Origin.ProviderID != "" {
		parsed, perr := strconv.ParseInt(canonical.Origin.ProviderID, 10, 64)
		if perr == nil {
			id = parsed
		}
	}
	return claudeMemRecord{
		ID:              id,
		MemorySessionID: canonical.Origin.SessionID,
		Project:         "", // not represented in canonical record (filter, not payload)
		Type:            canonical.Type,
		Title:           canonical.Title,
		Narrative:       canonical.Content,
		// CreatedAt intentionally omitted — see doc comment above.
	}, nil
}

// WriteNative writes the native record to claude-mem via POST /api/memory/save
// (REQ-CMW-04). Returns the assigned NativeID on success.
//
// Gate order:
//  1. cfg.WriteEnabled == false → ErrUnsupported (skip all I/O)
//  2. transport.WriteSupported returns false → ErrUnsupported
//  3. record is not a claudeMemRecord → ErrUnsupported
//  4. POST /api/memory/save → NativeID or error
//
// The request text is formatted as "Title\n\nNarrative" when both are
// non-empty, or whichever is non-empty alone. This matches the format
// expected by the claude-mem worker for session summaries.
func (a *ClaudeMemAdapter) WriteNative(ctx context.Context, record adapter.NativeRecord) (adapter.NativeID, error) {
	// Gate 1: config gate.
	if !a.cfg.WriteEnabled {
		return "", fmt.Errorf("%w: write_enabled=false", adapter.ErrUnsupported)
	}

	// Gate 2: endpoint probe.
	if a.transport == nil || !a.transport.WriteSupported(ctx) {
		return "", fmt.Errorf("%w: worker missing POST /api/memory/save", adapter.ErrUnsupported)
	}

	// Gate 3: type assertion.
	rec, ok := record.(claudeMemRecord)
	if !ok {
		return "", fmt.Errorf("%w: WriteNative expected claudeMemRecord, got %T", adapter.ErrUnsupported, record)
	}

	// Build the text payload. Format: "Title\n\nNarrative" when both present;
	// fallback to whichever is non-empty.
	var text string
	switch {
	case rec.Title != "" && rec.Narrative != "":
		text = rec.Title + "\n\n" + rec.Narrative
	case rec.Title != "":
		text = rec.Title
	default:
		text = rec.Narrative
	}

	resp, err := a.transport.SaveMemory(ctx, SaveMemoryRequest{Text: text})
	if err != nil {
		return "", err
	}

	return adapter.NativeID(strconv.FormatInt(resp.ID, 10)), nil
}

// WriteCapability returns a human-readable string describing this adapter's
// write support level (REQ-CMW-03).
//
// Decision order:
//  1. cfg.WriteEnabled == false → "read-only (write_enabled=false)"
//  2. transport.WriteSupported returns false → "read-only (worker missing POST /api/memory/save)"
//  3. otherwise → "read+write"
//
// If transport is nil, the probe cannot be run → "read-only (worker missing POST /api/memory/save)".
func (a *ClaudeMemAdapter) WriteCapability() string {
	if !a.cfg.WriteEnabled {
		return "read-only (write_enabled=false)"
	}
	if a.transport == nil || !a.transport.WriteSupported(context.Background()) {
		return "read-only (worker missing POST /api/memory/save)"
	}
	return "read+write"
}

// SupportedKinds returns nil, meaning all canonical kinds are attempted.
// In practice only "session_summary" records round-trip through this adapter;
// other kinds are rejected by FromCanonical with ErrUnsupported.
func (a *ClaudeMemAdapter) SupportedKinds() []string {
	return nil
}
