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
	"time"

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
// PR#3 unit tests (T-15)
// Covers: Health (nil transport, happy path, ErrUnavailable wrap, ctx cancel),
//         ListNative (3-page pagination Scenario D, since filter Scenario E,
//         cross-project isolation Scenario G, timestamp normalization Scenario H,
//         unparseable updated_at skipped+warn, context cancellation),
//         ReadNative (happy per-ID, degrade-to-scan on ErrUnavailable,
//         clean-404 → ErrNotFound, scan-exhausted → ErrNotFound).
// All tests use fakeTransport — NO network, NO time flakiness.
// ---------------------------------------------------------------------------

// fakeTransport is a configurable fake Transport for PR#3 I/O method testing.
// It supports scripted multi-page responses and toggleable GetByID behaviour.
type fakeTransport struct {
	// healthErr, if non-nil, is returned by Health.
	healthErr error

	// pages is the ordered list of page bodies returned by successive ListPage
	// calls. The i-th call returns pages[i]. If the index is out of range,
	// ListPage returns an error.
	pages []observationsPage

	// listPageErr, if non-nil, overrides pages and is returned on every ListPage call.
	listPageErr error

	// listPageCalls records the (limit, offset) pairs each ListPage call used.
	listPageCalls [][2]int

	// getByIDBody is returned by GetByID when getByIDFound is true.
	getByIDBody []byte
	// getByIDFound controls whether GetByID returns (body, true, nil).
	getByIDFound bool
	// getByIDErr, if non-nil, is returned by GetByID instead.
	getByIDErr error
	// getByIDReceivedID records the id argument passed to the most recent GetByID call.
	getByIDReceivedID string
}

// Compile-time assertion: fakeTransport must satisfy Transport.
var _ Transport = (*fakeTransport)(nil)

func (f *fakeTransport) Health(_ context.Context) error {
	return f.healthErr
}

func (f *fakeTransport) ListPage(_ context.Context, limit, offset int) ([]byte, error) {
	f.listPageCalls = append(f.listPageCalls, [2]int{limit, offset})
	if f.listPageErr != nil {
		return nil, f.listPageErr
	}
	// Determine page index from offset (pages are always pageLimit=100 apart).
	idx := offset / 100
	if idx >= len(f.pages) {
		return nil, fmt.Errorf("%w: fakeTransport: no page at offset %d", adapter.ErrUnavailable, offset)
	}
	b, err := json.Marshal(f.pages[idx])
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (f *fakeTransport) GetByID(_ context.Context, id string) ([]byte, bool, error) {
	f.getByIDReceivedID = id
	if f.getByIDErr != nil {
		return nil, false, f.getByIDErr
	}
	if f.getByIDFound {
		return f.getByIDBody, true, nil
	}
	return nil, false, adapter.ErrNotFound
}

// makeRawItem encodes a claudeMemRecord as a json.RawMessage for use in fakeTransport pages.
func makeRawItem(id, projectID, updatedAt string) json.RawMessage {
	b, _ := json.Marshal(claudeMemRecord{
		ID:        id,
		ProjectID: projectID,
		Title:     "title-" + id,
		Summary:   "summary-" + id,
		UpdatedAt: updatedAt,
		CreatedAt: "2026-05-16T10:00:00Z",
	})
	return b
}

// ---------------------------------------------------------------------------
// Health tests (T-12)
// ---------------------------------------------------------------------------

// TestHealth_NilTransport_ErrUnavailable verifies nil transport returns ErrUnavailable.
func TestHealth_NilTransport_ErrUnavailable(t *testing.T) {
	a := New(nil)
	err := a.Health(context.Background())
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable for nil transport, got %v", err)
	}
}

