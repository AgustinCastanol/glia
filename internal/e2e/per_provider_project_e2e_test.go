// Package e2e — per-provider project override smoke tests (PRD-6).
//
// Covered scenarios:
//
//	6.1 Per-provider project override flows from config to glia status --json
//	    effective_project field.
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeHealthServer spins up a minimal httptest.Server that responds 200 to
// GET /health (sufficient for the status health check) and 200 to
// GET /api/observations (empty page, so the pull loop has nothing to ingest).
// The server is registered for cleanup via t.Cleanup.
func fakeHealthServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/observations", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"items":[],"hasMore":false}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeConfigWithPerProviderProject writes .glia/config.yaml in dir with:
//   - engram disabled
//   - claude-mem enabled, pointing at cmBaseURL, with providers.claude-mem.project
//     set to cmProject
//   - global project set to globalProject
func writeConfigWithPerProviderProject(t *testing.T, dir, cmBaseURL, globalProject, cmProject string) {
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
    worker_pid_path: /tmp/cm-e2e-ppp.pid
    write_enabled: false
    project: %s
sync:
  mirror_engram: false
  default_action: full
  mirror_timeout_seconds: 5
`, globalProject, cmBaseURL, cmProject)
	path := filepath.Join(dir, ".glia", "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("writeConfigWithPerProviderProject: %v", err)
	}
}

// statusJSONOutput is a minimal struct for decoding the fields we care about
// from `glia status --json`.
type statusJSONOutput struct {
	EffectiveProject map[string]string `json:"effective_project"`
	ProviderHealth   map[string]string `json:"provider_health"`
}

// ---------------------------------------------------------------------------
// Test 6.1 — Per-provider project override flows into status --json
// ---------------------------------------------------------------------------

// TestE2E_PerProviderProject_StatusJSON verifies that when
// providers.claude-mem.project is set in config, `glia status --json` reports
// that value in the effective_project["claude-mem"] field (PRD-6).
func TestE2E_PerProviderProject_StatusJSON(t *testing.T) {
	srv := fakeHealthServer(t)

	dir := initFreshStore(t)
	writeConfigWithPerProviderProject(t, dir, srv.URL, "global-project", "cm-override")

	r := runCLI(t, dir, "status", "--json")
	// Exit 0 (all healthy) or exit 1 (degraded) are both acceptable here;
	// we only assert on the JSON body content.
	if r.exitCode == 2 {
		t.Fatalf("glia status --json misconfigured exit=2\nstdout=%s\nstderr=%s", r.stdout, r.stderr)
	}

	var out statusJSONOutput
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("parse status JSON: %v\nraw: %s", err, r.stdout)
	}

	got, ok := out.EffectiveProject["claude-mem"]
	if !ok {
		t.Fatalf("effective_project map missing 'claude-mem' key; full map: %v", out.EffectiveProject)
	}
	if got != "cm-override" {
		t.Errorf("effective_project[claude-mem] = %q, want %q", got, "cm-override")
	}
}
