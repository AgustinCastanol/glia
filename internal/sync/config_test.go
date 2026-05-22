package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.MirrorEngram {
		t.Error("Default() MirrorEngram should be false")
	}
	if cfg.MirrorTimeoutSeconds != 30 {
		t.Errorf("Default() MirrorTimeoutSeconds = %d, want 30", cfg.MirrorTimeoutSeconds)
	}
	if len(cfg.Providers) == 0 {
		t.Error("Default() Providers should be non-empty")
	}
}

func TestLoad_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `mirror_engram: true
mirror_timeout_seconds: 60
providers:
  - engram
  - claude-mem
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.MirrorEngram {
		t.Error("MirrorEngram should be true")
	}
	if cfg.MirrorTimeoutSeconds != 60 {
		t.Errorf("MirrorTimeoutSeconds = %d, want 60", cfg.MirrorTimeoutSeconds)
	}
	if len(cfg.Providers) != 2 || cfg.Providers[0] != "engram" {
		t.Errorf("Providers = %v, want [engram, claude-mem]", cfg.Providers)
	}
}

func TestLoad_MissingFile_ReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() on missing file should not error, got: %v", err)
	}

	def := Default()
	if cfg.MirrorEngram != def.MirrorEngram {
		t.Errorf("MirrorEngram = %v, want %v", cfg.MirrorEngram, def.MirrorEngram)
	}
	if cfg.MirrorTimeoutSeconds != def.MirrorTimeoutSeconds {
		t.Errorf("MirrorTimeoutSeconds = %d, want %d", cfg.MirrorTimeoutSeconds, def.MirrorTimeoutSeconds)
	}
}

func TestLoad_UnknownKeysIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `mirror_engram: true
totally_unknown_key: some_value
another_future_key: 42
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() with unknown keys should not error: %v", err)
	}
	if !cfg.MirrorEngram {
		t.Error("MirrorEngram should be true even with unknown keys present")
	}
}

func TestLoad_PartialOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Only override one field; others should keep defaults.
	content := `mirror_timeout_seconds: 90
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MirrorTimeoutSeconds != 90 {
		t.Errorf("MirrorTimeoutSeconds = %d, want 90", cfg.MirrorTimeoutSeconds)
	}
	// MirrorEngram should still be default (false).
	if cfg.MirrorEngram {
		t.Error("MirrorEngram should still be default false")
	}
}
