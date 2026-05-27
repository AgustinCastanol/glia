package claudemem

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// Phase 1 — SaveMemory and WriteSupported transport tests (REQ-CMW-01, REQ-CMW-02)
// ---------------------------------------------------------------------------

// TestSaveMemory_Success verifies a well-formed POST /api/memory/save returns
// SaveMemoryResponse with Success==true and a positive ID.
func TestSaveMemory_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/memory/save" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"id":42}`))
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	resp, err := tr.SaveMemory(context.Background(), SaveMemoryRequest{Text: "hello world"})
	if err != nil {
		t.Fatalf("SaveMemory error: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected Success==true, got false")
	}
	if resp.ID != 42 {
		t.Errorf("expected ID==42, got %d", resp.ID)
	}
}

// TestSaveMemory_HTTP400 verifies that a 400 response surfaces as a non-nil error.
func TestSaveMemory_HTTP400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"text is required"}`))
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	_, err := tr.SaveMemory(context.Background(), SaveMemoryRequest{Text: ""})
	if err == nil {
		t.Fatal("expected error for HTTP 400, got nil")
	}
}

// TestSaveMemory_NetworkError verifies that a connection error surfaces as a non-nil error.
func TestSaveMemory_NetworkError(t *testing.T) {
	// Use a closed server so the connection is refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	tr := NewHTTPTransport(srv.URL)
	_, err := tr.SaveMemory(context.Background(), SaveMemoryRequest{Text: "hello"})
	if err == nil {
		t.Fatal("expected error for network failure, got nil")
	}
}

// TestWriteSupported_Probe400ReturnsTrue verifies HTTP 400 → writeSupported=true.
func TestWriteSupported_Probe400ReturnsTrue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	if !tr.WriteSupported(context.Background()) {
		t.Error("expected WriteSupported==true when probe returns 400")
	}
}

// TestWriteSupported_Probe404ReturnsFalse verifies HTTP 404 → writeSupported=false.
func TestWriteSupported_Probe404ReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	if tr.WriteSupported(context.Background()) {
		t.Error("expected WriteSupported==false when probe returns 404")
	}
}

// TestWriteSupported_OtherStatusReturnsFalse verifies unexpected status → false.
func TestWriteSupported_OtherStatusReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	if tr.WriteSupported(context.Background()) {
		t.Error("expected WriteSupported==false for unexpected status")
	}
}

// TestWriteSupported_CachedAfterFirstCall verifies that the probe runs only once
// and the result is cached (the server tracks call count).
func TestWriteSupported_CachedAfterFirstCall(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusBadRequest) // 400 → true
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	// Call twice — probe must only fire once.
	first := tr.WriteSupported(context.Background())
	second := tr.WriteSupported(context.Background())

	if !first || !second {
		t.Errorf("expected both calls to return true, got first=%v second=%v", first, second)
	}
	if callCount != 1 {
		t.Errorf("expected probe to run exactly once, ran %d times", callCount)
	}
}
