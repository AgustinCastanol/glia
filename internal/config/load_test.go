package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProjectConfig writes config.yaml into <dir>/.wrapper-mems/.
func writeProjectConfig(t *testing.T, dir string, content string) {
	t.Helper()
	storeDir := filepath.Join(dir, storeDirName)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir storeDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, configFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

func writeUserConfig(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir user config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
}

// TestLoad_DefaultsOnly loads from a minimal project config (only required fields).
// REQ-CFG-01, REQ-CFG-02: defaults fill in all unspecified fields.
func TestLoad_DefaultsOnly(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\nproject: testproject\n")

	cfg, err := Load(dir, "/nonexistent/user.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.Project != "testproject" {
		t.Errorf("Project: got %q, want %q", cfg.Project, "testproject")
	}
	// Defaults preserved.
	if cfg.Providers.Engram.CLIPath != "engram" {
		t.Errorf("Engram.CLIPath: got %q, want %q", cfg.Providers.Engram.CLIPath, "engram")
	}
	if cfg.Providers.Engram.Transport != "cli" {
		t.Errorf("Engram.Transport: got %q, want %q", cfg.Providers.Engram.Transport, "cli")
	}
	if cfg.Sync.DefaultAction != "full" {
		t.Errorf("Sync.DefaultAction: got %q, want %q", cfg.Sync.DefaultAction, "full")
	}
}

// TestLoad_ProjectOverridesDefault verifies project config overrides defaults.
// REQ-CFG-02: Three-layer merge scenario.
func TestLoad_ProjectOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `schema_version: 1
project: my-app
providers:
  engram:
    cli_path: engram-dev
sync:
  mirror_engram: false
`)

	cfg, err := Load(dir, "/nonexistent/user.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.Providers.Engram.CLIPath != "engram-dev" {
		t.Errorf("CLIPath: got %q, want %q", cfg.Providers.Engram.CLIPath, "engram-dev")
	}
}

// TestLoad_UserOverridesProject verifies user config overrides project config.
// REQ-CFG-01: Scenario "User config overrides project config field".
func TestLoad_UserOverridesProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\nproject: proj\nsync:\n  mirror_engram: false\n")
	userPath := filepath.Join(t.TempDir(), "user.yaml")
	writeUserConfig(t, userPath, "sync:\n  mirror_engram: true\n")

	cfg, err := Load(dir, userPath)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if !cfg.Sync.MirrorEngram {
		t.Error("expected MirrorEngram=true from user config, got false")
	}
}

// TestLoad_MissingUserConfig verifies a missing user config is silently skipped.
// REQ-CFG-05: Missing user config scenario.
func TestLoad_MissingUserConfig(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\nproject: proj\n")

	cfg, err := Load(dir, "/definitely/does/not/exist.yaml")
	if err != nil {
		t.Fatalf("expected nil error for missing user config, got: %v", err)
	}
	if cfg.Project != "proj" {
		t.Errorf("Project: got %q, want %q", cfg.Project, "proj")
	}
}

// TestLoad_MalformedProjectConfig verifies malformed YAML returns an error with the path.
// REQ-CFG-05: Malformed project config scenario.
func TestLoad_MalformedProjectConfig(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "key: [unclosed\n")

	_, err := Load(dir, "/nonexistent/user.yaml")
	if err == nil {
		t.Fatal("expected error for malformed project config, got nil")
	}
	if !containsRaw(err.Error(), configFileName) {
		t.Errorf("error message %q should contain config filename %q", err.Error(), configFileName)
	}
}

// TestLoad_MissingProject verifies that missing project field returns error.
// REQ-CFG validation.
func TestLoad_MissingProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\n")

	_, err := Load(dir, "/nonexistent/user.yaml")
	if err == nil {
		t.Fatal("expected error for missing project, got nil")
	}
}

// TestLoad_InvalidSchemaVersion verifies schema_version != 1 returns error.
func TestLoad_InvalidSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 2\nproject: proj\n")

	_, err := Load(dir, "/nonexistent/user.yaml")
	if err == nil {
		t.Fatal("expected error for schema_version=2, got nil")
	}
}

// TestLoad_EnvOverlayProject verifies WRAPPER_MEMS_PROJECT overrides the project name.
// REQ-CFG-03: WRAPPER_MEMS_PROJECT overrides project name scenario.
func TestLoad_EnvOverlayProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\nproject: my-app\n")
	t.Setenv("WRAPPER_MEMS_PROJECT", "ci-run")

	cfg, err := Load(dir, "/nonexistent/user.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Project != "ci-run" {
		t.Errorf("Project: got %q, want %q (env override)", cfg.Project, "ci-run")
	}
}

