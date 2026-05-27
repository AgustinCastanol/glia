package engram_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/adapter/engram"
	"github.com/agustincastanol/glia/internal/store"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// panicCommander panics if called — used to verify pure functions never touch CLI.
type panicCommander struct{ t *testing.T }

func (p *panicCommander) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	p.t.Fatalf("pure function must not call Commander; args=%v", args)
	return nil, nil, nil
}

// panicTransport panics if called — used to verify pure functions never touch HTTP.
type panicTransport struct{ t *testing.T }

func (p *panicTransport) GetByID(_ context.Context, id adapter.NativeID) (engram.EngramRecord, error) {
	p.t.Fatalf("pure function must not call Transport; id=%s", id)
	return engram.EngramRecord{}, nil
}

func (p *panicTransport) Export(_ context.Context) ([]byte, error) {
	p.t.Fatal("pure function must not call Transport.Export")
	return nil, nil
}

// fakeCommander returns canned responses keyed by the first arg.
type fakeCommander struct {
	runs map[string]fakeCommandRun
}

type fakeCommandRun struct {
	stdout []byte
	stderr []byte
	err    error
}

func (f *fakeCommander) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	if len(args) == 0 {
		return nil, nil, errors.New("fake: no args")
	}
	key := args[0]
	if run, ok := f.runs[key]; ok {
		return run.stdout, run.stderr, run.err
	}
	// Default: empty JSON array for "search", error otherwise.
	if key == "search" {
		return []byte("[]"), nil, nil
	}
	return nil, nil, errors.New("fake: unexpected command: " + key)
}

// blockingCommander blocks until ctx is cancelled.
type blockingCommander struct{}

func (b *blockingCommander) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

// fakeTransport returns canned responses by NativeID, and optionally a canned
// export body for the Export() method.
type fakeTransport struct {
	records    map[adapter.NativeID]engram.EngramRecord
	err        map[adapter.NativeID]error
	exportBody []byte
	exportErr  error
}

func (f *fakeTransport) GetByID(_ context.Context, id adapter.NativeID) (engram.EngramRecord, error) {
	if err, ok := f.err[id]; ok {
		return engram.EngramRecord{}, err
	}
	if rec, ok := f.records[id]; ok {
		return rec, nil
	}
	return engram.EngramRecord{}, adapter.ErrNotFound
}

func (f *fakeTransport) Export(_ context.Context) ([]byte, error) {
	if f.exportErr != nil {
		return nil, f.exportErr
	}
	if f.exportBody != nil {
		return f.exportBody, nil
	}
	// Default: empty export with no observations.
	return []byte(`{"observations":[]}`), nil
}

// fakeIDMap is a simple in-memory IDMap for tests.
type fakeIDMap struct {
	fwd map[adapter.NativeID]adapter.CanonicalID
}

func (f *fakeIDMap) CanonicalFromNative(id adapter.NativeID) (adapter.CanonicalID, bool) {
	v, ok := f.fwd[id]
	return v, ok
}

func (f *fakeIDMap) NativeFromCanonical(id adapter.CanonicalID) (adapter.NativeID, bool) {
	for k, v := range f.fwd {
		if v == id {
			return k, true
		}
	}
	return "", false
}

// emptyIDMap returns false for all lookups.
type emptyIDMap struct{}

func (e *emptyIDMap) CanonicalFromNative(_ adapter.NativeID) (adapter.CanonicalID, bool) {
	return "", false
}
func (e *emptyIDMap) NativeFromCanonical(_ adapter.CanonicalID) (adapter.NativeID, bool) {
	return "", false
}

// ---------------------------------------------------------------------------
// Helper: build a minimal project-scoped EngramRecord for tests.
// ---------------------------------------------------------------------------
func minimalRecord(id, title string) engram.EngramRecord {
	return engram.EngramRecord{
		ID:            id,
		Title:         title,
		Type:          "manual",
		Content:       "test content",
		ContentFormat: "markdown",
		Scope:         "project",
		Project:       "testproject",
		CreatedAt:     "2026-05-16T01:00:00.000000000Z",
		UpdatedAt:     "2026-05-16T04:00:00.000000000Z",
	}
}

