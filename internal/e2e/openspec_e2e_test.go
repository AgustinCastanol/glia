package e2e_test

// End-to-end tests for the openspec read-only source (PRD-11, Task 8).
// These tests build the real glia binary (via TestMain in e2e_test.go) and
// run it against a temporary store with a synthetic openspec fixture tree.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildOpenspecFixture creates a synthetic openspec/ tree under dir and
// returns its absolute path. The tree mirrors the layout described in PRD-11:
//
//	openspec/
//	  changes/
//	    my-feature/
//	      proposal.md
//	      design.md
//	      tasks.md
//	      specs/
//	        req.md
//	    archive/
//	      old-feature/
//	        proposal.md
//	  specs/
//	    auth/
//	      spec.md
func buildOpenspecFixture(t *testing.T, dir string) string {
	t.Helper()
	base := filepath.Join(dir, "openspec")
	files := map[string]string{
		"changes/my-feature/proposal.md":       "# My Feature Proposal\nThis is the proposal.",
		"changes/my-feature/design.md":          "# My Feature Design\nThis is the design.",
		"changes/my-feature/tasks.md":           "# My Feature Tasks\nThis is the task list.",
		"changes/my-feature/specs/req.md":       "# Requirements\nFunctional requirements.",
		"changes/archive/old-feature/proposal.md": "# Old Feature Proposal\nArchived content.",
		"specs/auth/spec.md":                    "# Auth Spec\nAuth domain specification.",
	}
	for rel, content := range files {
		full := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return base
}

// writeOpenspecConfig writes a .glia/config.yaml that enables the openspec
// source and disables all live providers (so sync doesn't need network).
func writeOpenspecConfig(t *testing.T, dir string) {
	t.Helper()
	storeDir := filepath.Join(dir, ".glia")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir storeDir: %v", err)
	}
	cfg := `schema_version: 1
project: e2e-openspec
providers:
  engram:
    enabled: false
  claude-mem:
    enabled: false
sources:
  openspec:
    enabled: true
    path: openspec
`
	if err := os.WriteFile(filepath.Join(storeDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

// TestE2E_OpenspecSync verifies that `glia sync` ingests spec_artifact records
// from an openspec fixture tree when sources.openspec.enabled = true (PRD-11).
func TestE2E_OpenspecSync(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping openspec sync test in -short mode")
	}

	dir := t.TempDir()

	// Build fixture tree before init so the config can reference it.
	buildOpenspecFixture(t, dir)

	// Init the store (sets up schema.json and memory.jsonl).
	r := runCLI(t, dir, "init", "--project", "e2e-openspec", "--providers", "engram")
	if r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%s", r.exitCode, r.stderr)
	}

	// Overwrite config to enable openspec and disable live providers.
	writeOpenspecConfig(t, dir)

	// Run sync — should ingest openspec artifacts.
	r = runCLI(t, dir, "sync")
	// Exit code 1 is acceptable: engram/claude-mem are disabled but if any
	// provider health check runs and fails it exits 1. What matters is that
	// memory.jsonl contains spec_artifact records.
	if r.exitCode > 1 {
		t.Fatalf("sync: exit=%d stderr=%s stdout=%s", r.exitCode, r.stderr, r.stdout)
	}

	// Read memory.jsonl and verify spec_artifact records were written.
	memPath := filepath.Join(dir, ".glia", "memory.jsonl")
	data, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatalf("read memory.jsonl: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	specArtifactCount := 0
	var typeSeen = map[string]bool{}
	archivedSeen := false

	for _, line := range lines {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse memory.jsonl line: %v\nline: %s", err, line)
		}
		if rec["kind"] != "spec_artifact" {
			continue
		}
		specArtifactCount++

		if tp, ok := rec["type"].(string); ok {
			typeSeen[tp] = true
		}

		// Check for archived tag.
		if tags, ok := rec["tags"].([]any); ok {
			for _, tag := range tags {
				if tag == "archived" {
					archivedSeen = true
				}
			}
		}
	}

	if specArtifactCount == 0 {
		t.Fatalf("expected spec_artifact records in memory.jsonl after sync; got 0\nfull store:\n%s", string(data))
	}

	// Expect all four types to appear from the fixture.
	for _, wantType := range []string{"proposal", "design", "tasks", "spec"} {
		if !typeSeen[wantType] {
			t.Errorf("expected spec_artifact with type=%q in store; types seen: %v", wantType, typeSeen)
		}
	}

	// At least one archived artifact must have been tagged.
	if !archivedSeen {
		t.Error("expected at least one spec_artifact with tag 'archived' from changes/archive/")
	}

	// Verify status --json includes sources block.
	r = runCLI(t, dir, "status", "--json")
	var payload map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("status --json not valid JSON: %v\nraw: %s", err, r.stdout)
	}
	sources, ok := payload["sources"]
	if !ok {
		t.Error("status --json must include 'sources' key")
	} else {
		srcs, ok := sources.([]any)
		if !ok || len(srcs) == 0 {
			t.Errorf("status --json 'sources' must be non-empty array; got: %v", sources)
		}
	}
}

// TestE2E_OpenspecDisabled verifies that when sources.openspec.enabled = false
// (the default), no spec_artifact records appear in the store after sync.
func TestE2E_OpenspecDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping openspec disabled test in -short mode")
	}

	dir := t.TempDir()

	// Build a fixture tree even though openspec is disabled.
	buildOpenspecFixture(t, dir)

	// Init with engram only (openspec disabled by default).
	r := runCLI(t, dir, "init", "--project", "e2e-openspec-off", "--providers", "engram")
	if r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%s", r.exitCode, r.stderr)
	}

	// Sync — openspec adapter should not be wired.
	r = runCLI(t, dir, "sync")
	if r.exitCode > 1 {
		t.Fatalf("sync: exit=%d stderr=%s", r.exitCode, r.stderr)
	}

	// memory.jsonl should have no spec_artifact records.
	memPath := filepath.Join(dir, ".glia", "memory.jsonl")
	data, err := os.ReadFile(memPath)
	if err != nil {
		// An empty store may not have the file yet — that's fine.
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["kind"] == "spec_artifact" {
			t.Errorf("expected no spec_artifact when openspec disabled; found record: %s", line)
		}
	}
}
