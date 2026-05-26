package config

import (
	"testing"
)

// TestEnvOverlay_Project verifies WRAPPER_MEMS_PROJECT binding.
func TestEnvOverlay_Project(t *testing.T) {
	cfg := Default()
	cfg.Project = "original"
	envOverlay(cfg, func(name string) string {
		if name == "WRAPPER_MEMS_PROJECT" {
			return "env-project"
		}
		return ""
	})
	if cfg.Project != "env-project" {
		t.Errorf("Project: got %q, want %q", cfg.Project, "env-project")
	}
}

// TestEnvOverlay_EngramBin verifies WRAPPER_MEMS_ENGRAM_BIN binding.
func TestEnvOverlay_EngramBin(t *testing.T) {
	cfg := Default()
	envOverlay(cfg, func(name string) string {
		if name == "WRAPPER_MEMS_ENGRAM_BIN" {
			return "/usr/local/bin/engram"
		}
		return ""
	})
	if cfg.Providers.Engram.CLIPath != "/usr/local/bin/engram" {
		t.Errorf("CLIPath: got %q, want %q", cfg.Providers.Engram.CLIPath, "/usr/local/bin/engram")
	}
}

// TestEnvOverlay_CMBaseURL verifies WRAPPER_MEMS_CM_BASE_URL binding.
func TestEnvOverlay_CMBaseURL(t *testing.T) {
	cfg := Default()
	envOverlay(cfg, func(name string) string {
		if name == "WRAPPER_MEMS_CM_BASE_URL" {
			return "http://remote:9000"
		}
		return ""
	})
	if cfg.Providers.ClaudeMem.HTTPBaseURL != "http://remote:9000" {
		t.Errorf("CM HTTPBaseURL: got %q, want %q", cfg.Providers.ClaudeMem.HTTPBaseURL, "http://remote:9000")
	}
}

// TestEnvOverlay_EmptyVarNoEffect verifies empty env vars do not override values.
func TestEnvOverlay_EmptyVarNoEffect(t *testing.T) {
	cfg := Default()
	cfg.Project = "kept"
	envOverlay(cfg, func(string) string { return "" })
	if cfg.Project != "kept" {
		t.Errorf("Project should not be overridden by empty env var: got %q", cfg.Project)
	}
}