// ---------------------------------------------------------------------------
// S-01: ToCanonical purity — no provider calls
// ---------------------------------------------------------------------------
func TestToCanonical_Purity_NoProviderCalls(t *testing.T) {
	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, &panicTransport{t: t})
	rec := minimalRecord("obs-001", "Pure test")
	idmap := &emptyIDMap{}

	canonical, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("ToCanonical returned error: %v", err)
	}
	if canonical.Title != "Pure test" {
		t.Errorf("title mismatch: got %q, want %q", canonical.Title, "Pure test")
	}
	if canonical.Origin.Provider != "engram" {
		t.Errorf("provider mismatch: got %q, want %q", canonical.Origin.Provider, "engram")
	}
}

// ---------------------------------------------------------------------------
// S-02: FromCanonical purity — no provider calls
// ---------------------------------------------------------------------------
func TestFromCanonical_Purity_NoProviderCalls(t *testing.T) {
	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, nil)
	canonical := store.CanonicalRecord{
		CanonicalID:   "01JTESTULID01",
		Kind:          "observation",
		Title:         "FromCanon test",
		Type:          "manual",
		Content:       "content here",
		ContentFormat: "markdown",
		Revision:      1,
		Origin: store.Origin{
			Provider:   "engram",
			ProviderID: "obs-002",
		},
		CreatedAt: "2026-05-16T01:00:00.000000000Z",
		UpdatedAt: "2026-05-16T04:00:00.000000000Z",
	}

	native, err := a.FromCanonical(canonical)
	if err != nil {
		t.Fatalf("FromCanonical returned error: %v", err)
	}
	rec, ok := native.(engram.EngramRecord)
	if !ok {
		t.Fatalf("expected EngramRecord, got %T", native)
	}
	if rec.Title != "FromCanon test" {
		t.Errorf("title mismatch: got %q, want %q", rec.Title, "FromCanon test")
	}
}

// buildExportBody builds a fake GET /export JSON body with the given observations.
// Timestamps use the SQLite format "2006-01-02 15:04:05" as returned by the real daemon.
func buildExportBody(obs []map[string]interface{}) []byte {
	payload := map[string]interface{}{"observations": obs}
	b, _ := json.Marshal(payload)
	return b
}

// ---------------------------------------------------------------------------
// S-03: Personal-scope filtering in ListNative (DEFECT-LN-01 fix — Export path)
// ---------------------------------------------------------------------------
func TestListNative_PersonalScopeFiltered(t *testing.T) {
	// Export timestamps use SQLite datetime format: "2006-01-02 15:04:05" UTC.
	exportObs := []map[string]interface{}{
		{"sync_id": "obs-proj-1", "project": "testproject", "scope": "project", "updated_at": "2026-05-16 04:00:00"},
		{"sync_id": "obs-pers-1", "project": "testproject", "scope": "personal", "updated_at": "2026-05-16 04:00:00"},
		{"sync_id": "obs-proj-2", "project": "testproject", "scope": "project", "updated_at": "2026-05-16 04:00:00"},
	}

	tr := &fakeTransport{exportBody: buildExportBody(exportObs)}
	a := engram.New(engram.Config{Commander: &fakeCommander{}}, tr)

	since := time.Time{}
	ids, err := a.ListNative(context.Background(), "testproject", since)
	if err != nil {
		t.Fatalf("ListNative error: %v", err)
	}

	for _, id := range ids {
		if string(id) == "obs-pers-1" {
			t.Errorf("personal-scope ID leaked into result: %q", id)
		}
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 project-scope IDs, got %d", len(ids))
	}
}

