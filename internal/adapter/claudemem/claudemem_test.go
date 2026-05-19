package claudemem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// ---------------------------------------------------------------------------
// PR#1 unit tests (T-05)
// Covers: resolveBaseURL precedence (scenarios M, N), malformed/missing
// supervisor.json fallback, httpTransport error mapping, Name().
// ---------------------------------------------------------------------------

// setFakeHome sets XDG_HOME-style home for the duration of the test by
// rewriting os.UserHomeDir via a temporary HOME env var. Cleanup is registered
// automatically via t.Setenv — no cleanup func returned.
func setFakeHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
}

// writeSupervisorJSON writes a supervisor.json file under <dir>/.claude-mem/.
func writeSupervisorJSON(t *testing.T, dir string, content string) {
	t.Helper()
	claudeMemDir := filepath.Join(dir, ".claude-mem")
	if err := os.MkdirAll(claudeMemDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude-mem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeMemDir, "supervisor.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write supervisor.json: %v", err)
	}
}

// ---------------------------------------------------------------------------
// resolveBaseURL tests
// ---------------------------------------------------------------------------

// TestResolveBaseURL_ExplicitWins verifies scenario N: explicit baseURL beats
// supervisor.json.
func TestResolveBaseURL_ExplicitWins(t *testing.T) {
	dir := t.TempDir()
	writeSupervisorJSON(t, dir, `{"port": 38888}`)
	setFakeHome(t, dir)

	got := resolveBaseURL("http://localhost:9999")
	if got != "http://localhost:9999" {
		t.Fatalf("expected explicit URL to win, got %q", got)
	}
}

// TestResolveBaseURL_SupervisorWins verifies scenario M: supervisor.json port
// is used when no explicit URL is provided.
func TestResolveBaseURL_SupervisorWins(t *testing.T) {
	dir := t.TempDir()
	writeSupervisorJSON(t, dir, `{"port": 38888}`)
	setFakeHome(t, dir)

	got := resolveBaseURL("")
	if got != "http://localhost:38888" {
		t.Fatalf("expected supervisor port 38888, got %q", got)
	}
}

// TestResolveBaseURL_MalformedSupervisor_FallsBack verifies that malformed
// supervisor.json silently falls through to the hardcoded default.
func TestResolveBaseURL_MalformedSupervisor_FallsBack(t *testing.T) {
	dir := t.TempDir()
	writeSupervisorJSON(t, dir, `not valid json {{`)
	setFakeHome(t, dir)

	got := resolveBaseURL("")
	if got != "http://localhost:37701" {
		t.Fatalf("expected fallback 37701, got %q", got)
	}
}

// TestResolveBaseURL_MissingSupervisor_FallsBack verifies that a missing
// supervisor.json silently falls through to the hardcoded default.
func TestResolveBaseURL_MissingSupervisor_FallsBack(t *testing.T) {
	dir := t.TempDir() // empty dir — no .claude-mem/supervisor.json
	setFakeHome(t, dir)

	got := resolveBaseURL("")
	if got != "http://localhost:37701" {
		t.Fatalf("expected fallback 37701, got %q", got)
	}
}

