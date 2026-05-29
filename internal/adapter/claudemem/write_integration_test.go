package claudemem

// Phase 9 — httptest.Server-based integration tests for the SaveMemory write path.
//
// These tests exercise ClaudeMemAdapter.WriteNative end-to-end against a real
// net/http/httptest.Server. Unlike the unit tests in claudemem_test.go (which
// inject a fake Transport), these tests use NewHTTPTransport wired to a live
// test server — verifying the full adapter → transport → HTTP path.
//
// Scenarios covered (REQ-CMW-01, REQ-CMW-04):
//   9.1 Success: 200 response → NativeID returned, Success==true.
//   9.2 400 pass-through: server returns 400 → WriteNative surfaces a non-nil error.
//   9.3 Network error: server closed before request → WriteNative returns error, no panic.
//   9.4 Idempotency: WriteNative does NOT deduplicate — two calls produce two HTTP requests.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/agustincastanol/glia/internal/adapter"
)

// saveServer spins up a httptest.Server that handles the two routes the
// integration tests need:
//   GET /health  → 200 OK (so Health() passes and WriteNative gates succeed)
//   POST /api/memory/save → behaviour controlled by the handler argument
//
// Callers must call srv.Close() or use t.Cleanup(srv.Close).
func saveServer(t *testing.T, saveHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/memory/save", saveHandler)
	return httptest.NewServer(mux)
}

// writeEnabledAdapter builds a ClaudeMemAdapter with write_enabled=true wired
// to NewHTTPTransport pointing at baseURL.
func writeEnabledAdapter(baseURL string) *ClaudeMemAdapter {
	return New(Config{WriteEnabled: true}, NewHTTPTransport(baseURL))
}

// ---------------------------------------------------------------------------
// 9.1 — Success: 200 + {"success":true,"id":99} → NativeID "99"
// ---------------------------------------------------------------------------

func TestWriteNative_Integration_200Success(t *testing.T) {
	srv := saveServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify the probe first-call returns 400 so WriteSupported caches true.
		// Real worker does this; we distinguish by body content.
		var req SaveMemoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Text == "" {
			// probe call
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"id":99}`))
	})
	defer srv.Close()

	a := writeEnabledAdapter(srv.URL)
	native := claudeMemRecord{Title: "hello", Narrative: "world"}

	id, err := a.WriteNative(context.Background(), native)
	if err != nil {
		t.Fatalf("WriteNative: unexpected error: %v", err)
	}
	if id != adapter.NativeID("99") {
		t.Errorf("WriteNative: got NativeID %q, want %q", id, "99")
	}
}

// ---------------------------------------------------------------------------
// 9.2 — 400 pass-through: the worker rejects the actual payload with 400
//       (after the probe already returned 400 → WriteSupported=true).
// ---------------------------------------------------------------------------

func TestWriteNative_Integration_400PassThrough(t *testing.T) {
	callCount := 0
	srv := saveServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req SaveMemoryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if callCount == 1 {
			// first call is the WriteSupported probe (empty text)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// All subsequent calls: return 400 (simulates payload rejection).
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad payload"}`))
	})
	defer srv.Close()

	a := writeEnabledAdapter(srv.URL)
	native := claudeMemRecord{Title: "bad", Narrative: "content"}

	_, err := a.WriteNative(context.Background(), native)
	if err == nil {
		t.Fatal("WriteNative: expected error for HTTP 400 response, got nil")
	}
}

// ---------------------------------------------------------------------------
// 9.3 — Network error: server closed before WriteNative fires.
// ---------------------------------------------------------------------------

func TestWriteNative_Integration_NetworkError(t *testing.T) {
	// Start a server just to get a valid URL, then close it immediately.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	a := writeEnabledAdapter(url)
	native := claudeMemRecord{Title: "hello", Narrative: "world"}

	_, err := a.WriteNative(context.Background(), native)
	if err == nil {
		t.Fatal("WriteNative: expected error for network failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// 9.4 — Idempotency: the adapter must NOT suppress duplicate calls.
//       Two WriteNative calls with identical content must produce two
//       POST /api/memory/save requests. Deduplication is the worker's
//       responsibility (REQ-CMW-04, D5).
// ---------------------------------------------------------------------------

func TestWriteNative_Integration_Idempotency(t *testing.T) {
	var saveCalls atomic.Int64

	srv := saveServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req SaveMemoryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if req.Text == "" {
			// probe
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id := saveCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return distinct IDs per call so the caller can also verify them.
		_ = json.NewEncoder(w).Encode(SaveMemoryResponse{Success: true, ID: id})
	})
	defer srv.Close()

	a := writeEnabledAdapter(srv.URL)
	native := claudeMemRecord{Title: "same title", Narrative: "same narrative"}

	id1, err1 := a.WriteNative(context.Background(), native)
	if err1 != nil {
		t.Fatalf("first WriteNative: %v", err1)
	}
	id2, err2 := a.WriteNative(context.Background(), native)
	if err2 != nil {
		t.Fatalf("second WriteNative: %v", err2)
	}

	// Adapter must not suppress the second call.
	if got := saveCalls.Load(); got != 2 {
		t.Errorf("expected 2 POST /api/memory/save calls, got %d", got)
	}
	// IDs must differ (server returned 1 then 2).
	if id1 == id2 {
		t.Errorf("expected distinct NativeIDs per call, got %q twice", id1)
	}
}
