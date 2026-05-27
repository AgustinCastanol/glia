// Package config provides deterministic runtime configuration for glia.
// It implements a four-layer merge: defaults → project file → user file → env vars.
// Adapters receive their sub-config by value at construction time; this package
// is never imported by adapter packages (no import cycle — see design ADR-D3).
package config

// Config is the top-level runtime configuration struct. Its shape matches
// PRD-5 §4.2 exactly. yaml tags govern both reading (via gopkg.in/yaml.v3) and
// writing (init command marshals Default() with detected values applied).
type Config struct {
	SchemaVersion int             `yaml:"schema_version"`
	Project       string          `yaml:"project"`
	Providers     ProvidersConfig `yaml:"providers"`
	Sync          SyncConfig      `yaml:"sync"`
	Privacy       PrivacyConfig   `yaml:"privacy"`
	// Identity is a user-config-only section. If present in a project config
	// file it is silently ignored (enforced by the merge layer, not validated here).
	Identity IdentityConfig `yaml:"identity"`
}

// ProvidersConfig holds configuration for each known provider.
type ProvidersConfig struct {
	Engram    EngramProviderConfig    `yaml:"engram"`
	ClaudeMem ClaudeMemProviderConfig `yaml:"claude-mem"`
}

// EngramProviderConfig holds engram-specific options.
type EngramProviderConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Transport   string `yaml:"transport"`      // "cli" | "http"
	CLIPath     string `yaml:"cli_path"`
	HTTPBaseURL string `yaml:"http_base_url"`
}

// ClaudeMemProviderConfig holds claude-mem-specific options.
type ClaudeMemProviderConfig struct {
	Enabled            bool              `yaml:"enabled"`
	Transport          string            `yaml:"transport"`          // "http" only in v1
	HTTPBaseURL        string            `yaml:"http_base_url"`
	WorkerPIDPath      string            `yaml:"worker_pid_path"`
	ProjectPathMapping map[string]string `yaml:"project_path_mapping"`
	// WriteEnabled controls whether glia will push canonical records back to the
	// claude-mem worker via POST /api/memory/save. A pointer distinguishes
	// "absent from config" (nil) from "explicitly set to false". Load fills nil
	// to true so the effective value is always non-nil after Load returns.
	// REQ-CMW-04.
	WriteEnabled *bool `yaml:"write_enabled,omitempty"`
}

// SyncConfig holds sync-engine options. The extra fields are carry-overs from
// internal/sync.Config so engine semantics remain unchanged in PR-A (the
// toEngineConfig shim translates between the two representations).
type SyncConfig struct {
	MirrorEngram  bool   `yaml:"mirror_engram"`
	DefaultAction string `yaml:"default_action"` // "full" | "pull" | "push"
	AutoCommit    bool   `yaml:"auto_commit"`

	// MirrorTimeoutSeconds and Providers are carry-overs for back-compat with
	// internal/sync.Config. If Providers is empty the engine derives the list
	// from Providers.*.Enabled.
	MirrorTimeoutSeconds int      `yaml:"mirror_timeout_seconds,omitempty"`
	Providers            []string `yaml:"providers,omitempty"`
}

// PrivacyConfig holds privacy-filter options.
type PrivacyConfig struct {
	// ExcludedSessionIDs is the list of session IDs whose observations are
	// filtered out by the claude-mem adapter's ListNative implementation
	// (REQ-PRV-01). The deepest config layer REPLACES the slice entirely
	// (see design ADR-D2 — slice rule).
	ExcludedSessionIDs []string `yaml:"excluded_session_ids"`
}

// IdentityConfig holds identity options. Only honoured in the user config layer.
type IdentityConfig struct {
	// Author overrides the default hostname:user attribution for origin.author.
	Author string `yaml:"author"`
}

// Default returns the canonical default Config used as the bottom layer of
// every merge. Callers must not mutate the returned value.
func Default() *Config {
	writeEnabled := true
	return &Config{
		SchemaVersion: 1,
		Providers: ProvidersConfig{
			Engram: EngramProviderConfig{
				Enabled:     true,
				Transport:   "cli",
				CLIPath:     "engram",
				HTTPBaseURL: "http://localhost:7437",
			},
			ClaudeMem: ClaudeMemProviderConfig{
				Enabled:            true,
				Transport:          "http",
				HTTPBaseURL:        "http://localhost:37701",
				WorkerPIDPath:      "~/.claude-mem/worker.pid",
				ProjectPathMapping: map[string]string{},
				WriteEnabled:       &writeEnabled,
			},
		},
		Sync: SyncConfig{
			MirrorEngram:         false,
			DefaultAction:        "full",
			AutoCommit:           false,
			MirrorTimeoutSeconds: 5,
		},
		Privacy: PrivacyConfig{
			ExcludedSessionIDs: []string{},
		},
	}
}
