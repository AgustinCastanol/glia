package cmd

import (
	"testing"

	"github.com/agustincastanol/glia/internal/adapter/claudemem"
	"github.com/agustincastanol/glia/internal/config"
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

// TestBuildAdapters_WriteEnabledPropagated verifies that config.WriteEnabled is
// correctly propagated from the *bool config field into the claudemem.Config
// bool field (REQ-CMW-04 wiring fix). Before the fix, WriteEnabled was silently
// dropped and the adapter always defaulted to write_enabled=false.
func TestBuildAdapters_WriteEnabledPropagated(t *testing.T) {
	t.Run("write_enabled=true propagated", func(t *testing.T) {
		cfg := config.Default()
		cfg.Providers.Engram.Enabled = false
		cfg.Providers.ClaudeMem.Enabled = true
		writeEnabled := true
		cfg.Providers.ClaudeMem.WriteEnabled = &writeEnabled

		adapters, err := buildAdapters(cfg)
		if err != nil {
			t.Fatalf("buildAdapters: %v", err)
		}
		a, ok := adapters["claude-mem"].(*claudemem.ClaudeMemAdapter)
		if !ok {
			t.Fatalf("expected *claudemem.ClaudeMemAdapter, got %T", adapters["claude-mem"])
		}
		// WriteCapability() returns "read-only (write_enabled=false)" when WriteEnabled==false.
		// With the fix applied it must NOT return that string.
		if cap := a.WriteCapability(); cap == "read-only (write_enabled=false)" {
			t.Errorf("WriteCapability=%q: WriteEnabled was not propagated (got write_enabled=false)", cap)
		}
	})

	t.Run("write_enabled=false propagated", func(t *testing.T) {
		cfg := config.Default()
		cfg.Providers.Engram.Enabled = false
		cfg.Providers.ClaudeMem.Enabled = true
		writeEnabled := false
		cfg.Providers.ClaudeMem.WriteEnabled = &writeEnabled

		adapters, err := buildAdapters(cfg)
		if err != nil {
			t.Fatalf("buildAdapters: %v", err)
		}
		a, ok := adapters["claude-mem"].(*claudemem.ClaudeMemAdapter)
		if !ok {
			t.Fatalf("expected *claudemem.ClaudeMemAdapter, got %T", adapters["claude-mem"])
		}
		if cap := a.WriteCapability(); cap != "read-only (write_enabled=false)" {
			t.Errorf("WriteCapability=%q: expected read-only when write_enabled=false", cap)
		}
	})
}