// TestLoad_EnvOverlayEngramBin verifies WRAPPER_MEMS_ENGRAM_BIN override.
// REQ-CFG-03: env var binding table.
func TestLoad_EnvOverlayEngramBin(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\nproject: proj\n")
	t.Setenv("WRAPPER_MEMS_ENGRAM_BIN", "/opt/engram")

	cfg, err := Load(dir, "/nonexistent/user.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Providers.Engram.CLIPath != "/opt/engram" {
		t.Errorf("CLIPath: got %q, want %q", cfg.Providers.Engram.CLIPath, "/opt/engram")
	}
}

// TestLoad_EnvOverlayCMBaseURL verifies WRAPPER_MEMS_CM_BASE_URL override.
func TestLoad_EnvOverlayCMBaseURL(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\nproject: proj\n")
	t.Setenv("WRAPPER_MEMS_CM_BASE_URL", "http://localhost:9999")

	cfg, err := Load(dir, "/nonexistent/user.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Providers.ClaudeMem.HTTPBaseURL != "http://localhost:9999" {
		t.Errorf("CM HTTPBaseURL: got %q, want %q", cfg.Providers.ClaudeMem.HTTPBaseURL, "http://localhost:9999")
	}
}

// TestLoad_SliceReplace verifies that excluded_session_ids is REPLACED (not appended)
// by the user config layer. REQ-PRV-02, design ADR-D2.
func TestLoad_SliceReplace(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `schema_version: 1
project: proj
privacy:
  excluded_session_ids:
    - sess_abc
    - sess_def
`)
	userPath := filepath.Join(t.TempDir(), "user.yaml")
	writeUserConfig(t, userPath, "privacy:\n  excluded_session_ids:\n    - sess_xyz\n")

	cfg, err := Load(dir, userPath)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	// User layer REPLACES — only sess_xyz must be present.
	if len(cfg.Privacy.ExcludedSessionIDs) != 1 || cfg.Privacy.ExcludedSessionIDs[0] != "sess_xyz" {
		t.Errorf("ExcludedSessionIDs: got %v, want [sess_xyz] (slice replace, not append)", cfg.Privacy.ExcludedSessionIDs)
	}
}

// TestLoad_MapMerge verifies that project_path_mapping merges key-by-key.
// Design: map rule — user adds/overrides individual keys, cannot blank-out project entries.
func TestLoad_MapMerge(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `schema_version: 1
project: proj
providers:
  claude-mem:
    project_path_mapping:
      /work/proj: proj
`)
	userPath := filepath.Join(t.TempDir(), "user.yaml")
	writeUserConfig(t, userPath, `providers:
  claude-mem:
    project_path_mapping:
      /home/alice/proj: proj
`)

	cfg, err := Load(dir, userPath)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Providers.ClaudeMem.ProjectPathMapping["/work/proj"] != "proj" {
		t.Error("expected project key /work/proj to remain after user merge")
	}
	if cfg.Providers.ClaudeMem.ProjectPathMapping["/home/alice/proj"] != "proj" {
		t.Error("expected user key /home/alice/proj to be added by merge")
	}
}

// TestLoad_ThreeLayerMerge verifies project → user → env precedence for a single field.
// REQ-CFG-02: Three-layer merge scenario.
func TestLoad_ThreeLayerMerge(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\nproject: proj\nproviders:\n  engram:\n    cli_path: engram-dev\n")
	// User config does NOT set cli_path. env also not set.
	cfg, err := Load(dir, "/nonexistent/user.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Providers.Engram.CLIPath != "engram-dev" {
		t.Errorf("CLIPath: got %q, want %q (project value preserved)", cfg.Providers.Engram.CLIPath, "engram-dev")
	}
}

// TestLoad_IdentityFromUserConfig verifies identity.author is read from user config.
// REQ-IDN-01.
func TestLoad_IdentityFromUserConfig(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "schema_version: 1\nproject: proj\n")
	userPath := filepath.Join(t.TempDir(), "user.yaml")
	writeUserConfig(t, userPath, "identity:\n  author: alice\n")

	cfg, err := Load(dir, userPath)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Identity.Author != "alice" {
		t.Errorf("Identity.Author: got %q, want %q", cfg.Identity.Author, "alice")
	}
}
