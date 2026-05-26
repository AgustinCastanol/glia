package cmd

import (
	"testing"

	"github.com/agustincastanol/wrapper-mems/internal/config"
)

// TestBuildAdapters_EnabledOnlyReturned verifies that only enabled providers
// appear in the output map.
func TestBuildAdapters_EnabledOnlyReturned(t *testing.T) {
	cfg := config.Default()
	cfg.Project = "test-project"
	cfg.Providers.Engram.Enabled = true
	cfg.Providers.ClaudeMem.Enabled = false

	adapters, err := buildAdapters(cfg)
	if err != nil {
		t.Fatalf("buildAdapters: unexpected error: %v", err)
	}
	if _, ok := adapters["engram"]; !ok {
		t.Error("expected engram adapter in map (enabled=true)")
	}
	if _, ok := adapters["claude-mem"]; ok {
		t.Error("expected claude-mem adapter absent (enabled=false)")
	}
}

// TestBuildAdapters_BothEnabled verifies that both providers appear when both
// are enabled.
func TestBuildAdapters_BothEnabled(t *testing.T) {
	cfg := config.Default()
	cfg.Project = "test-project"
	cfg.Providers.Engram.Enabled = true
	cfg.Providers.ClaudeMem.Enabled = true

	adapters, err := buildAdapters(cfg)
	if err != nil {
		t.Fatalf("buildAdapters: unexpected error: %v", err)
	}
	if _, ok := adapters["engram"]; !ok {
		t.Error("expected engram adapter")
	}
	if _, ok := adapters["claude-mem"]; !ok {
		t.Error("expected claude-mem adapter")
	}
}

// TestBuildAdapters_NoneEnabled verifies that an empty map (not nil error) is
// returned when all providers are disabled.
func TestBuildAdapters_NoneEnabled(t *testing.T) {
	cfg := config.Default()
	cfg.Project = "test-project"
	cfg.Providers.Engram.Enabled = false
	cfg.Providers.ClaudeMem.Enabled = false

	adapters, err := buildAdapters(cfg)
	if err != nil {
		t.Fatalf("buildAdapters: unexpected error: %v", err)
	}
	if len(adapters) != 0 {
		t.Errorf("expected empty map, got %d adapters", len(adapters))
	}
}

// TestBuildAdapters_UnknownEngramTransportReturnsError verifies that an
// unrecognised transport value results in an error (not a panic).
func TestBuildAdapters_UnknownEngramTransportReturnsError(t *testing.T) {
	cfg := config.Default()
	cfg.Project = "test-project"
	cfg.Providers.Engram.Enabled = true
	cfg.Providers.Engram.Transport = "grpc" // unknown

	_, err := buildAdapters(cfg)
	if err == nil {
		t.Fatal("expected error for unknown engram transport, got nil")
	}
}

// TestBuildAdapters_EngineAdapterNamesMatch verifies that the returned map keys
// use the canonical provider names expected by the sync engine.
func TestBuildAdapters_EngineAdapterNamesMatch(t *testing.T) {
	cfg := config.Default()
	cfg.Project = "test-project"
	cfg.Providers.Engram.Enabled = true
	cfg.Providers.ClaudeMem.Enabled = true

	adapters, err := buildAdapters(cfg)
	if err != nil {
		t.Fatalf("buildAdapters: %v", err)
	}

	if a, ok := adapters["engram"]; !ok || a.Name() != "engram" {
		t.Errorf("engram adapter Name()=%q, want %q", adapters["engram"].Name(), "engram")
	}
	if a, ok := adapters["claude-mem"]; !ok || a.Name() != "claude-mem" {
		t.Errorf("claude-mem adapter Name()=%q, want %q", adapters["claude-mem"].Name(), "claude-mem")
	}
}
