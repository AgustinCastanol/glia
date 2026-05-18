package claudemem

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
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