// W-02 regression: a record whose updated_at is exactly the `since` boundary
// (a whole-second timestamp) MUST be included. The Export path normalizes the
// SQLite "2006-01-02 15:04:05" timestamps to rfc3339NanoFixed before comparing,
// and sinceStr also uses rfc3339NanoFixed — so whole-second boundaries align.
//
// This test exercises the Export-based ListNative (DEFECT-LN-01 fix path).
func TestListNative_SinceBoundary_WholeSecond_Included(t *testing.T) {
	// Export timestamps: "2006-01-02 15:04:05" UTC (SQLite format, no nanoseconds).
	exportObs := []map[string]interface{}{
		{"sync_id": "obs-at-boundary", "project": "testproject", "scope": "project", "updated_at": "2026-05-16 10:00:00"},
		{"sync_id": "obs-before", "project": "testproject", "scope": "project", "updated_at": "2026-05-16 09:59:59"},
	}

	tr := &fakeTransport{exportBody: buildExportBody(exportObs)}
	a := engram.New(engram.Config{Commander: &fakeCommander{}}, tr)

	// Whole-second `since` exactly equal to the boundary record's updated_at.
	since := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	ids, err := a.ListNative(context.Background(), "testproject", since)
	if err != nil {
		t.Fatalf("ListNative error: %v", err)
	}

	var got []string
	for _, id := range ids {
		got = append(got, string(id))
	}
	if len(got) != 1 || got[0] != "obs-at-boundary" {
		t.Errorf("expected exactly [obs-at-boundary] (record AT the since boundary kept, earlier record dropped), got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ListNative (Export path): cross-project filtering — only requested project IDs
// ---------------------------------------------------------------------------
func TestListNative_CrossProjectFiltered(t *testing.T) {
	// Export contains observations from multiple projects; only "myproject" requested.
	exportObs := []map[string]interface{}{
		{"sync_id": "obs-mine-1", "project": "myproject", "scope": "project", "updated_at": "2026-05-16 04:00:00"},
		{"sync_id": "obs-other-1", "project": "otherproject", "scope": "project", "updated_at": "2026-05-16 04:00:00"},
		{"sync_id": "obs-mine-2", "project": "myproject", "scope": "project", "updated_at": "2026-05-16 05:00:00"},
		{"sync_id": "obs-other-2", "project": "thirdproject", "scope": "project", "updated_at": "2026-05-16 06:00:00"},
	}

	tr := &fakeTransport{exportBody: buildExportBody(exportObs)}
	a := engram.New(engram.Config{Commander: &fakeCommander{}}, tr)

	ids, err := a.ListNative(context.Background(), "myproject", time.Time{})
	if err != nil {
		t.Fatalf("ListNative error: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 IDs for myproject, got %d: %v", len(ids), ids)
	}
	for _, id := range ids {
		if string(id) != "obs-mine-1" && string(id) != "obs-mine-2" {
			t.Errorf("unexpected ID in result: %q", id)
		}
	}
}

// ---------------------------------------------------------------------------
// ListNative (Export path): ErrUnavailable when daemon is unreachable
// ---------------------------------------------------------------------------
func TestListNative_DaemonDown_ErrUnavailable(t *testing.T) {
	tr := &fakeTransport{
		exportErr: fmt.Errorf("%w: connection refused", adapter.ErrUnavailable),
	}
	a := engram.New(engram.Config{Commander: &fakeCommander{}}, tr)

	_, err := a.ListNative(context.Background(), "testproject", time.Time{})
	if !errors.Is(err, adapter.ErrUnavailable) {
		t.Errorf("expected ErrUnavailable when daemon down, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// S-04: WriteNative idempotence — existing provider_id → update path
// ---------------------------------------------------------------------------
func TestWriteNative_Idempotence_ExistingRecord(t *testing.T) {
	existingID := "obs-abc123"
	existingResult := []map[string]interface{}{
		{"id": existingID, "scope": "project", "title": "existing", "updated_at": "2026-05-16T04:00:00.000000000Z"},
	}
	data, _ := json.Marshal(existingResult)

	cmd := &fakeCommander{
		runs: map[string]fakeCommandRun{
			"search": {stdout: data},
		},
	}
	a := engram.New(engram.Config{Commander: cmd}, nil)

	rec := engram.EngramRecord{
		ID:      existingID,
		Title:   "existing",
		Type:    "manual",
		Content: "content",
		Scope:   "project",
		Project: "testproject",
	}

	id, err := a.WriteNative(context.Background(), rec)
	// Should return ErrUnsupported (no update path in v1), NOT duplicate.
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for existing record, got err=%v id=%q", err, id)
	}
	// The returned ID must be the existing one (not empty).
	if string(id) != existingID {
		t.Errorf("expected NativeID=%q, got %q", existingID, id)
	}
}

// ---------------------------------------------------------------------------
// S-05: ReadNative HTTP fallback — daemon down → ErrUnavailable
// ---------------------------------------------------------------------------
func TestReadNative_HTTPFallback_DaemonDown(t *testing.T) {
	// CLI returns empty (no match for this ID).
	cmd := &fakeCommander{
		runs: map[string]fakeCommandRun{
			"search": {stdout: []byte("[]")},
		},
	}
	tr := &fakeTransport{
		err: map[adapter.NativeID]error{
			adapter.NativeID("obs-missing"): fmt.Errorf("%w: connection refused", adapter.ErrUnavailable),
		},
	}
	a := engram.New(engram.Config{Commander: cmd}, tr)

	_, err := a.ReadNative(context.Background(), "obs-missing")
	if !errors.Is(err, adapter.ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// S-06: ReadNative HTTP fallback — daemon up, record found
// ---------------------------------------------------------------------------
func TestReadNative_HTTPFallback_DaemonUp_RecordFound(t *testing.T) {
	expected := engram.EngramRecord{
		ID:        "obs-http-001",
		Title:     "HTTP record",
		Type:      "manual",
		Content:   "from HTTP",
		Scope:     "project",
		UpdatedAt: "2026-05-16T04:00:00.000000000Z",
	}

	// Use httptest.NewServer to exercise the real HTTP path mapping.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/observations/obs-http-001" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(expected)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	// Use a fake transport that hits the test server.
	cmd := &fakeCommander{
		runs: map[string]fakeCommandRun{
			"search": {stdout: []byte("[]")},
		},
	}
	tr := &httpTestTransport{baseURL: ts.URL}
	a := engram.New(engram.Config{Commander: cmd}, tr)

	native, err := a.ReadNative(context.Background(), "obs-http-001")
	if err != nil {
		t.Fatalf("ReadNative error: %v", err)
	}
	rec, ok := native.(engram.EngramRecord)
	if !ok {
		t.Fatalf("expected EngramRecord, got %T", native)
	}
	if rec.Title != expected.Title {
		t.Errorf("title mismatch: got %q, want %q", rec.Title, expected.Title)
	}
}

// httpTestTransport is a Transport backed by an httptest.Server for S-06.
type httpTestTransport struct {
	baseURL string
}

func (h *httpTestTransport) GetByID(ctx context.Context, id adapter.NativeID) (engram.EngramRecord, error) {
	url := h.baseURL + "/observations/" + string(id)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return engram.EngramRecord{}, fmt.Errorf("%w: %v", adapter.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return engram.EngramRecord{}, adapter.ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return engram.EngramRecord{}, fmt.Errorf("%w: status %d", adapter.ErrUnavailable, resp.StatusCode)
	}
	var rec engram.EngramRecord
	json.NewDecoder(resp.Body).Decode(&rec)
	return rec, nil
}

func (h *httpTestTransport) Export(_ context.Context) ([]byte, error) {
	// Not used in S-06 test; return empty export.
	return []byte(`{"observations":[]}`), nil
}

// ---------------------------------------------------------------------------
// S-07: ReadNative — record not found in either path → ErrNotFound
// ---------------------------------------------------------------------------
func TestReadNative_NotFoundInEitherPath(t *testing.T) {
	cmd := &fakeCommander{
		runs: map[string]fakeCommandRun{
			"search": {stdout: []byte("[]")},
		},
	}
	tr := &fakeTransport{
		err: map[adapter.NativeID]error{
			adapter.NativeID("obs-404"): adapter.ErrNotFound,
		},
	}
	a := engram.New(engram.Config{Commander: cmd}, tr)

	_, err := a.ReadNative(context.Background(), "obs-404")
	if !errors.Is(err, adapter.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// S-08: ReadNative — context cancellation propagates
// ---------------------------------------------------------------------------
func TestReadNative_ContextCancellation(t *testing.T) {
	cmd := &blockingCommander{}
	a := engram.New(engram.Config{Commander: cmd}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := a.ReadNative(ctx, "obs-block")
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("ReadNative did not return within deadline; elapsed=%v", elapsed)
	}
	if err == nil {
		t.Error("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, adapter.ErrUnavailable) {
		t.Errorf("expected DeadlineExceeded or ErrUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// S-12: Timestamp RFC3339Nano normalization — byte-identical round-trip
// ---------------------------------------------------------------------------
func TestToCanonical_TimestampNormalization(t *testing.T) {
	rec := minimalRecord("obs-ts", "TS test")
	rec.CreatedAt = "2026-05-16T01:33:00.000000000Z"
	rec.UpdatedAt = "2026-05-16T04:27:57.123456789Z"

	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, &panicTransport{t: t})
	canonical, err := a.ToCanonical(rec, &emptyIDMap{})
	if err != nil {
		t.Fatalf("ToCanonical error: %v", err)
	}

	if canonical.CreatedAt != "2026-05-16T01:33:00.000000000Z" {
		t.Errorf("CreatedAt mismatch: got %q, want %q", canonical.CreatedAt, "2026-05-16T01:33:00.000000000Z")
	}
	if canonical.UpdatedAt != "2026-05-16T04:27:57.123456789Z" {
		t.Errorf("UpdatedAt mismatch: got %q, want %q", canonical.UpdatedAt, "2026-05-16T04:27:57.123456789Z")
	}
	// Verify lexicographic ordering holds.
	if canonical.CreatedAt >= canonical.UpdatedAt {
		t.Errorf("timestamps not in chronological order: created=%q, updated=%q", canonical.CreatedAt, canonical.UpdatedAt)
	}
}

// ---------------------------------------------------------------------------
// S-13: Author attribution — env var override
// ---------------------------------------------------------------------------
func TestAuthorAttribution_EnvOverride(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "ci-bot@build-server")

	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, &panicTransport{t: t})
	rec := minimalRecord("obs-author", "Author test")
	canonical, err := a.ToCanonical(rec, &emptyIDMap{})
	if err != nil {
		t.Fatalf("ToCanonical error: %v", err)
	}
	if canonical.Origin.Author != "ci-bot@build-server" {
		t.Errorf("author mismatch: got %q, want %q", canonical.Origin.Author, "ci-bot@build-server")
	}
}

// ---------------------------------------------------------------------------
// S-14: Author attribution — default fallback USER@hostname
// ---------------------------------------------------------------------------
func TestAuthorAttribution_DefaultFallback(t *testing.T) {
	// Clear WRAPPER_MEMS_AUTHOR so we hit the fallback.
	os.Unsetenv("WRAPPER_MEMS_AUTHOR")

	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, &panicTransport{t: t})
	rec := minimalRecord("obs-author2", "Author fallback test")
	canonical, err := a.ToCanonical(rec, &emptyIDMap{})
	if err != nil {
		t.Fatalf("ToCanonical error: %v", err)
	}
	// Author should be non-empty (USER@hostname or whatever is available).
	if canonical.Origin.Author == "" {
		t.Error("author attribution must not be empty")
	}
}

// ---------------------------------------------------------------------------
// S-15: Relation records → ErrUnsupported
// ---------------------------------------------------------------------------
func TestToCanonical_RelationRecord_ErrUnsupported(t *testing.T) {
	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, &panicTransport{t: t})
	rec := minimalRecord("obs-rel", "Relation")
	rec.Type = "relation"

	_, err := a.ToCanonical(rec, &emptyIDMap{})
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for relation record, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// S-16: Personal-scope record in ToCanonical → error
// ---------------------------------------------------------------------------
func TestToCanonical_PersonalScope_Error(t *testing.T) {
	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, &panicTransport{t: t})
	rec := minimalRecord("obs-personal", "Personal record")
	rec.Scope = "personal"

	_, err := a.ToCanonical(rec, &emptyIDMap{})
	if err == nil {
		t.Error("expected error for personal-scope record, got nil")
	}
}

// ---------------------------------------------------------------------------
// S-19: Revision increment on re-import (existing mapping → signals prior mapping)
// ---------------------------------------------------------------------------
func TestToCanonical_RevisionIncrement_ExistingMapping(t *testing.T) {
	// IDMap with an existing mapping for obs-xyz → canonical-001.
	idmap := &fakeIDMap{
		fwd: map[adapter.NativeID]adapter.CanonicalID{
			"obs-xyz": "canonical-001",
		},
	}
	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, &panicTransport{t: t})
	rec := minimalRecord("obs-xyz", "Existing record")

	canonical, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("ToCanonical error: %v", err)
	}
	// When prior mapping exists, revision is -1 (sentinel for "orchestrator must increment").
	// Per design: adapter cannot know current revision; it signals prior-mapping existence.
	if canonical.Revision != -1 {
		t.Errorf("expected revision=-1 (prior mapping sentinel), got %d", canonical.Revision)
	}
	// CanonicalID should be populated from idmap.
	if canonical.CanonicalID != "canonical-001" {
		t.Errorf("canonical ID mismatch: got %q, want %q", canonical.CanonicalID, "canonical-001")
	}
}

// ---------------------------------------------------------------------------
// S-20: Backward updated_at — revision still incremented (signaled), no error
// ---------------------------------------------------------------------------
func TestToCanonical_BackwardUpdatedAt_WarningNotError(t *testing.T) {
	idmap := &fakeIDMap{
		fwd: map[adapter.NativeID]adapter.CanonicalID{
			"obs-back": "canonical-002",
		},
	}
	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, &panicTransport{t: t})

	// Incoming record has an older UpdatedAt than would be expected.
	rec := minimalRecord("obs-back", "Backward ts")
	rec.UpdatedAt = "2026-05-16T09:00:00.000000000Z" // earlier than "existing" 10:00

	// ToCanonical should NOT return an error, just log a warning.
	canonical, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Errorf("expected no error for backward updated_at, got %v", err)
	}
	// Prior mapping → revision sentinel must be set.
	if canonical.Revision != -1 {
		t.Errorf("expected revision=-1 (prior mapping sentinel), got %d", canonical.Revision)
	}
}

// ---------------------------------------------------------------------------
// W-01: providerIDMapWrapper satisfies adapter.IDMap
// ---------------------------------------------------------------------------
func TestProviderIDMapWrapper_SatisfiesAdapterIDMap(t *testing.T) {
	// Build a fake plain-string-method source (mirrors *store.providerIDMap shape).
	inner := &plainStringIDMap{
		fwd: map[string]string{
			"native-001": "canonical-001",
			"native-002": "canonical-002",
		},
	}

	// WrapIDMap should return a value that satisfies adapter.IDMap.
	var idmap adapter.IDMap = engram.WrapIDMap(inner)
	if idmap == nil {
		t.Fatal("WrapIDMap returned nil")
	}

	// Forward lookup.
	cid, ok := idmap.CanonicalFromNative("native-001")
	if !ok || cid != "canonical-001" {
		t.Errorf("CanonicalFromNative: got (%q, %v), want (\"canonical-001\", true)", cid, ok)
	}

	// Reverse lookup.
	nid, ok := idmap.NativeFromCanonical("canonical-002")
	if !ok || nid != "native-002" {
		t.Errorf("NativeFromCanonical: got (%q, %v), want (\"native-002\", true)", nid, ok)
	}

	// Miss.
	_, ok = idmap.CanonicalFromNative("native-nope")
	if ok {
		t.Error("CanonicalFromNative should return false for unknown key")
	}
}

// plainStringIDMap mimics *store.providerIDMap's plain-string method signatures.
type plainStringIDMap struct {
	fwd map[string]string
}

func (p *plainStringIDMap) CanonicalFromNative(nativeID string) (string, bool) {
	v, ok := p.fwd[nativeID]
	return v, ok
}

func (p *plainStringIDMap) NativeFromCanonical(canonicalID string) (string, bool) {
	for k, v := range p.fwd {
		if v == canonicalID {
			return k, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Health: verify wraps ErrUnavailable on non-zero exit
// ---------------------------------------------------------------------------
func TestHealth_EngramUnavailable(t *testing.T) {
	cmd := &fakeCommander{
		runs: map[string]fakeCommandRun{
			"version": {err: errors.New("exit status 1")},
		},
	}
	a := engram.New(engram.Config{Commander: cmd}, nil)
	err := a.Health(context.Background())
	if !errors.Is(err, adapter.ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}

func TestHealth_EngramAvailable(t *testing.T) {
	cmd := &fakeCommander{
		runs: map[string]fakeCommandRun{
			"version": {stdout: []byte("engram v1.15.3\n")},
		},
	}
	a := engram.New(engram.Config{Commander: cmd}, nil)
	err := a.Health(context.Background())
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// FromCanonical: relation record → ErrUnsupported
// ---------------------------------------------------------------------------
func TestFromCanonical_RelationRecord_ErrUnsupported(t *testing.T) {
	a := engram.New(engram.Config{Commander: &panicCommander{t: t}}, nil)
	canonical := store.CanonicalRecord{
		CanonicalID:   "rel-001",
		Kind:          "relation",
		ContentFormat: "markdown",
	}
	_, err := a.FromCanonical(canonical)
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for relation, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Name()
// ---------------------------------------------------------------------------
func TestName(t *testing.T) {
	a := engram.New(engram.Config{}, nil)
	if a.Name() != "engram" {
		t.Errorf("Name()=%q, want %q", a.Name(), "engram")
	}
}
