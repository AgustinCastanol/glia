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

	adapters, err := buildAdapters(cfg, "")
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

	adapters, err := buildAdapters(cfg, "")
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

	adapters, err := buildAdapters(cfg, "")
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

	_, err := buildAdapters(cfg, "")
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

	adapters, err := buildAdapters(cfg, "")
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

// TestBuildAdapters_PerProviderProjectPropagated verifies that when a provider
// has a per-provider project override, the resolved project is passed to the
// adapter (not the global config.Project). PRD-6 wiring requirement.
func TestBuildAdapters_PerProviderProjectPropagated(t *testing.T) {
	t.Run("engram per-provider project used when set", func(t *testing.T) {
		cfg := config.Default()
		cfg.Project = "global"
		cfg.Providers.Engram.Enabled = true
		cfg.Providers.Engram.Project = "eng-specific"
		cfg.Providers.ClaudeMem.Enabled = false

		adapters, err := buildAdapters(cfg, "")
		if err != nil {
			t.Fatalf("buildAdapters: %v", err)
		}
		a, ok := adapters["engram"]
		if !ok {
			t.Fatal("expected engram adapter")
		}
		// The engram adapter must use "eng-specific" as its effective project.
		// We verify via the exported EffectiveProject() accessor.
		type projecter interface{ EffectiveProject() string }
		ep, ok := a.(projecter)
		if !ok {
			t.Fatalf("engram adapter does not implement EffectiveProject(); got %T", a)
		}
		if got := ep.EffectiveProject(); got != "eng-specific" {
			t.Errorf("EffectiveProject()=%q, want %q", got, "eng-specific")
		}
	})

	t.Run("global project used when no per-provider override", func(t *testing.T) {
		cfg := config.Default()
		cfg.Project = "global"
		cfg.Providers.Engram.Enabled = true
		cfg.Providers.Engram.Project = "" // no override
		cfg.Providers.ClaudeMem.Enabled = false

		adapters, err := buildAdapters(cfg, "")
		if err != nil {
			t.Fatalf("buildAdapters: %v", err)
		}
		a, ok := adapters["engram"]
		if !ok {
			t.Fatal("expected engram adapter")
		}
		type projecter interface{ EffectiveProject() string }
		ep, ok := a.(projecter)
		if !ok {
			t.Fatalf("engram adapter does not implement EffectiveProject(); got %T", a)
		}
		if got := ep.EffectiveProject(); got != "global" {
			t.Errorf("EffectiveProject()=%q, want %q", got, "global")
		}
	})

	t.Run("claudemem per-provider project used when set", func(t *testing.T) {
		cfg := config.Default()
		cfg.Project = "global"
		cfg.Providers.Engram.Enabled = false
		cfg.Providers.ClaudeMem.Enabled = true
		cfg.Providers.ClaudeMem.Project = "cm-specific"

		adapters, err := buildAdapters(cfg, "")
		if err != nil {
			t.Fatalf("buildAdapters: %v", err)
		}
		a, ok := adapters["claude-mem"]
		if !ok {
			t.Fatal("expected claude-mem adapter")
		}
		type projecter interface{ EffectiveProject() string }
		ep, ok := a.(projecter)
		if !ok {
			t.Fatalf("claude-mem adapter does not implement EffectiveProject(); got %T", a)
		}
		if got := ep.EffectiveProject(); got != "cm-specific" {
			t.Errorf("EffectiveProject()=%q, want %q", got, "cm-specific")
		}
	})
}

// TestResolveEngineProject verifies PRD-6 precedence and the empty-project guard
// (Phase 2: validation moved to buildSyncEngine via resolveEngineProject).
func TestResolveEngineProject(t *testing.T) {
	tests := []struct {
		name        string
		cliFlag     string
		global      string
		wantProject string
		wantErr     bool
	}{
		{
			name:        "CLI flag wins over global",
			cliFlag:     "cli-project",
			global:      "global-project",
			wantProject: "cli-project",
		},
		{
			name:        "global used when CLI flag empty",
			cliFlag:     "",
			global:      "global-project",
			wantProject: "global-project",
		},
		{
			name:    "error when both empty",
			cliFlag: "",
			global:  "",
			wantErr: true,
		},
		{
			name:        "CLI flag used when global empty",
			cliFlag:     "cli-only",
			global:      "",
			wantProject: "cli-only",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveEngineProject(tc.cliFlag, tc.global)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveEngineProject(%q, %q): expected error, got nil", tc.cliFlag, tc.global)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveEngineProject(%q, %q): unexpected error: %v", tc.cliFlag, tc.global, err)
			}
			if got != tc.wantProject {
				t.Errorf("resolveEngineProject(%q, %q) = %q, want %q", tc.cliFlag, tc.global, got, tc.wantProject)
			}
		})
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

		adapters, err := buildAdapters(cfg, "")
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

		adapters, err := buildAdapters(cfg, "")
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
