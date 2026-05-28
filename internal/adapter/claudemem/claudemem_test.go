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

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/store"
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

// TestHTTPTransport_GetByIDOK verifies 2xx with single-item envelope returns
// the bare record body. claude-mem returns the standard list envelope when
// queried by id (`?id=...`), and the transport unwraps it.
func TestHTTPTransport_GetByIDOK(t *testing.T) {
	record := `{"id":1,"title":"Hello"}`
	envelope := fmt.Sprintf(`{"items":[%s],"hasMore":false,"offset":0,"limit":1}`, record)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "1" {
			t.Errorf("expected query id=1, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, envelope)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	body, found, err := tr.GetByID(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("expected (body, true, nil), got (%v, %v, %v)", body, found, err)
	}
	if string(body) != record {
		t.Fatalf("body mismatch: got %q, want %q", string(body), record)
	}
}

// TestHTTPTransport_GetByIDNotFound verifies an empty items[] envelope (2xx with
// 0 results) returns ErrNotFound. This is the only clean not-found path —
// claude-mem does not 404 on a missing id, it returns an empty envelope.
func TestHTTPTransport_GetByIDNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[],"hasMore":false,"offset":0,"limit":0}`)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	body, found, err := tr.GetByID(context.Background(), "9999")
	if found || body != nil {
		t.Fatal("expected found=false and nil body")
	}
	if !isNotFound(err) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestHTTPTransport_GetByIDRouteMissing verifies a 404 from the worker is
// treated as ErrUnavailable (route missing), not ErrNotFound. This signals
// ReadNative to degrade to paginate+scan (ADR-5).
func TestHTTPTransport_GetByIDRouteMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	body, found, err := tr.GetByID(context.Background(), "1")
	if found || body != nil {
		t.Fatal("expected found=false and nil body")
	}
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable (route missing), got %v", err)
	}
}

// TestHTTPTransport_GetByIDUnavailable verifies non-2xx maps to ErrUnavailable.
func TestHTTPTransport_GetByIDUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	body, found, err := tr.GetByID(context.Background(), "1")
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
	a := New(Config{}, nil)
	if got := a.Name(); got != "claude-mem" {
		t.Fatalf("expected \"claude-mem\", got %q", got)
	}
}

// ---------------------------------------------------------------------------
// ClaudeMemAdapter.WriteNative test (single-line ErrUnsupported — REQ-CM-03)
// ---------------------------------------------------------------------------

// TestWriteNative_ReturnsErrUnsupported verifies scenario C (partial, PR#1 scope).
func TestWriteNative_ReturnsErrUnsupported(t *testing.T) {
	a := New(Config{}, nil)
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
func (panicTransport) SaveMemory(_ context.Context, _ SaveMemoryRequest) (*SaveMemoryResponse, error) {
	panic("ToCanonical/FromCanonical must not call Transport.SaveMemory")
}
func (panicTransport) WriteSupported(_ context.Context) bool {
	panic("ToCanonical/FromCanonical must not call Transport.WriteSupported")
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
func newAdapter() *ClaudeMemAdapter { return New(Config{}, panicTransport{}) }

// makeRec builds a claudeMemRecord with Raw populated from its own JSON.
// Signature reflects the verified live shape (2026-05-20): numeric id,
// memory_session_id, project (name), type vocab, narrative as content,
// created_at only (no updated_at).
func makeRec(id int64, memorySessionID, title, narrative, recType, project, createdAt string) claudeMemRecord {
	rec := claudeMemRecord{
		ID:              id,
		MemorySessionID: memorySessionID,
		Project:         project,
		Type:            recType,
		Title:           title,
		Narrative:       narrative,
		CreatedAt:       createdAt,
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
// Uses verified shape (2026-05-20): type vocab passed through; tags always empty;
// UpdatedAt mirrors CreatedAt (claude-mem is append-only, D7).
func TestToCanonical_HappyPath(t *testing.T) {
	a := newAdapter()
	rec := makeRec(1, "sess-42", "My Title", "Full narrative here.", "discovery", "proj",
		"2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Kind != "session_summary" {
		t.Errorf("Kind: got %q, want %q (ADR-9 invariant)", got.Kind, "session_summary")
	}
	if got.Origin.Provider != "claude-mem" {
		t.Errorf("Origin.Provider: got %q, want %q", got.Origin.Provider, "claude-mem")
	}
	if got.Origin.ProviderID != "1" {
		t.Errorf("Origin.ProviderID: got %q, want %q", got.Origin.ProviderID, "1")
	}
	if got.Origin.SessionID != "sess-42" {
		t.Errorf("Origin.SessionID: got %q, want %q", got.Origin.SessionID, "sess-42")
	}
	if got.Title != "My Title" {
		t.Errorf("Title: got %q, want %q", got.Title, "My Title")
	}
	if got.Content != "Full narrative here." {
		t.Errorf("Content: got %q, want %q", got.Content, "Full narrative here.")
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
	// Type now preserves the claude-mem vocabulary (decision 1A, 2026-05-20).
	if got.Type != "discovery" {
		t.Errorf("Type: got %q, want %q (claude-mem vocab pass-through)", got.Type, "discovery")
	}
	if got.TopicKey != "" {
		t.Errorf("TopicKey: expected empty, got %q", got.TopicKey)
	}
	if got.SchemaVersion != 0 {
		t.Errorf("SchemaVersion: must not be set by adapter, got %d", got.SchemaVersion)
	}
	// claude-mem has no native tags; canonical Tags is always [].
	if got.Tags == nil || len(got.Tags) != 0 {
		t.Errorf("Tags: got %v, want [] (claude-mem has no native tags)", got.Tags)
	}
	// Timestamps must be in rfc3339NanoFixed format; UpdatedAt mirrors CreatedAt (D7).
	want := "2026-05-16T15:04:05.000000000Z"
	if got.CreatedAt != want {
		t.Errorf("CreatedAt: got %q, want %q", got.CreatedAt, want)
	}
	if got.UpdatedAt != want {
		t.Errorf("UpdatedAt: got %q, want %q (must mirror CreatedAt — D7)", got.UpdatedAt, want)
	}
}

// TestToCanonical_TitleDeriveFallback verifies Scenario B: title derived from narrative
// when both Title and Subtitle are empty.
func TestToCanonical_TitleDeriveFallback(t *testing.T) {
	a := newAdapter()
	rec := makeRec(2, "", "", "This is a session narrative that should become the title.", "", "",
		"2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title == "" {
		t.Fatal("Title must be non-empty (derived from narrative)")
	}
	if !strings.HasPrefix("This is a session narrative that should become the title.", got.Title) {
		t.Errorf("Title %q is not a prefix of narrative", got.Title)
	}
}

// TestToCanonical_TitleDeriveFromSubtitle verifies that Subtitle is used as the
// second-tier fallback when Title is empty but Subtitle is present.
func TestToCanonical_TitleDeriveFromSubtitle(t *testing.T) {
	a := newAdapter()
	rec := claudeMemRecord{
		ID:        99,
		Subtitle:  "Subtitle as fallback title",
		Narrative: "Long narrative body that should NOT be used when subtitle exists.",
		CreatedAt: "2026-05-16T15:04:05Z",
	}
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "Subtitle as fallback title" {
		t.Errorf("Title: got %q, want %q (Subtitle fallback)", got.Title, "Subtitle as fallback title")
	}
}

// TestToCanonical_TitleTruncatesLongNarrative verifies that a narrative longer than
// titleMaxRunes is truncated to exactly titleMaxRunes runes (REQ-CM-12).
func TestToCanonical_TitleTruncatesLongNarrative(t *testing.T) {
	a := newAdapter()
	longNarrative := strings.Repeat("a", titleMaxRunes+20)
	rec := makeRec(3, "", "", longNarrative, "", "", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	runeCount := len([]rune(got.Title))
	if runeCount != titleMaxRunes {
		t.Errorf("Title rune count: got %d, want %d", runeCount, titleMaxRunes)
	}
	if !strings.HasPrefix(longNarrative, got.Title) {
		t.Errorf("Truncated title %q is not a prefix of original narrative", got.Title)
	}
}

// TestToCanonical_EmptyTitleAndNarrative_NoError verifies Scenario L: no error
// even when both Title and Narrative are empty (only a forensic WARN is logged).
func TestToCanonical_EmptyTitleAndNarrative_NoError(t *testing.T) {
	a := newAdapter()
	rawBytes, _ := json.Marshal(map[string]int64{"id": 7})
	rec := claudeMemRecord{ID: 7, Raw: rawBytes}
	idmap := &fakeIDMap{m: map[string]string{}}

	_, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("expected no error for empty title+narrative, got: %v", err)
	}
}

// TestToCanonical_TimestampSQLite verifies Scenario H: SQLite layout normalized correctly.
func TestToCanonical_TimestampSQLite(t *testing.T) {
	a := newAdapter()
	rec := makeRec(4, "", "T", "n", "", "", "2026-05-16 15:04:05")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "2026-05-16T15:04:05.000000000Z"
	if got.CreatedAt != want {
		t.Errorf("CreatedAt: got %q, want %q", got.CreatedAt, want)
	}
	if got.UpdatedAt != want {
		t.Errorf("UpdatedAt: got %q, want %q (must mirror CreatedAt — D7)", got.UpdatedAt, want)
	}
}

// TestToCanonical_UnknownTimestamp_ReturnsError verifies ADR-6 failure mode.
func TestToCanonical_UnknownTimestamp_ReturnsError(t *testing.T) {
	a := newAdapter()
	rec := makeRec(5, "", "T", "n", "", "", "not-a-date")
	idmap := &fakeIDMap{m: map[string]string{}}

	_, err := a.ToCanonical(rec, idmap)
	if err == nil {
		t.Fatal("expected error for unknown timestamp format, got nil")
	}
}

// TestToCanonical_IDMap_NewRecord verifies Scenario J: new record → empty CanonicalID, revision=1.
func TestToCanonical_IDMap_NewRecord(t *testing.T) {
	a := newAdapter()
	rec := makeRec(10, "", "T", "n", "", "", "2026-05-16T15:04:05Z")
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
// Matches the engram adapter's cross-adapter convention (ADR-12).
func TestToCanonical_IDMap_KnownRecord(t *testing.T) {
	a := newAdapter()
	rec := makeRec(42, "", "T", "n", "", "", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{"42": "C1"}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CanonicalID != "C1" {
		t.Errorf("CanonicalID: got %q, want %q", got.CanonicalID, "C1")
	}
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

// TestToCanonical_Tags_AlwaysEmpty verifies decision D4: canonical Tags is always
// [] (non-nil empty slice) regardless of input. claude-mem has no native tags.
func TestToCanonical_Tags_AlwaysEmpty(t *testing.T) {
	a := newAdapter()
	rec := makeRec(6, "", "T", "n", "", "", "2026-05-16T15:04:05Z")
	idmap := &fakeIDMap{m: map[string]string{}}

	got, err := a.ToCanonical(rec, idmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Tags == nil {
		t.Fatal("Tags must be non-nil empty slice (JSON marshalling safety)")
	}
	if len(got.Tags) != 0 {
		t.Errorf("Tags must be empty (claude-mem has no native tags); got %v", got.Tags)
	}
}

// TestToCanonical_TypePassThrough verifies decision 1A: claude-mem `type` vocab
// is preserved verbatim in canonical Type. Kind stays "session_summary" (ADR-9).
func TestToCanonical_TypePassThrough(t *testing.T) {
	a := newAdapter()
	cases := []string{"discovery", "feature", "bugfix", "decision", "session_summary", "architecture"}
	for _, vocab := range cases {
		t.Run(vocab, func(t *testing.T) {
			rec := makeRec(int64(100), "", "T", "n", vocab, "", "2026-05-16T15:04:05Z")
			got, err := a.ToCanonical(rec, &fakeIDMap{m: map[string]string{}})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Type != vocab {
				t.Errorf("Type: got %q, want %q (claude-mem vocab pass-through)", got.Type, vocab)
			}
			if got.Kind != "session_summary" {
				t.Errorf("Kind: got %q, want %q (ADR-9 invariant — Kind never varies)", got.Kind, "session_summary")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FromCanonical tests
// ---------------------------------------------------------------------------

// TestFromCanonical_HappyPath verifies the pure conversion returns a claudeMemRecord.
func TestFromCanonical_HappyPath(t *testing.T) {
	a := newAdapter()

	canonical := toCanonicalOrFatal(t, a, makeRec(77, "sess-1", "Round-trip title", "Round-trip narrative.",
		"decision", "", "2026-05-16T15:04:05Z"), &fakeIDMap{m: map[string]string{}})

	native, err := a.FromCanonical(canonical)
	if err != nil {
		t.Fatalf("FromCanonical: unexpected error: %v", err)
	}
	back, ok := native.(claudeMemRecord)
	if !ok {
		t.Fatalf("FromCanonical: expected claudeMemRecord, got %T", native)
	}
	if back.ID != 77 {
		t.Errorf("ID: got %d, want %d", back.ID, 77)
	}
	if back.MemorySessionID != "sess-1" {
		t.Errorf("MemorySessionID: got %q, want %q", back.MemorySessionID, "sess-1")
	}
	if back.Title != "Round-trip title" {
		t.Errorf("Title: got %q, want %q", back.Title, "Round-trip title")
	}
	if back.Narrative != "Round-trip narrative." {
		t.Errorf("Narrative: got %q, want %q", back.Narrative, "Round-trip narrative.")
	}
	if back.Type != "decision" {
		t.Errorf("Type: got %q, want %q", back.Type, "decision")
	}
}

// TestFromCanonical_NoTransportCalls verifies purity: panicTransport must not fire.
func TestFromCanonical_NoTransportCalls(t *testing.T) {
	a := newAdapter()
	canonical := toCanonicalOrFatal(t, a, makeRec(11, "", "T", "n", "", "", "2026-05-16T15:04:05Z"),
		&fakeIDMap{m: map[string]string{}})

	if _, err := a.FromCanonical(canonical); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFromCanonical_OmitsTimestamps verifies the read-only contract: FromCanonical
// intentionally omits CreatedAt. Any future write path must re-read timestamps from
// the provider rather than round-tripping through FromCanonical.
func TestFromCanonical_OmitsTimestamps(t *testing.T) {
	a := newAdapter()
	canonical := toCanonicalOrFatal(t, a, makeRec(12, "", "T", "n", "", "", "2026-05-16T15:04:05Z"),
		&fakeIDMap{m: map[string]string{}})

	native, err := a.FromCanonical(canonical)
	if err != nil {
		t.Fatalf("FromCanonical: unexpected error: %v", err)
	}
	back, ok := native.(claudeMemRecord)
	if !ok {
		t.Fatalf("FromCanonical: expected claudeMemRecord, got %T", native)
	}
	if back.CreatedAt != "" {
		t.Errorf("FromCanonical must omit CreatedAt, got %q", back.CreatedAt)
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

func (f *fakeTransport) SaveMemory(_ context.Context, _ SaveMemoryRequest) (*SaveMemoryResponse, error) {
	return nil, fmt.Errorf("fakeTransport: SaveMemory not configured")
}

func (f *fakeTransport) WriteSupported(_ context.Context) bool {
	return false
}

// makeRawItem encodes a claudeMemRecord as a json.RawMessage for use in fakeTransport pages.
// Uses verified live field names: numeric id, project (name), created_at as the
// since-filter source (no updated_at exists in the API — D7).
func makeRawItem(id int64, project, createdAt string) json.RawMessage {
	b, _ := json.Marshal(claudeMemRecord{
		ID:        id,
		Project:   project,
		Title:     fmt.Sprintf("title-%d", id),
		Narrative: fmt.Sprintf("narrative-%d", id),
		CreatedAt: createdAt,
	})
	return b
}

// idStr is a convenience for tests comparing adapter.NativeID against int64 ids.
func idStr(id int64) string { return fmt.Sprintf("%d", id) }

// ---------------------------------------------------------------------------
// Health tests (T-12)
// ---------------------------------------------------------------------------

// TestHealth_NilTransport_ErrUnavailable verifies nil transport returns ErrUnavailable.
func TestHealth_NilTransport_ErrUnavailable(t *testing.T) {
	a := New(Config{}, nil)
	err := a.Health(context.Background())
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable for nil transport, got %v", err)
	}
}

// TestHealth_OK verifies nil error from transport → nil returned (Scenario F happy path).
func TestHealth_OK(t *testing.T) {
	a := New(Config{}, &fakeTransport{healthErr: nil})
	if err := a.Health(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestHealth_TransportError_WrapsErrUnavailable verifies Scenario F: transport
// failure is wrapped as ErrUnavailable.
func TestHealth_TransportError_WrapsErrUnavailable(t *testing.T) {
	a := New(Config{}, &fakeTransport{healthErr: fmt.Errorf("%w: connection refused", adapter.ErrUnavailable)})
	err := a.Health(context.Background())
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// TestHealth_ContextCancelled_Propagates verifies context cancellation propagates raw.
func TestHealth_ContextCancelled_Propagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	a := New(Config{}, &fakeTransport{healthErr: context.Canceled})
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
	createdAt := "2026-05-16T10:00:00Z"

	// IDs are partitioned by range so the test can identify the project of each
	// returned ID without parsing: 100000+ = my-project, 200000+ = other.
	var p1 []json.RawMessage
	for i := 0; i < 60; i++ {
		p1 = append(p1, makeRawItem(int64(100000+i), "my-project", createdAt))
	}
	for i := 0; i < 40; i++ {
		p1 = append(p1, makeRawItem(int64(200000+i), "other-project", createdAt))
	}

	var p2 []json.RawMessage
	for i := 0; i < 100; i++ {
		p2 = append(p2, makeRawItem(int64(110000+i), "my-project", createdAt))
	}

	var p3 []json.RawMessage
	for i := 0; i < 50; i++ {
		p3 = append(p3, makeRawItem(int64(120000+i), "my-project", createdAt))
	}

	ft := &fakeTransport{pages: buildPages(p1, p2, p3)}
	a := New(Config{}, ft)
	since := time.Time{} // zero time — accept all

	ids, err := a.ListNative(context.Background(), "my-project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
		// my-project IDs fall in 100000-119999; other-project in 200000+.
		s := string(id)
		if strings.HasPrefix(s, "2") {
			t.Errorf("non-my-project ID leaked: %q", s)
		}
	}
}

// TestListNative_SinceFilter verifies Scenario E: 4 records before since discarded,
// 6 records after since retained. The filter is rebased on created_at (D7).
// IDs <1000 are "old" (before cutoff); IDs >=1000 are "new".
func TestListNative_SinceFilter(t *testing.T) {
	cutoff := "2026-05-16T12:00:00Z"

	items := []json.RawMessage{
		makeRawItem(1, "proj", "2026-05-16T10:00:00Z"),
		makeRawItem(2, "proj", "2026-05-16T11:00:00Z"),
		makeRawItem(3, "proj", "2026-05-16T11:30:00Z"),
		makeRawItem(4, "proj", "2026-05-16T11:59:59Z"),
		makeRawItem(1001, "proj", "2026-05-16T12:00:00Z"),
		makeRawItem(1002, "proj", "2026-05-16T13:00:00Z"),
		makeRawItem(1003, "proj", "2026-05-16T14:00:00Z"),
		makeRawItem(1004, "proj", "2026-05-17T00:00:00Z"),
		makeRawItem(1005, "proj", "2026-05-17T10:00:00Z"),
		makeRawItem(1006, "proj", "2026-05-18T00:00:00Z"),
	}

	ft := &fakeTransport{pages: buildPages(items)}
	a := New(Config{}, ft)

	sinceTime, _ := time.Parse(time.RFC3339, cutoff)
	ids, err := a.ListNative(context.Background(), "proj", sinceTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 6 {
		t.Fatalf("expected 6 IDs after since filter, got %d: %v", len(ids), ids)
	}
	for _, id := range ids {
		s := string(id)
		if len(s) < 4 {
			t.Errorf("old record should have been filtered: %q", id)
		}
	}
}

// TestListNative_CrossProjectIsolation verifies Scenario G: only proj-B records
// returned when proj-A, proj-B, proj-C are intermixed. IDs <2000 = A, 2000-2999 = B,
// 3000+ = C — chosen so the assertion can identify project from the returned ID.
func TestListNative_CrossProjectIsolation(t *testing.T) {
	createdAt := "2026-05-16T10:00:00Z"
	items := []json.RawMessage{
		makeRawItem(1001, "proj-A", createdAt),
		makeRawItem(2001, "proj-B", createdAt),
		makeRawItem(3001, "proj-C", createdAt),
		makeRawItem(1002, "proj-A", createdAt),
		makeRawItem(2002, "proj-B", createdAt),
		makeRawItem(3002, "proj-C", createdAt),
		makeRawItem(2003, "proj-B", createdAt),
	}

	ft := &fakeTransport{pages: buildPages(items)}
	a := New(Config{}, ft)

	ids, err := a.ListNative(context.Background(), "proj-B", time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 proj-B IDs, got %d", len(ids))
	}
	for _, id := range ids {
		s := string(id)
		if !strings.HasPrefix(s, "2") {
			t.Errorf("non-proj-B ID leaked: %q", id)
		}
	}
}

// TestListNative_TimestampNormalization verifies Scenario H: SQLite-format
// created_at is normalized correctly and passes the since filter as expected.
func TestListNative_TimestampNormalization(t *testing.T) {
	sqliteItem, _ := json.Marshal(claudeMemRecord{
		ID:        500,
		Project:   "proj",
		Title:     "t",
		Narrative: "n",
		CreatedAt: "2026-05-16 15:04:05", // SQLite layout (ADR-6)
	})

	ft := &fakeTransport{pages: buildPages([]json.RawMessage{sqliteItem})}
	a := New(Config{}, ft)

	since, _ := time.Parse(time.RFC3339, "2026-05-16T14:00:00Z")
	ids, err := a.ListNative(context.Background(), "proj", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || string(ids[0]) != "500" {
		t.Fatalf("expected [500], got %v", ids)
	}
}

// TestListNative_UnparseableCreatedAt_Skipped verifies ADR-6 discipline: a record
// with an unparseable created_at is skipped (warn logged) and never stored.
// Rebased from updated_at to created_at (D7).
func TestListNative_UnparseableCreatedAt_Skipped(t *testing.T) {
	badItem, _ := json.Marshal(claudeMemRecord{
		ID:        666,
		Project:   "proj",
		Title:     "t",
		Narrative: "n",
		CreatedAt: "not-a-timestamp",
	})
	goodItem := makeRawItem(777, "proj", "2026-05-17T10:00:00Z")

	ft := &fakeTransport{pages: buildPages([]json.RawMessage{badItem, goodItem})}
	a := New(Config{}, ft)

	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || string(ids[0]) != "777" {
		t.Fatalf("expected [777], got %v", ids)
	}
}

// TestListNative_ContextCancellation_Aborts verifies that context cancellation
// propagates raw (context.Canceled) without wrapping — matching the engram adapter contract.
// The fake is scripted to return context.Canceled so no real goroutine race is needed.
func TestListNative_ContextCancellation_Aborts(t *testing.T) {
	// Script the transport to return context.Canceled so the test is deterministic.
	ft := &fakeTransport{listPageErr: context.Canceled}
	a := New(Config{}, ft)

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
	a := New(Config{}, ft)

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
		ID:        1,
		Project:   "proj",
		Title:     "My Title",
		Narrative: "My Narrative",
		CreatedAt: "2026-05-16T09:00:00Z",
	})

	ft := &fakeTransport{
		getByIDFound: true,
		getByIDBody:  bodyBytes,
	}
	a := New(Config{}, ft)

	rec, err := a.ReadNative(context.Background(), "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := rec.(claudeMemRecord)
	if !ok {
		t.Fatalf("expected claudeMemRecord, got %T", rec)
	}
	if got.ID != 1 {
		t.Errorf("ID: got %d, want %d", got.ID, 1)
	}
	if got.Raw == nil {
		t.Error("Raw must be populated on ReadNative")
	}
	if ft.getByIDReceivedID != "1" {
		t.Errorf("GetByID received id %q, want %q", ft.getByIDReceivedID, "1")
	}
}

// TestReadNative_CleanNotFound_ErrNotFound verifies ADR-5: clean 404 from
// GetByID returns adapter.ErrNotFound (no degrade).
func TestReadNative_CleanNotFound_ErrNotFound(t *testing.T) {
	ft := &fakeTransport{
		getByIDErr: adapter.ErrNotFound,
	}
	a := New(Config{}, ft)

	_, err := a.ReadNative(context.Background(), "missing")
	if !isNotFound(err) {
		t.Fatalf("expected ErrNotFound for clean 404, got %v", err)
	}
}

// TestReadNative_DegradeToScan_Found verifies ADR-5: when GetByID returns
// ErrUnavailable, ReadNative degrades to paginate+scan and finds the record.
func TestReadNative_DegradeToScan_Found(t *testing.T) {
	target, _ := json.Marshal(claudeMemRecord{
		ID:        42,
		Project:   "proj",
		Title:     "Found via scan",
		Narrative: "n",
		CreatedAt: "2026-05-16T09:00:00Z",
	})

	items := []json.RawMessage{
		makeRawItem(1, "proj", "2026-05-16T10:00:00Z"),
		target,
		makeRawItem(99, "proj", "2026-05-16T10:00:00Z"),
	}

	ft := &fakeTransport{
		getByIDErr: fmt.Errorf("%w: endpoint not found", adapter.ErrUnavailable),
		pages:      buildPages(items),
	}
	a := New(Config{}, ft)

	rec, err := a.ReadNative(context.Background(), "42")
	if err != nil {
		t.Fatalf("unexpected error during degrade scan: %v", err)
	}
	got, ok := rec.(claudeMemRecord)
	if !ok {
		t.Fatalf("expected claudeMemRecord, got %T", rec)
	}
	if got.ID != 42 {
		t.Errorf("ID: got %d, want %d", got.ID, 42)
	}
}

// TestReadNative_DegradeToScan_Exhausted_ErrNotFound verifies ADR-5: scan
// exhausted (id not present anywhere) → adapter.ErrNotFound.
func TestReadNative_DegradeToScan_Exhausted_ErrNotFound(t *testing.T) {
	items := []json.RawMessage{
		makeRawItem(1, "proj", "2026-05-16T10:00:00Z"),
		makeRawItem(2, "proj", "2026-05-16T10:00:00Z"),
	}

	ft := &fakeTransport{
		getByIDErr: fmt.Errorf("%w: 5xx", adapter.ErrUnavailable),
		pages:      buildPages(items),
	}
	a := New(Config{}, ft)

	_, err := a.ReadNative(context.Background(), "9999")
	if !isNotFound(err) {
		t.Fatalf("expected ErrNotFound after exhausted scan, got %v", err)
	}
}

// TestReadNative_NilTransport_ErrUnavailable verifies nil transport returns ErrUnavailable.
func TestReadNative_NilTransport_ErrUnavailable(t *testing.T) {
	a := New(Config{}, nil)
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
	a := New(Config{}, ft)

	_, err := a.ReadNative(context.Background(), "obs-x")
	if !isUnavailable(err) {
		t.Fatalf("expected ErrUnavailable when scan transport fails, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Author attribution tests (via adapter.cfg.Author → identity.Resolve)
// ---------------------------------------------------------------------------

// TestAuthorAttribution_EnvOverride verifies that WRAPPER_MEMS_AUTHOR is used
// as the canonical author when set, propagated through the adapter cfg.
func TestAuthorAttribution_EnvOverride(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "test-author")
	a := New(Config{}, nil) // Config.Author="" → identity.Resolve("") → env wins
	if a.cfg.Author != "test-author" {
		t.Fatalf("expected %q, got %q", "test-author", a.cfg.Author)
	}
}

// TestAuthorAttribution_FallbackNonEmpty verifies that a non-empty author is
// always resolved when WRAPPER_MEMS_AUTHOR is not set.
func TestAuthorAttribution_FallbackNonEmpty(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "")
	a := New(Config{}, nil)
	if a.cfg.Author == "" {
		t.Fatal("cfg.Author must be non-empty after construction")
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

// ---------------------------------------------------------------------------
// Phase 2 — WriteCapability tests (REQ-CMW-03)
// ---------------------------------------------------------------------------

// writeCapTransport is a minimal Transport that returns a configurable
// WriteSupported result. All other methods delegate to fakeTransport no-ops.
type writeCapTransport struct {
	fakeTransport
	writeSupported bool
}

func (w *writeCapTransport) WriteSupported(_ context.Context) bool {
	return w.writeSupported
}

// TestWriteCapability_WriteEnabledFalse verifies the config gate takes priority.
func TestWriteCapability_WriteEnabledFalse(t *testing.T) {
	tr := &writeCapTransport{writeSupported: true}
	a := New(Config{WriteEnabled: false}, tr)
	got := a.WriteCapability()
	want := "read-only (write_enabled=false)"
	if got != want {
		t.Errorf("WriteCapability: got %q, want %q", got, want)
	}
}

// TestWriteCapability_WriteEnabledTrueProbeTrue verifies "read+write" when config
// and probe both allow writes.
func TestWriteCapability_WriteEnabledTrueProbeTrue(t *testing.T) {
	tr := &writeCapTransport{writeSupported: true}
	a := New(Config{WriteEnabled: true}, tr)
	got := a.WriteCapability()
	want := "read+write"
	if got != want {
		t.Errorf("WriteCapability: got %q, want %q", got, want)
	}
}

// TestWriteCapability_WriteEnabledTrueProbeFailure verifies the endpoint-missing
// string when config enables writes but the worker doesn't expose the route.
func TestWriteCapability_WriteEnabledTrueProbeFailure(t *testing.T) {
	tr := &writeCapTransport{writeSupported: false}
	a := New(Config{WriteEnabled: true}, tr)
	got := a.WriteCapability()
	want := "read-only (worker missing POST /api/memory/save)"
	if got != want {
		t.Errorf("WriteCapability: got %q, want %q", got, want)
	}
}

// TestWriteCapability_NilTransport verifies nil transport → endpoint-missing string.
func TestWriteCapability_NilTransport(t *testing.T) {
	a := New(Config{WriteEnabled: true}, nil)
	got := a.WriteCapability()
	want := "read-only (worker missing POST /api/memory/save)"
	if got != want {
		t.Errorf("WriteCapability: got %q, want %q", got, want)
	}
}
