package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExpandHome_TildeSlash verifies "~/" prefix expansion.
func TestExpandHome_TildeSlash(t *testing.T) {
	tmp := t.TempDir()
	got := expandHome("~/foo/bar", tmp)
	want := filepath.Join(tmp, "foo/bar")
	if got != want {
		t.Errorf("expandHome: got %q, want %q", got, want)
	}
}

// TestExpandHome_TildeOnly verifies bare "~" expands to home.
func TestExpandHome_TildeOnly(t *testing.T) {
	tmp := t.TempDir()
	got := expandHome("~", tmp)
	if got != tmp {
		t.Errorf("expandHome(\"~\"): got %q, want %q", got, tmp)
	}
}

// TestExpandHome_NoTilde verifies paths without tilde are unchanged.
func TestExpandHome_NoTilde(t *testing.T) {
	got := expandHome("/absolute/path", "/home/user")
	if got != "/absolute/path" {
		t.Errorf("expandHome: got %q, want %q", got, "/absolute/path")
	}
}

// TestExpandPaths_WorkerPIDPath verifies WorkerPIDPath expands via HOME.
func TestExpandPaths_WorkerPIDPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Force os.UserHomeDir to use our tmp via HOME.
	// On macOS os.UserHomeDir reads $HOME. On Linux too.
	os.Setenv("HOME", tmp)

	cfg := Default()
	cfg.Providers.ClaudeMem.WorkerPIDPath = "~/.claude-mem/worker.pid"
	expandPaths(cfg)

	want := filepath.Join(tmp, ".claude-mem/worker.pid")
	if cfg.Providers.ClaudeMem.WorkerPIDPath != want {
		t.Errorf("WorkerPIDPath: got %q, want %q", cfg.Providers.ClaudeMem.WorkerPIDPath, want)
	}
}

// TestLoad_PathExpansion verifies that Load() expands "~/" in WorkerPIDPath.
func TestLoad_PathExpansion(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	projDir := t.TempDir()
	writeProjectConfig(t, projDir, `schema_version: 1
project: proj
providers:
  claude-mem:
    worker_pid_path: ~/.claude-mem/worker.pid
`)

	cfg, err := Load(projDir, "/nonexistent/user.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	want := filepath.Join(tmp, ".claude-mem/worker.pid")
	if cfg.Providers.ClaudeMem.WorkerPIDPath != want {
		t.Errorf("WorkerPIDPath after Load: got %q, want %q", cfg.Providers.ClaudeMem.WorkerPIDPath, want)
	}
}