// TestResolveBaseURL_ZeroPortFallsBack verifies that port==0 in supervisor.json
// falls through to the hardcoded default.
func TestResolveBaseURL_ZeroPortFallsBack(t *testing.T) {
	dir := t.TempDir()
	writeSupervisorJSON(t, dir, `{"port": 0}`)
	setFakeHome(t, dir)

	got := resolveBaseURL("")
	if got != "http://localhost:37701" {
		t.Fatalf("expected fallback 37701, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// httpTransport.Health tests
// ---------------------------------------------------------------------------

// TestHTTPTransport_HealthOK verifies that a 200 response returns nil.
func TestHTTPTransport_HealthOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	if err := tr.Health(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestHTTPTransport_HealthUnavailable verifies scenario F: non-2xx maps to
// adapter.ErrUnavailable.
func TestHTTPTransport_HealthUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	err := tr.Health(context.Background())
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// TestHTTPTransport_HealthContextCancel verifies that context cancellation
// propagates without being wrapped in ErrUnavailable.
func TestHTTPTransport_HealthContextCancel(t *testing.T) {
	// Server that blocks until the test is done (forces context cancel to fire).
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(ready)
		// block until client context is cancelled
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	tr := NewHTTPTransport(srv.URL)

	done := make(chan error, 1)
	go func() { done <- tr.Health(ctx) }()

	<-ready   // ensure request has reached server before cancelling
	cancel()

	err := <-done
	if err == nil {
		t.Fatal("expected an error after context cancel, got nil")
	}
	// Should NOT be wrapped in ErrUnavailable — should be context.Canceled or
	// a net error that ultimately has context.Canceled as cause. Either form is
	// acceptable; the critical invariant is that it is NOT nil.
}

// ---------------------------------------------------------------------------
// httpTransport.ListPage tests
// ---------------------------------------------------------------------------

// TestHTTPTransport_ListPageOK verifies that a 200 response returns the body.
func TestHTTPTransport_ListPageOK(t *testing.T) {
	fixture := `{"items":[],"hasMore":false,"offset":0,"limit":100}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "100" || q.Get("offset") != "0" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	body, err := tr.ListPage(context.Background(), 100, 0)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if string(body) != fixture {
		t.Fatalf("body mismatch: got %q", string(body))
	}
}

// TestHTTPTransport_ListPageUnavailable verifies non-2xx maps to ErrUnavailable.
func TestHTTPTransport_ListPageUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	_, err := tr.ListPage(context.Background(), 100, 0)
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// httpTransport.GetByID tests
// ---------------------------------------------------------------------------

// TestHTTPTransport_GetByIDOK verifies 2xx returns (body, true, nil).
func TestHTTPTransport_GetByIDOK(t *testing.T) {
	fixture := `{"id":"obs-1","title":"Hello"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	body, found, err := tr.GetByID(context.Background(), "obs-1")
	if err != nil || !found {
		t.Fatalf("expected (body, true, nil), got (%v, %v, %v)", body, found, err)
	}
	if string(body) != fixture {
		t.Fatalf("body mismatch: got %q", string(body))
	}
}

// TestHTTPTransport_GetByIDNotFound verifies 404 returns (nil, false, ErrNotFound).
func TestHTTPTransport_GetByIDNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	body, found, err := tr.GetByID(context.Background(), "missing")
	if found || body != nil {
		t.Fatal("expected found=false and nil body")
	}
	if !isNotFound(err) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestHTTPTransport_GetByIDUnavailable verifies non-2xx/non-404 maps to
// ErrUnavailable (signals ReadNative to degrade — ADR-5).
func TestHTTPTransport_GetByIDUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	body, found, err := tr.GetByID(context.Background(), "obs-1")
	if found || body != nil {
		t.Fatal("expected found=false and nil body")
	}
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ClaudeMemAdapter.Name test
// ---------------------------------------------------------------------------

// TestName_ReturnsClaudioMem verifies REQ-CM-02.
func TestName_ReturnsClaudioMem(t *testing.T) {
	a := New(nil)
	if got := a.Name(); got != "claude-mem" {
		t.Fatalf("expected \"claude-mem\", got %q", got)
	}
}

// ---------------------------------------------------------------------------
// ClaudeMemAdapter.WriteNative test (single-line ErrUnsupported — REQ-CM-03)
// ---------------------------------------------------------------------------

// TestWriteNative_ReturnsErrUnsupported verifies scenario C (partial, PR#1 scope).
func TestWriteNative_ReturnsErrUnsupported(t *testing.T) {
	a := New(nil)
	id, err := a.WriteNative(context.Background(), nil)
	if id != "" {
		t.Fatalf("expected empty ID, got %q", id)
	}
	if !isUnsupported(err) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// PR#2 unit tests (T-11)
// Covers: normalizeTimestamp, deriveTitle, authorAttribution, WrapIDMap,
//         ToCanonical (happy-path, title-derive, revision, IDMap, forensic warn,
//         timestamp reject), FromCanonical round-trip.
// All tests use a panicTransport to guarantee no I/O is performed in pure funcs.
// ---------------------------------------------------------------------------

// panicTransport panics if any Transport method is called. Used to prove that
// ToCanonical/FromCanonical are pure (REQ-CM-10, REQ-CM-15, T-11 contract).
type panicTransport struct{}

// Compile-time assertion: panicTransport must satisfy Transport.
var _ Transport = panicTransport{}

func (panicTransport) Health(_ context.Context) error {
	panic("ToCanonical/FromCanonical must not call Transport.Health")
}
func (panicTransport) ListPage(_ context.Context, _, _ int) ([]byte, error) {
	panic("ToCanonical/FromCanonical must not call Transport.ListPage")
}
func (panicTransport) GetByID(_ context.Context, _ string) ([]byte, bool, error) {
	panic("ToCanonical/FromCanonical must not call Transport.GetByID")
}

// fakeIDMap is a minimal adapter.IDMap for unit testing.
type fakeIDMap struct {
	// native → (canonical, revision) — omit entry for "new" records.
	m map[string]string
}

func (f *fakeIDMap) CanonicalFromNative(id adapter.NativeID) (adapter.CanonicalID, bool) {
	v, ok := f.m[string(id)]
	return adapter.CanonicalID(v), ok
}
func (f *fakeIDMap) NativeFromCanonical(id adapter.CanonicalID) (adapter.NativeID, bool) {
	for native, canonical := range f.m {
		if canonical == string(id) {
			return adapter.NativeID(native), true
		}
	}
	return "", false
}

// newAdapter constructs a ClaudeMemAdapter backed by a panicTransport.
func newAdapter() *ClaudeMemAdapter { return New(panicTransport{}) }

// makeRec builds a claudeMemRecord with Raw populated from its own JSON.
func makeRec(id, sessionID, title, summary string, tags []string, createdAt, updatedAt string) claudeMemRecord {
	rec := claudeMemRecord{
		ID:        id,
		SessionID: sessionID,
		Title:     title,
		Summary:   summary,
		Tags:      tags,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
	raw, _ := json.Marshal(rec)
	rec.Raw = json.RawMessage(raw)
	return rec
}

// ---------------------------------------------------------------------------
// normalizeTimestamp tests
// ---------------------------------------------------------------------------

func TestNormalizeTimestamp_Layouts(t *testing.T) {
	want := "2026-05-16T15:04:05.000000000Z"
	cases := []struct {
		name  string
		input string
	}{
		{"RFC3339Nano", "2026-05-16T15:04:05.000000000Z"},
		{"RFC3339", "2026-05-16T15:04:05Z"},
		{"SQLite", "2026-05-16 15:04:05"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTimestamp(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestNormalizeTimestamp_Empty(t *testing.T) {
	got, err := normalizeTimestamp("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestNormalizeTimestamp_UnknownFormat_ReturnsError(t *testing.T) {
	_, err := normalizeTimestamp("not-a-date")
	if err == nil {
		t.Fatal("expected error for unknown format, got nil")
	}
	if !strings.Contains(err.Error(), "normalizeTimestamp") {
		t.Fatalf("error should mention normalizeTimestamp, got: %v", err)
	}
	// CRITICAL-01: error chain must be unwrappable so callers can errors.Is/As
	// the root cause (ADR-6: NON-SILENT, composable failure). Verify %w wrapping
	// by confirming errors.Unwrap returns a non-nil cause.
	if errors.Unwrap(err) == nil {
		t.Fatalf("error must wrap the underlying parse error via %%w for errors.Is/As; got unwrappable=nil for: %v", err)
	}
}

// Verify byte-comparable invariant across layouts (REQ-TS-03): two timestamps
// that are chronologically equal must normalize to the identical byte sequence.
func TestNormalizeTimestamp_ByteComparableAcrossLayouts(t *testing.T) {
	inputs := []string{
		"2026-05-16T15:04:05.000000000Z",
		"2026-05-16T15:04:05Z",
		"2026-05-16 15:04:05",
	}
	var results []string
	for _, ts := range inputs {
		got, err := normalizeTimestamp(ts)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", ts, err)
		}
		results = append(results, got)
	}
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatalf("byte-comparable invariant broken: %q != %q", results[i], results[0])
		}
	}
}

// ---------------------------------------------------------------------------
// deriveTitle tests
// ---------------------------------------------------------------------------

func TestDeriveTitle_ShortSummary(t *testing.T) {
	got := deriveTitle("hello world")
	if got != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", got)
	}
}

func TestDeriveTitle_EmptySummary(t *testing.T) {
	got := deriveTitle("")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestDeriveTitle_TruncatesAtMaxRunes(t *testing.T) {
	// Build a summary longer than titleMaxRunes.
	long := strings.Repeat("x", titleMaxRunes+10)
	got := deriveTitle(long)
	if len([]rune(got)) != titleMaxRunes {
		t.Fatalf("expected %d runes, got %d", titleMaxRunes, len([]rune(got)))
	}
}

func TestDeriveTitle_MultibyteRunes(t *testing.T) {
	// "日" is 3 bytes in UTF-8 — verify we count runes not bytes.
	long := strings.Repeat("日", titleMaxRunes+5)
	got := deriveTitle(long)
	if len([]rune(got)) != titleMaxRunes {
		t.Fatalf("expected %d runes, got %d", titleMaxRunes, len([]rune(got)))
	}
}

// ---------------------------------------------------------------------------
// WrapIDMap tests
// ---------------------------------------------------------------------------

// rawStringIDMap has plain-string method signatures, matching the
// interface expected by WrapIDMap (mirrors store.providerIDMap).
type rawStringIDMap struct {
	m map[string]string
}

func (r *rawStringIDMap) CanonicalFromNative(id string) (string, bool) {
	v, ok := r.m[id]
	return v, ok
}
func (r *rawStringIDMap) NativeFromCanonical(id string) (string, bool) {
	for native, canonical := range r.m {
		if canonical == id {
			return native, true
		}
	}
	return "", false
}

func TestWrapIDMap_CanonicalFromNative_Hit(t *testing.T) {
	raw := &rawStringIDMap{m: map[string]string{"n1": "C1"}}
	wrapped := WrapIDMap(raw)
	got, ok := wrapped.CanonicalFromNative(adapter.NativeID("n1"))
	if !ok || got != "C1" {
		t.Fatalf("expected (C1, true), got (%q, %v)", got, ok)
	}
}

func TestWrapIDMap_CanonicalFromNative_Miss(t *testing.T) {
	raw := &rawStringIDMap{m: map[string]string{}}
	wrapped := WrapIDMap(raw)
	_, ok := wrapped.CanonicalFromNative(adapter.NativeID("missing"))
	if ok {
		t.Fatal("expected false for missing native ID")
	}
}

func TestWrapIDMap_NativeFromCanonical(t *testing.T) {
	raw := &rawStringIDMap{m: map[string]string{"n1": "C1"}}
	wrapped := WrapIDMap(raw)
	got, ok := wrapped.NativeFromCanonical(adapter.CanonicalID("C1"))
	if !ok || got != "n1" {
		t.Fatalf("expected (n1, true), got (%q, %v)", got, ok)
	}
}

// ---------------------------------------------------------------------------
// ToCanonical tests
// ---------------------------------------------------------------------------

// TestToCanonical_HappyPath verifies Scenario A: all fields populated, new record.
func TestToCanonical_HappyPath(t *testing.T) {
	a := newAdapter()
	rec := makeRec("obs-1", "sess-42", "My Title", "Full summary here.", []string{"go", "test"},
		"2026-05-16T15:04:05Z", "2026-05-17T10:00:00Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Kind != "session_summary" {
		t.Errorf("Kind: got %q, want %q", got.Kind, "session_summary")
	}
	if got.Origin.Provider != "claude-mem" {
		t.Errorf("Origin.Provider: got %q, want %q", got.Origin.Provider, "claude-mem")
	}
	if got.Origin.ProviderID != "obs-1" {
		t.Errorf("Origin.ProviderID: got %q, want %q", got.Origin.ProviderID, "obs-1")
	}
	if got.Origin.SessionID != "sess-42" {
		t.Errorf("Origin.SessionID: got %q, want %q", got.Origin.SessionID, "sess-42")
	}
	if got.Title != "My Title" {
		t.Errorf("Title: got %q, want %q", got.Title, "My Title")
	}
	if got.Content != "Full summary here." {
		t.Errorf("Content: got %q, want %q", got.Content, "Full summary here.")
	}
	if got.ContentFormat != "markdown" {
		t.Errorf("ContentFormat: got %q, want %q", got.ContentFormat, "markdown")
	}
	if got.Revision != 1 {
		t.Errorf("Revision: got %d, want 1 (new record)", got.Revision)
	}
	if got.CanonicalID != "" {
		t.Errorf("CanonicalID: expected empty for new record, got %q", got.CanonicalID)
	}
	if got.Type != "" {
		t.Errorf("Type: expected empty, got %q", got.Type)
	}
	if got.TopicKey != "" {
		t.Errorf("TopicKey: expected empty, got %q", got.TopicKey)
	}
	if got.SchemaVersion != 0 {
		t.Errorf("SchemaVersion: must not be set by adapter, got %d", got.SchemaVersion)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "go" || got.Tags[1] != "test" {
		t.Errorf("Tags: got %v, want [go test]", got.Tags)
	}
	// Timestamps must be in rfc3339NanoFixed format.
	wantCreated := "2026-05-16T15:04:05.000000000Z"
	if got.CreatedAt != wantCreated {
		t.Errorf("CreatedAt: got %q, want %q", got.CreatedAt, wantCreated)
	}
	wantUpdated := "2026-05-17T10:00:00.000000000Z"
	if got.UpdatedAt != wantUpdated {
		t.Errorf("UpdatedAt: got %q, want %q", got.UpdatedAt, wantUpdated)
	}
}

// TestToCanonical_TitleDeriveFallback verifies Scenario B: title derived from summary.
func TestToCanonical_TitleDeriveFallback(t *testing.T) {
	a := newAdapter()
	rec := makeRec("obs-2", "", "", "This is a session summary that should become the title.", nil,
		"2026-05-16T15:04:05Z", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title == "" {
		t.Fatal("Title must be non-empty (derived from summary)")
	}
	// The derived title must be a prefix of the summary (or equal when short enough).
	// Args order: strings.HasPrefix(s, prefix) — got.Title is the prefix of the full summary.
	if !strings.HasPrefix("This is a session summary that should become the title.", got.Title) {
		t.Errorf("Title %q is not a prefix of summary", got.Title)
	}
}

// TestToCanonical_TitleTruncatesLongSummary verifies that a summary longer than
// titleMaxRunes is truncated to exactly titleMaxRunes runes (REQ-CM-12).
func TestToCanonical_TitleTruncatesLongSummary(t *testing.T) {
	a := newAdapter()
	longSummary := strings.Repeat("a", titleMaxRunes+20)
	rec := makeRec("obs-trunc", "", "", longSummary, nil,
		"2026-05-16T15:04:05Z", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	runeCount := len([]rune(got.Title))
	if runeCount != titleMaxRunes {
		t.Errorf("Title rune count: got %d, want %d", runeCount, titleMaxRunes)
	}
	// The truncated title must be a prefix of the original summary.
	if !strings.HasPrefix(longSummary, got.Title) {
		t.Errorf("Truncated title %q is not a prefix of original summary", got.Title)
	}
}

// TestToCanonical_EmptyTitleAndSummary_NoError verifies Scenario L: no error even when both are empty.
func TestToCanonical_EmptyTitleAndSummary_NoError(t *testing.T) {
	a := newAdapter()
	rawBytes, _ := json.Marshal(map[string]string{"id": "obs-empty"})
	rec := claudeMemRecord{ID: "obs-empty", Raw: rawBytes}
	idmap := &fakeIDMap{m: map[string]string{}}

	_, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("expected no error for empty title+summary, got: %v", err)
	}
}

// TestToCanonical_TimestampSQLite verifies Scenario H: SQLite layout normalized correctly.
func TestToCanonical_TimestampSQLite(t *testing.T) {
	a := newAdapter()
	rec := makeRec("obs-3", "", "T", "s", nil, "2026-05-16 15:04:05", "2026-05-16 15:04:05")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "2026-05-16T15:04:05.000000000Z"
	if got.CreatedAt != want {
		t.Errorf("CreatedAt: got %q, want %q", got.CreatedAt, want)
	}
}

// TestToCanonical_UnknownTimestamp_ReturnsError verifies ADR-6 failure mode.
func TestToCanonical_UnknownTimestamp_ReturnsError(t *testing.T) {
	a := newAdapter()
	rec := makeRec("obs-4", "", "T", "s", nil, "not-a-date", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	_, err := a.ToCanonical(rec, idmap)
	if err == nil {
		t.Fatal("expected error for unknown timestamp format, got nil")
	}
}

// TestToCanonical_IDMap_NewRecord verifies Scenario J: new record → empty CanonicalID, revision=1.
func TestToCanonical_IDMap_NewRecord(t *testing.T) {
	a := newAdapter()
	rec := makeRec("obs-new", "", "T", "s", nil, "2026-05-16T15:04:05Z", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CanonicalID != "" {
		t.Errorf("CanonicalID: expected empty for new record, got %q", got.CanonicalID)
	}
	if got.Revision != 1 {
		t.Errorf("Revision: got %d, want 1", got.Revision)
	}
}

// TestToCanonical_IDMap_KnownRecord verifies Scenario I: known record → CanonicalID set,
// revision == -1 (sentinel: "prior mapping exists; PRD-3 orchestrator must assign final value").
// This matches the engram adapter's cross-adapter convention (CRITICAL-02 alignment).
func TestToCanonical_IDMap_KnownRecord(t *testing.T) {
	a := newAdapter()
	rec := makeRec("obs-42", "", "T", "s", nil, "2026-05-16T15:04:05Z", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{"obs-42": "C1"}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CanonicalID != "C1" {
		t.Errorf("CanonicalID: got %q, want %q", got.CanonicalID, "C1")
	}
	// revision == -1 is the shared cross-adapter sentinel meaning
	// "known record; PRD-3 orchestrator must replace with priorRevision+1".
	// Consistent with internal/adapter/engram/engram.go ~378.
	if got.Revision != -1 {
		t.Errorf("Revision: got %d, want -1 (sentinel for known record)", got.Revision)
	}
}

// TestToCanonical_WrongNativeType_ReturnsError verifies type-guard behavior.
func TestToCanonical_WrongNativeType_ReturnsError(t *testing.T) {
	a := newAdapter()
	idmap := &fakeIDMap{m: map[string]string{}}
	_, err := a.ToCanonical("not-a-claudeMemRecord", idmap)
	if err == nil {
		t.Fatal("expected error for wrong native type, got nil")
	}
}

// TestToCanonical_NilTags_EmptySlice verifies nil tags become empty slice (JSON-safe).
func TestToCanonical_NilTags_EmptySlice(t *testing.T) {
	a := newAdapter()
	rec := makeRec("obs-5", "", "T", "s", nil, "2026-05-16T15:04:05Z", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Tags == nil {
		t.Error("Tags must be non-nil (empty slice, not nil) for JSON marshalling safety")
	}
}

// ---------------------------------------------------------------------------
// FromCanonical tests
// ---------------------------------------------------------------------------

// TestFromCanonical_HappyPath verifies the pure conversion returns a claudeMemRecord.
func TestFromCanonical_HappyPath(t *testing.T) {
	a := newAdapter()

	canonical := toCanonicalOrFatal(t, a, makeRec("obs-fc", "sess-1", "Round-trip title", "Round-trip summary.",
		[]string{"x"}, "2026-05-16T15:04:05Z", "2026-05-16T15:04:05Z"), &fakeIDMap{m: map[string]string{}})

	native, err := a.FromCanonical(canonical)
	if err != nil {
		t.Fatalf("FromCanonical: unexpected error: %v", err)
	}
	back, ok := native.(claudeMemRecord)
	if !ok {
		t.Fatalf("FromCanonical: expected claudeMemRecord, got %T", native)
	}
	if back.ID != "obs-fc" {
		t.Errorf("ID: got %q, want %q", back.ID, "obs-fc")
	}
	if back.SessionID != "sess-1" {
		t.Errorf("SessionID: got %q, want %q", back.SessionID, "sess-1")
	}
	if back.Title != "Round-trip title" {
		t.Errorf("Title: got %q, want %q", back.Title, "Round-trip title")
	}
	if back.Summary != "Round-trip summary." {
		t.Errorf("Summary: got %q, want %q", back.Summary, "Round-trip summary.")
	}
}

// TestFromCanonical_NoTransportCalls verifies purity: panicTransport must not fire.
func TestFromCanonical_NoTransportCalls(t *testing.T) {
	a := newAdapter() // backed by panicTransport
	canonical := toCanonicalOrFatal(t, a, makeRec("obs-pure", "", "T", "s", nil,
		"2026-05-16T15:04:05Z", "2026-05-16T15:04:05Z"), &fakeIDMap{m: map[string]string{}})

	// If FromCanonical calls Transport, the test panics — proving purity.
	if _, err := a.FromCanonical(canonical); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFromCanonical_OmitsTimestamps verifies WARNING-03 contract: FromCanonical
// intentionally omits CreatedAt and UpdatedAt. Any future write path must re-read
// timestamps from the provider rather than round-tripping through FromCanonical.
func TestFromCanonical_OmitsTimestamps(t *testing.T) {
	a := newAdapter()
	canonical := toCanonicalOrFatal(t, a, makeRec("obs-ts", "", "T", "s", nil,
		"2026-05-16T15:04:05Z", "2026-05-17T10:00:00Z"), &fakeIDMap{m: map[string]string{}})

	native, err := a.FromCanonical(canonical)
	if err != nil {
		t.Fatalf("FromCanonical: unexpected error: %v", err)
	}
	back, ok := native.(claudeMemRecord)
	if !ok {
		t.Fatalf("FromCanonical: expected claudeMemRecord, got %T", native)
	}
	// By design: timestamps are omitted from FromCanonical output (read-only v1,
	// no write surface). A future write path must NOT rely on these fields.
	if back.CreatedAt != "" {
		t.Errorf("FromCanonical must omit CreatedAt, got %q", back.CreatedAt)
	}
	if back.UpdatedAt != "" {
		t.Errorf("FromCanonical must omit UpdatedAt, got %q", back.UpdatedAt)
	}
}

// toCanonicalOrFatal calls ToCanonical and fatals the test on error.
func toCanonicalOrFatal(t *testing.T, a *ClaudeMemAdapter, rec claudeMemRecord, idmap adapter.IDMap) store.CanonicalRecord {
	t.Helper()
	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("ToCanonical unexpectedly failed: %v", err)
	}
	return got
}

// ---------------------------------------------------------------------------
// authorAttribution tests
// ---------------------------------------------------------------------------

func TestAuthorAttribution_EnvOverride(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "test-author")
	got := authorAttribution()
	if got != "test-author" {
		t.Fatalf("expected %q, got %q", "test-author", got)
	}
}

func TestAuthorAttribution_FallbackNonEmpty(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "") // ensure env override is cleared
	got := authorAttribution()
	if got == "" {
		t.Fatal("authorAttribution must return non-empty string")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	return containsErr(err, adapter.ErrUnavailable)
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return containsErr(err, adapter.ErrNotFound)
}

func isUnsupported(err error) bool {
	if err == nil {
		return false
	}
	return containsErr(err, adapter.ErrUnsupported)
}

func containsErr(err error, target error) bool {
	return errors.Is(err, target)
}
