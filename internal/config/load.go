package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// storeDirName is the canonical subdirectory used by glia stores.
// Kept as a package-level constant here so that internal/config does not import
// internal/store (which would create a back-edge: store → config is not allowed).
const storeDirName = ".glia"

// configFileName is the name of the project config file inside storeDirName.
const configFileName = "config.yaml"

// Load resolves the effective runtime Config via a six-step process:
//  1. Start from Default().
//  2. Read and merge the project config file (<projectDir>/.glia/config.yaml).
//  3. Read and merge the user config file (userConfigPath). A missing user file is
//     silently skipped (REQ-CFG-05). An existing-but-malformed file is an error.
//  4. Apply env var overrides via envOverlay (REQ-CFG-03).
//  5. Expand "~/" prefixes in path fields (expandPaths).
//  6. Validate the resulting config.
//
// projectDir must point to the project root directory (not the .glia/
// subdirectory). userConfigPath may be empty, in which case the XDG default is
// used (~/.config/glia/config.yaml).
func Load(projectDir, userConfigPath string) (*Config, error) {
	cfg := Default()

	// Step 2: project config — required if the project is initialized.
	projPath := filepath.Join(projectDir, storeDirName, configFileName)
	if err := mergeFile(cfg, projPath, layerProject); err != nil {
		return nil, err
	}

	// Step 3: user config — missing is OK, malformed is an error.
	if userConfigPath == "" {
		userConfigPath = defaultUserConfigPath()
	}
	if err := mergeFile(cfg, userConfigPath, layerUser); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		// Silently skip missing user config (REQ-CFG-05).
	}

	// Step 4: env overlay.
	envOverlay(cfg, os.Getenv)

	// Step 4b: default-fill pointer fields that remain nil after all merges.
	// WriteEnabled defaults to true when absent from every config layer (REQ-CMW-04).
	if cfg.Providers.ClaudeMem.WriteEnabled == nil {
		t := true
		cfg.Providers.ClaudeMem.WriteEnabled = &t
	}

	// Step 5: path expansion.
	expandPaths(cfg)

	// Step 6: validate.
	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// layer distinguishes which config layer is being merged so that layer-specific
// rules (e.g. ignoring Identity in project files) can be enforced.
type layer int

const (
	layerProject layer = iota
	layerUser
)

// defaultUserConfigPath returns ~/.config/glia/config.yaml, honouring
// $XDG_CONFIG_HOME when set.
func defaultUserConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "glia", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "glia", "config.yaml")
}

// mergeFile reads the YAML file at path, decodes it into a map[string]any
// to discover which keys are explicitly present, then applies only those keys
// to dst (design ADR-D7: two-pass decode to distinguish "key absent" from
// "key present with zero value").
//
// Returns fs.ErrNotExist (unwrapped) when path does not exist, so callers can
// decide whether to treat absence as an error.
func mergeFile(dst *Config, path string, l layer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fs.ErrNotExist
		}
		return fmt.Errorf("%s: %w", path, err)
	}

	// First pass: decode into an intermediate struct to get typed values.
	var layer Config
	if err := yaml.Unmarshal(data, &layer); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	// Second pass: decode into a map to know which keys are present.
	var rawMap map[string]any
	if err := yaml.Unmarshal(data, &rawMap); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	mergeInto(dst, &layer, rawMap, l)
	return nil
}

// mergeInto copies fields from src into dst when the corresponding key appears
// in rawMap (the two-pass decode result).
//
// Slice rule (ADR-D2): excluded_session_ids and providers slices REPLACE the
// destination slice entirely when the key is present in rawMap.
//
// Map rule: project_path_mapping merges key-by-key (user adds/overrides
// individual entries; cannot blank-out project entries without explicitly setting
// each key to "").
//
// Identity is silently ignored in the project layer.
func mergeInto(dst *Config, src *Config, rawMap map[string]any, l layer) {
	if _, ok := rawMap["schema_version"]; ok {
		dst.SchemaVersion = src.SchemaVersion
	}
	if _, ok := rawMap["project"]; ok {
		dst.Project = src.Project
	}

	// Providers sub-map.
	if rawProviders, ok := rawMap["providers"]; ok {
		if pm, ok := rawProviders.(map[string]any); ok {
			if rawEngram, ok := pm["engram"]; ok {
				if em, ok := rawEngram.(map[string]any); ok {
					mergeEngram(&dst.Providers.Engram, &src.Providers.Engram, em)
				}
			}
			if rawCM, ok := pm["claude-mem"]; ok {
				if cm, ok := rawCM.(map[string]any); ok {
					mergeClaudeMem(&dst.Providers.ClaudeMem, &src.Providers.ClaudeMem, cm)
				}
			}
		}
	}

	// Sync sub-map.
	if rawSync, ok := rawMap["sync"]; ok {
		if sm, ok := rawSync.(map[string]any); ok {
			mergeSync(&dst.Sync, &src.Sync, sm)
		}
	}

	// Privacy sub-map.
	if rawPrivacy, ok := rawMap["privacy"]; ok {
		if pm, ok := rawPrivacy.(map[string]any); ok {
			// Slice REPLACE rule (ADR-D2).
			if _, ok := pm["excluded_session_ids"]; ok {
				dst.Privacy.ExcludedSessionIDs = src.Privacy.ExcludedSessionIDs
			}
		}
	}

	// Identity: only honoured in the user layer.
	if l == layerUser {
		if rawIdentity, ok := rawMap["identity"]; ok {
			if im, ok := rawIdentity.(map[string]any); ok {
				if _, ok := im["author"]; ok {
					dst.Identity.Author = src.Identity.Author
				}
			}
		}
	}
}