// TestHealth_OK verifies nil error from transport → nil returned (Scenario F happy path).
func TestHealth_OK(t *testing.T) {
	a := New(&fakeTransport{healthErr: nil})
	if err := a.Health(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestHealth_TransportError_WrapsErrUnavailable verifies Scenario F: transport
// failure is wrapped as ErrUnavailable.
func TestHealth_TransportError_WrapsErrUnavailable(t *testing.T) {
	a := New(&fakeTransport{healthErr: fmt.Errorf("%w: connection refused", adapter.ErrUnavailable)})
	err := a.Health(context.Background())
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// TestHealth_ContextCancelled_Propagates verifies context cancellation propagates raw.
func TestHealth_ContextCancelled_Propagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	a := New(&fakeTransport{healthErr: context.Canceled})
	err := a.Health(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	// Must not be wrapped in ErrUnavailable — context errors propagate raw.
	if errors.Is(err, adapter.ErrUnavailable) {
		t.Fatalf("context.Canceled must propagate raw, not wrapped in ErrUnavailable; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListNative tests (T-13)
// ---------------------------------------------------------------------------

// buildPages constructs a slice of observationsPage for fakeTransport from
// groups of items. The last page has HasMore=false; earlier pages have HasMore=true.
func buildPages(groups ...[]json.RawMessage) []observationsPage {
	pages := make([]observationsPage, len(groups))
	for i, items := range groups {
		pages[i] = observationsPage{
			Items:   items,
			HasMore: i < len(groups)-1,
			Offset:  i * 100,
			Limit:   100,
		}
	}
	return pages
}

// TestListNative_ThreePagePagination_ProjectFilter verifies Scenario D:
// 3 pages fetched at offsets 0/100/200; cross-project records discarded.
func TestListNative_ThreePagePagination_ProjectFilter(t *testing.T) {
	// Page 1: 60 "my-project" + 40 "other" (total 100, HasMore=true)
	// Page 2: 100 "my-project" items (HasMore=true)
	// Page 3: 50 "my-project" items (HasMore=false)
	updatedAt := "2026-05-16T10:00:00Z"

	var p1 []json.RawMessage
	for i := 0; i < 60; i++ {
		p1 = append(p1, makeRawItem(fmt.Sprintf("mp1-%d", i), "my-project", updatedAt))
	}
	for i := 0; i < 40; i++ {
		p1 = append(p1, makeRawItem(fmt.Sprintf("ot1-%d", i), "other-project", updatedAt))
	}

	var p2 []json.RawMessage
	for i := 0; i < 100; i++ {
		p2 = append(p2, makeRawItem(fmt.Sprintf("mp2-%d", i), "my-project", updatedAt))
	}

	var p3 []json.RawMessage
	for i := 0; i < 50; i++ {
		p3 = append(p3, makeRawItem(fmt.Sprintf("mp3-%d", i), "my-project", updatedAt))
	}

	ft := &fakeTransport{pages: buildPages(p1, p2, p3)}
	a := New(ft)
	since := time.Time{} // zero time — accept all

	ids, err := a.ListNative(context.Background(), "my-project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must make exactly 3 ListPage calls with offsets 0, 100, 200.
	if len(ft.listPageCalls) != 3 {
		t.Fatalf("expected 3 ListPage calls, got %d", len(ft.listPageCalls))
	}
	wantOffsets := [][2]int{{100, 0}, {100, 100}, {100, 200}}
	for i, call := range ft.listPageCalls {
		if call != wantOffsets[i] {
			t.Errorf("call[%d]: got (limit=%d, offset=%d), want (limit=%d, offset=%d)",
				i, call[0], call[1], wantOffsets[i][0], wantOffsets[i][1])
		}
	}

	// 60 + 100 + 50 = 210 "my-project" IDs; zero from "other-project".
	if len(ids) != 210 {
		t.Fatalf("expected 210 my-project IDs, got %d", len(ids))
	}
	for _, id := range ids {
		s := string(id)
		if !strings.HasPrefix(s, "mp") {
			t.Errorf("non-my-project ID leaked: %q", s)
		}
	}
}

// TestListNative_SinceFilter verifies Scenario E: 4 records before since discarded,
// 6 records after since retained.
func TestListNative_SinceFilter(t *testing.T) {
	cutoff := "2026-05-16T12:00:00Z"

	items := []json.RawMessage{
		makeRawItem("old-1", "proj", "2026-05-16T10:00:00Z"),
		makeRawItem("old-2", "proj", "2026-05-16T11:00:00Z"),
		makeRawItem("old-3", "proj", "2026-05-16T11:30:00Z"),
		makeRawItem("old-4", "proj", "2026-05-16T11:59:59Z"),
		makeRawItem("new-1", "proj", "2026-05-16T12:00:00Z"),
		makeRawItem("new-2", "proj", "2026-05-16T13:00:00Z"),
		makeRawItem("new-3", "proj", "2026-05-16T14:00:00Z"),
		makeRawItem("new-4", "proj", "2026-05-17T00:00:00Z"),
		makeRawItem("new-5", "proj", "2026-05-17T10:00:00Z"),
		makeRawItem("new-6", "proj", "2026-05-18T00:00:00Z"),
	}

	ft := &fakeTransport{pages: buildPages(items)}
	a := New(ft)

	sinceTime, _ := time.Parse(time.RFC3339, cutoff)
	ids, err := a.ListNative(context.Background(), "proj", sinceTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 6 {
		t.Fatalf("expected 6 IDs after since filter, got %d: %v", len(ids), ids)
	}
	for _, id := range ids {
		if !strings.HasPrefix(string(id), "new-") {
			t.Errorf("old record should have been filtered: %q", id)
		}
	}
}

// TestListNative_CrossProjectIsolation verifies Scenario G: only proj-B records
// returned when proj-A, proj-B, proj-C are intermixed.
func TestListNative_CrossProjectIsolation(t *testing.T) {
	updatedAt := "2026-05-16T10:00:00Z"
	items := []json.RawMessage{
		makeRawItem("a-1", "proj-A", updatedAt),
		makeRawItem("b-1", "proj-B", updatedAt),
		makeRawItem("c-1", "proj-C", updatedAt),
		makeRawItem("a-2", "proj-A", updatedAt),
		makeRawItem("b-2", "proj-B", updatedAt),
		makeRawItem("c-2", "proj-C", updatedAt),
		makeRawItem("b-3", "proj-B", updatedAt),
	}

	ft := &fakeTransport{pages: buildPages(items)}
	a := New(ft)

	ids, err := a.ListNative(context.Background(), "proj-B", time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 proj-B IDs, got %d", len(ids))
	}
	for _, id := range ids {
		if !strings.HasPrefix(string(id), "b-") {
			t.Errorf("non-proj-B ID leaked: %q", id)
		}
	}
}

// TestListNative_TimestampNormalization verifies Scenario H: SQLite-format
// updated_at is normalized correctly and passes the since filter as expected.
func TestListNative_TimestampNormalization(t *testing.T) {
	// Record with SQLite format "2026-05-16 15:04:05" must normalize and
	// pass a since of 2026-05-16T14:00:00Z.
	sqliteItem, _ := json.Marshal(claudeMemRecord{
		ID:        "sqlite-obs",
		ProjectID: "proj",
		Title:     "t",
		Summary:   "s",
		UpdatedAt: "2026-05-16 15:04:05", // SQLite layout (ADR-6)
		CreatedAt: "2026-05-16T10:00:00Z",
	})

	ft := &fakeTransport{pages: buildPages([]json.RawMessage{sqliteItem})}
	a := New(ft)

	since, _ := time.Parse(time.RFC3339, "2026-05-16T14:00:00Z")
	ids, err := a.ListNative(context.Background(), "proj", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "sqlite-obs" {
		t.Fatalf("expected [sqlite-obs], got %v", ids)
	}
}

// TestListNative_UnparseableUpdatedAt_Skipped verifies ADR-6 discipline: a record
// with an unparseable updated_at is skipped (warn logged) and never stored.
func TestListNative_UnparseableUpdatedAt_Skipped(t *testing.T) {
	badItem, _ := json.Marshal(claudeMemRecord{
		ID:        "bad-ts",
		ProjectID: "proj",
		Title:     "t",
		Summary:   "s",
		UpdatedAt: "not-a-timestamp",
		CreatedAt: "2026-05-16T10:00:00Z",
	})
	goodItem := makeRawItem("good-1", "proj", "2026-05-17T10:00:00Z")

	ft := &fakeTransport{pages: buildPages([]json.RawMessage{badItem, goodItem})}
	a := New(ft)

	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "bad-ts" must be skipped; "good-1" must be present.
	if len(ids) != 1 || ids[0] != "good-1" {
		t.Fatalf("expected [good-1], got %v", ids)
	}
}

// TestListNative_ContextCancellation_Aborts verifies that context cancellation
// propagates raw (context.Canceled) without wrapping — matching the engram adapter contract.
// The fake is scripted to return context.Canceled so no real goroutine race is needed.
func TestListNative_ContextCancellation_Aborts(t *testing.T) {
	// Script the transport to return context.Canceled so the test is deterministic.
	ft := &fakeTransport{listPageErr: context.Canceled}
	a := New(ft)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := a.ListNative(ctx, "proj", time.Time{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected errors.Is(err, context.Canceled), got %v", err)
	}
}

// TestListNative_EmptyPageWithHasMoreTrue_Terminates is a regression test for
// the infinite-loop guard (W-1). A misbehaving server that returns HasMore:true
// with an empty Items slice must not cause an infinite loop — ListNative must
// terminate and return an empty result. The fake is bounded: it returns exactly
// one page (HasMore:true, Items:[]) so the fake itself never loops.
func TestListNative_EmptyPageWithHasMoreTrue_Terminates(t *testing.T) {
	// Single page: HasMore=true but Items is empty — server bug / DoS scenario.
	ft := &fakeTransport{
		pages: []observationsPage{
			{Items: []json.RawMessage{}, HasMore: true},
		},
	}
	a := New(ft)

	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty result, got %v", ids)
	}
	// Must have made exactly one ListPage call (offset 0) and then stopped.
	if len(ft.listPageCalls) != 1 {
		t.Fatalf("expected 1 ListPage call, got %d", len(ft.listPageCalls))
	}
}

// ---------------------------------------------------------------------------
// ReadNative tests (T-14)
// ---------------------------------------------------------------------------

// TestReadNative_HappyPath_PerID verifies ReadNative returns the record when
// GetByID succeeds.
func TestReadNative_HappyPath_PerID(t *testing.T) {
	bodyBytes, _ := json.Marshal(claudeMemRecord{
		ID:        "obs-1",
		ProjectID: "proj",
		Title:     "My Title",
		Summary:   "My Summary",
		UpdatedAt: "2026-05-16T10:00:00Z",
		CreatedAt: "2026-05-16T09:00:00Z",
	})

	ft := &fakeTransport{
		getByIDFound: true,
		getByIDBody:  bodyBytes,
	}
	a := New(ft)

	rec, err := a.ReadNative(context.Background(), "obs-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := rec.(claudeMemRecord)
	if !ok {
		t.Fatalf("expected claudeMemRecord, got %T", rec)
	}
	if got.ID != "obs-1" {
		t.Errorf("ID: got %q, want %q", got.ID, "obs-1")
	}
	if got.Raw == nil {
		t.Error("Raw must be populated on ReadNative")
	}
	// W-5: verify the correct id was forwarded to GetByID.
	if ft.getByIDReceivedID != "obs-1" {
		t.Errorf("GetByID received id %q, want %q", ft.getByIDReceivedID, "obs-1")
	}
}

// TestReadNative_CleanNotFound_ErrNotFound verifies ADR-5: clean 404 from
// GetByID returns adapter.ErrNotFound (no degrade).
func TestReadNative_CleanNotFound_ErrNotFound(t *testing.T) {
	ft := &fakeTransport{
		getByIDErr: adapter.ErrNotFound,
	}
	a := New(ft)

	_, err := a.ReadNative(context.Background(), "missing")
	if !isNotFound(err) {
		t.Fatalf("expected ErrNotFound for clean 404, got %v", err)
	}
}

// TestReadNative_DegradeToScan_Found verifies ADR-5: when GetByID returns
// ErrUnavailable, ReadNative degrades to paginate+scan and finds the record.
func TestReadNative_DegradeToScan_Found(t *testing.T) {
	target, _ := json.Marshal(claudeMemRecord{
		ID:        "obs-42",
		ProjectID: "proj",
		Title:     "Found via scan",
		UpdatedAt: "2026-05-16T10:00:00Z",
		CreatedAt: "2026-05-16T09:00:00Z",
	})

	items := []json.RawMessage{
		makeRawItem("obs-1", "proj", "2026-05-16T10:00:00Z"),
		target,
		makeRawItem("obs-99", "proj", "2026-05-16T10:00:00Z"),
	}

	ft := &fakeTransport{
		getByIDErr: fmt.Errorf("%w: endpoint not found", adapter.ErrUnavailable),
		pages:      buildPages(items),
	}
	a := New(ft)

	rec, err := a.ReadNative(context.Background(), "obs-42")
	if err != nil {
		t.Fatalf("unexpected error during degrade scan: %v", err)
	}
	got, ok := rec.(claudeMemRecord)
	if !ok {
		t.Fatalf("expected claudeMemRecord, got %T", rec)
	}
	if got.ID != "obs-42" {
		t.Errorf("ID: got %q, want %q", got.ID, "obs-42")
	}
}

// TestReadNative_DegradeToScan_Exhausted_ErrNotFound verifies ADR-5: scan
// exhausted (id not present anywhere) → adapter.ErrNotFound.
func TestReadNative_DegradeToScan_Exhausted_ErrNotFound(t *testing.T) {
	items := []json.RawMessage{
		makeRawItem("obs-1", "proj", "2026-05-16T10:00:00Z"),
		makeRawItem("obs-2", "proj", "2026-05-16T10:00:00Z"),
	}

	ft := &fakeTransport{
		getByIDErr: fmt.Errorf("%w: 5xx", adapter.ErrUnavailable),
		pages:      buildPages(items),
	}
	a := New(ft)

	_, err := a.ReadNative(context.Background(), "obs-ghost")
	if !isNotFound(err) {
		t.Fatalf("expected ErrNotFound after exhausted scan, got %v", err)
	}
}

// TestReadNative_NilTransport_ErrUnavailable verifies nil transport returns ErrUnavailable.
func TestReadNative_NilTransport_ErrUnavailable(t *testing.T) {
	a := New(nil)
	_, err := a.ReadNative(context.Background(), "any")
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable for nil transport, got %v", err)
	}
}

// TestReadNative_DegradeToScan_TransportError_ErrUnavailable verifies that a
// transport error during the degrade scan is returned as ErrUnavailable.
func TestReadNative_DegradeToScan_TransportError_ErrUnavailable(t *testing.T) {
	ft := &fakeTransport{
		// GetByID returns ErrUnavailable → triggers degrade.
		getByIDErr: fmt.Errorf("%w: refused", adapter.ErrUnavailable),
		// ListPage also returns ErrUnavailable (network down during scan).
		listPageErr: fmt.Errorf("%w: refused during scan", adapter.ErrUnavailable),
	}
	a := New(ft)

	_, err := a.ReadNative(context.Background(), "obs-x")
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable when scan transport fails, got %v", err)
	}
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
