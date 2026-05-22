// Package sync implements the sync engine that bidirectionally mirrors
// canonical store records with registered providers.
package sync

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds the runtime configuration loaded from config.yaml.
// Unknown YAML keys are silently ignored.
type Config struct {
	// MirrorEngram controls whether the engram CLI is shelled out before pull
	// and after push (D11). Default: false.
	MirrorEngram bool `yaml:"mirror_engram"`

	// MirrorTimeoutSeconds is the per-invocation deadline for the engram shell-out.
	// Default: 30.
	MirrorTimeoutSeconds int `yaml:"mirror_timeout_seconds"`

	// Providers is the ordered list of enabled provider names. When empty,
	// all registered adapters are used.
	Providers []string `yaml:"providers"`
}

// Default returns the baseline Config used when config.yaml is absent or
// written by `wrapper-mems init`.
func Default() Config {
	return Config{
		MirrorEngram:         false,
		MirrorTimeoutSeconds: 30,
		Providers:            []string{"engram", "claude-mem"},
	}
}

// Load reads config.yaml at path and merges it over Default().
// If the file does not exist, Default() is returned without error.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("sync: load config %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("sync: parse config %s: %w", path, err)
	}

	return cfg, nil
}