func mergeEngram(dst *EngramProviderConfig, src *EngramProviderConfig, m map[string]any) {
	if _, ok := m["enabled"]; ok {
		dst.Enabled = src.Enabled
	}
	if _, ok := m["transport"]; ok {
		dst.Transport = src.Transport
	}
	if _, ok := m["cli_path"]; ok {
		dst.CLIPath = src.CLIPath
	}
	if _, ok := m["http_base_url"]; ok {
		dst.HTTPBaseURL = src.HTTPBaseURL
	}
}

func mergeClaudeMem(dst *ClaudeMemProviderConfig, src *ClaudeMemProviderConfig, m map[string]any) {
	if _, ok := m["enabled"]; ok {
		dst.Enabled = src.Enabled
	}
	if _, ok := m["transport"]; ok {
		dst.Transport = src.Transport
	}
	if _, ok := m["http_base_url"]; ok {
		dst.HTTPBaseURL = src.HTTPBaseURL
	}
	if _, ok := m["worker_pid_path"]; ok {
		dst.WorkerPIDPath = src.WorkerPIDPath
	}
	// Map rule: key-by-key merge.
	if _, ok := m["project_path_mapping"]; ok {
		if dst.ProjectPathMapping == nil {
			dst.ProjectPathMapping = make(map[string]string)
		}
		for k, v := range src.ProjectPathMapping {
			dst.ProjectPathMapping[k] = v
		}
	}
	// Pointer field: only overwrite when key is explicitly present in the YAML.
	// This preserves the default (true) when the key is absent.
	if _, ok := m["write_enabled"]; ok {
		dst.WriteEnabled = src.WriteEnabled
	}
}

func mergeSync(dst *SyncConfig, src *SyncConfig, m map[string]any) {
	if _, ok := m["mirror_engram"]; ok {
		dst.MirrorEngram = src.MirrorEngram
	}
	if _, ok := m["default_action"]; ok {
		dst.DefaultAction = src.DefaultAction
	}
	if _, ok := m["auto_commit"]; ok {
		dst.AutoCommit = src.AutoCommit
	}
	if _, ok := m["mirror_timeout_seconds"]; ok {
		dst.MirrorTimeoutSeconds = src.MirrorTimeoutSeconds
	}
	// Slice REPLACE rule (ADR-D2).
	if _, ok := m["providers"]; ok {
		dst.Providers = src.Providers
	}
}

// expandPaths expands "~/" prefixes in known path fields.
func expandPaths(c *Config) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	c.Providers.Engram.CLIPath = expandHome(c.Providers.Engram.CLIPath, home)
	c.Providers.Engram.HTTPBaseURL = expandHome(c.Providers.Engram.HTTPBaseURL, home)
	c.Providers.ClaudeMem.HTTPBaseURL = expandHome(c.Providers.ClaudeMem.HTTPBaseURL, home)
	c.Providers.ClaudeMem.WorkerPIDPath = expandHome(c.Providers.ClaudeMem.WorkerPIDPath, home)
	for k, v := range c.Providers.ClaudeMem.ProjectPathMapping {
		c.Providers.ClaudeMem.ProjectPathMapping[k] = expandHome(v, home)
	}
}

// expandHome expands a leading "~/" or bare "~" using home.
func expandHome(s, home string) string {
	if s == "~" {
		return home
	}
	if strings.HasPrefix(s, "~/") {
		return filepath.Join(home, s[2:])
	}
	return s
}

// validate checks that the merged config satisfies all hard invariants.
func validate(c *Config) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("config: unsupported schema_version %d (want 1)", c.SchemaVersion)
	}
	if c.Project == "" {
		return fmt.Errorf("config: project is required — run `glia init` or set WRAPPER_MEMS_PROJECT")
	}
	if c.Providers.Engram.Transport != "cli" && c.Providers.Engram.Transport != "http" {
		return fmt.Errorf("config: providers.engram.transport must be \"cli\" or \"http\", got %q", c.Providers.Engram.Transport)
	}
	if c.Providers.ClaudeMem.Transport != "http" {
		return fmt.Errorf("config: providers.claude-mem.transport must be \"http\" in v1, got %q", c.Providers.ClaudeMem.Transport)
	}
	if c.Sync.DefaultAction != "full" && c.Sync.DefaultAction != "pull" && c.Sync.DefaultAction != "push" {
		return fmt.Errorf("config: sync.default_action must be \"full\", \"pull\", or \"push\", got %q", c.Sync.DefaultAction)
	}
	if c.Sync.MirrorTimeoutSeconds < 0 {
		return fmt.Errorf("config: sync.mirror_timeout_seconds must be >= 0, got %d", c.Sync.MirrorTimeoutSeconds)
	}
	return nil
}
