// Package e2e — bidirectional write path tests (Phase 10, REQ-CMW-04, REQ-CMW-E2E).
//
// These tests verify the full canonical→claude-mem write path by spinning up a
// fake claude-mem worker via httptest.Server (runs in-process), writing its URL
// into the project config, seeding a canonical record, and execing the binary.
//
// Server contract (mirrors write_integration_test.go probe branching):
//
//	GET  /health             → 200 OK
//	POST /api/memory/save    → if body text=="" → 400 (WriteSupported probe)
//	                         → else             → 200 {"success":true,"id":1}
//
// Covered scenarios:
//
//	10.1 Bidirectional write: engram-origin canonical record is pushed to claude-mem.
//	10.2 Read path unaffected: claude-mem → canonical pull still works alongside write path.
//	10.3 Wiring bug: WriteEnabled=false (zero value) short-circuits — fixed by wiring.go patch.
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake claude-mem worker helpers
// ---------------------------------------------------------------------------

// saveMemoryRequest mirrors claudemem.SaveMemoryRequest for JSON decode.
type saveMemoryRequest struct {
	Text string `json:"text"`
}

// fakeCMWorker spins up an httptest.Server that implements the minimal
// claude-mem worker surface needed for the write path:
//
//	GET  /health             → 200
//	GET  /api/observations   → 200 {"items":[],"hasMore":false} (read path)
//	POST /api/memory/save    → probe (empty text) → 400; real save → 200+JSON
//
// saveCalls is incremented for every non-probe POST to /api/memory/save.
// The server is registered for cleanup via t.Cleanup.
func fakeCMWorker(t *testing.T, saveCalls *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Read path: return empty page so pull doesn't produce canonical records
	// that would interfere with write-path assertions.
	mux.HandleFunc("/api/observations", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[],"hasMore":false,"offset":0,"limit":100}`))
	})

	mux.HandleFunc("/api/memory/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req saveMemoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Text == "" {
			// WriteSupported probe — return 400 to signal endpoint exists.
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Real save.
		id := saveCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "id": id})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// Config / store helpers
// ---------------------------------------------------------------------------

// writeClaudeMemConfig overwrites .glia/config.yaml in dir to point claude-mem
// at cmBaseURL with write_enabled: true and disables engram (so the only
// active provider is the fake claude-mem worker).
func writeClaudeMemConfig(t *testing.T, dir, cmBaseURL, project string) {
	t.Helper()
	yaml := fmt.Sprintf(`schema_version: 1
project: %s
providers:
  engram:
    enabled: false
    transport: cli
    cli_path: engram
    http_base_url: http://localhost:7437
  claude-mem:
    enabled: true
    transport: http
    http_base_url: %s
    worker_pid_path: /tmp/cm-e2e.pid
    write_enabled: true
sync:
  mirror_engram: false
  default_action: full
  mirror_timeout_seconds: 5
`, project, cmBaseURL)
	path := filepath.Join(dir, ".glia", "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("writeClaudeMemConfig: %v", err)
	}
}

// seedCanonicalRecord appends a single engram-origin canonical record to
// memory.jsonl so the pull loop has something to write to claude-mem.
// The record uses a fixed canonical_id and a timestamp far enough in the
// past that watermark checks won't filter it.
func seedCanonicalRecord(t *testing.T, dir string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	rec := map[string]any{
		"canonical_id":   "01HZ000000000000000000TEST1",
		"line_ulid":      "01HZ000000000000000000TEST1",
		"schema_version": 1,
		"kind":           "session_summary",
		"revision":       1,
		"supersedes":     "",
		"deleted":        false,
		"title":          "E2E test seed record",
		"content":        "This record was seeded by the e2e test to exercise the write path.",
		"content_format": "markdown",
		"origin": map[string]any{
			"provider":    "engram",
			"provider_id": "test-seed-001",
			"author":      "e2e-test",
			"session_id":  "",
		},
		"created_at": now,
		"updated_at": now,
		"tags":       []string{},
		"topic_key":  "",
		"type":       "manual",
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("seedCanonicalRecord: marshal: %v", err)
	}
	path := filepath.Join(dir, ".glia", "memory.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("seedCanonicalRecord: open: %v", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s\n", data); err != nil {
		t.Fatalf("seedCanonicalRecord: write: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 10.1 — Bidirectional write: glia sync pushes engram record to claude-mem
// ---------------------------------------------------------------------------

// TestE2E_ClaudeMemWritePath_EngramRecordPushed verifies REQ-CMW-04 end-to-end:
// a canonical record with origin.provider=="engram" is pushed to the fake
// claude-mem worker via POST /api/memory/save when write_enabled=true.
//
// This test also exercises the wiring fix: before the fix, buildAdapters set
// WriteEnabled=false (zero value), so WriteNative short-circuited and the
// fake server never received a POST.
func TestE2E_ClaudeMemWritePath_EngramRecordPushed(t *testing.T) {
	var saveCalls atomic.Int64
	srv := fakeCMWorker(t, &saveCalls)

	dir := initFreshStore(t)
	writeClaudeMemConfig(t, dir, srv.URL, "e2e")
	seedCanonicalRecord(t, dir)

	// Run glia sync (full = push-from-provider + pull-to-provider).
	// The pull direction (canonical→claude-mem) calls WriteNative for the
	// seeded engram-origin record.
	r := runCLI(t, dir, "sync")
	if r.exitCode != 0 {
		t.Fatalf("glia sync exit=%d\nstdout=%s\nstderr=%s", r.exitCode, r.stdout, r.stderr)
	}

	got := saveCalls.Load()
	if got < 1 {
		t.Errorf("expected at least 1 POST /api/memory/save call, got %d\n"+
			"stdout=%s\nstderr=%s", got, r.stdout, r.stderr)
	}
}

// ---------------------------------------------------------------------------
// Test 10.2 — Read path unaffected: claude-mem → canonical pull still works
// ---------------------------------------------------------------------------

// TestE2E_ClaudeMemReadPath_StillWorks verifies that the write path additions
// do not break the existing read path (claude-mem → canonical store).
//
// The fake worker returns an empty observations page, so we assert glia sync
// exits 0 and does not panic — the read path is exercised even with no records.
func TestE2E_ClaudeMemReadPath_StillWorks(t *testing.T) {
	var saveCalls atomic.Int64
	srv := fakeCMWorker(t, &saveCalls)

	dir := initFreshStore(t)
	writeClaudeMemConfig(t, dir, srv.URL, "e2e")
	// No seeded record — read path only.

	r := runCLI(t, dir, "sync")
	// Exit 0 is expected; exit 1 would indicate a hard error.
	// Exit 2 (conflicts) is also acceptable — we only care that it doesn't crash.
	if r.exitCode == 1 {
		t.Fatalf("glia sync hard error exit=1\nstdout=%s\nstderr=%s", r.stdout, r.stderr)
	}
}
