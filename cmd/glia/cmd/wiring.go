package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/adapter/claudemem"
	"github.com/agustincastanol/glia/internal/adapter/engram"
	"github.com/agustincastanol/glia/internal/config"
	"github.com/agustincastanol/glia/internal/identity"
	"github.com/agustincastanol/glia/internal/source/openspec"
)

// buildAdapters constructs configured adapters from a loaded *config.Config.
// Only enabled providers are included in the returned map. Disabled providers
// are silently omitted — their absence is not an error.
//
// cliProject is the value of the --project flag (empty string if not set).
// It is passed to config.ResolveProject as the highest-priority input so that
// per-provider and global project overrides honour PRD-6 precedence:
//   CLI flag > providers.<x>.project > Config.Project
//
// projectDir is the absolute path to the project root directory. It is used
// to resolve repo-relative source paths (e.g. sources.openspec.path).
//
// On any construction failure (e.g. unknown transport type) the error is
// returned immediately so callers (status, sync, doctor) can surface it.
//
// The returned map uses canonical provider names as keys ("engram",
// "claude-mem", "openspec"). A nil map (all providers disabled) is valid
// and not an error.
func buildAdapters(cfg *config.Config, cliProject string, projectDir string) (map[string]adapter.Adapter, error) {
	author := identity.Resolve(cfg.Identity.Author)
	out := make(map[string]adapter.Adapter)

	if cfg.Providers.Engram.Enabled {
		// For both transport modes we always provide the HTTP transport so that
		// ListNative (Export path) works alongside CLI operations. The Commander
		// is resolved from Config.CLIPath inside engram.New() when cfg.Commander
		// is nil. Unknown transport values are rejected early.
		switch cfg.Providers.Engram.Transport {
		case "cli", "", "http":
			// valid — handled below
		default:
			return nil, fmt.Errorf("buildAdapters: unknown engram transport %q", cfg.Providers.Engram.Transport)
		}
		tr := engram.NewHTTPTransport(cfg.Providers.Engram.HTTPBaseURL)
		out["engram"] = engram.New(engram.Config{
			Enabled:     true,
			Transport:   cfg.Providers.Engram.Transport,
			CLIPath:     cfg.Providers.Engram.CLIPath,
			HTTPBaseURL: cfg.Providers.Engram.HTTPBaseURL,
			Author:      author,
			Project:     config.ResolveProject(cliProject, cfg.Providers.Engram.Project, cfg.Project),
		}, tr)
	}

	if cfg.Providers.ClaudeMem.Enabled {
		// Dereference the *bool pointer. config.Load always fills nil → true, so
		// WriteEnabled is never nil here. Defensive fallback: nil → false (safe).
		writeEnabled := cfg.Providers.ClaudeMem.WriteEnabled != nil && *cfg.Providers.ClaudeMem.WriteEnabled
		tr := claudemem.NewHTTPTransport(cfg.Providers.ClaudeMem.HTTPBaseURL)
		out["claude-mem"] = claudemem.New(claudemem.Config{
			Enabled:            true,
			HTTPBaseURL:        cfg.Providers.ClaudeMem.HTTPBaseURL,
			WorkerPIDPath:      cfg.Providers.ClaudeMem.WorkerPIDPath,
			ProjectPathMapping: cfg.Providers.ClaudeMem.ProjectPathMapping,
			ExcludedSessionIDs: cfg.Privacy.ExcludedSessionIDs,
			Author:             author,
			WriteEnabled:       writeEnabled,
			Project:            config.ResolveProject(cliProject, cfg.Providers.ClaudeMem.Project, cfg.Project),
		}, tr)
	}

	// openspec read-only source (PRD-11). Registered only when enabled; absent
	// when disabled so the sync engine and status command are unaffected.
	if cfg.Sources.Openspec.Enabled {
		openspecDir := cfg.Sources.Openspec.Path
		if !filepath.IsAbs(openspecDir) && projectDir != "" {
			openspecDir = filepath.Join(projectDir, openspecDir)
		}
		out["openspec"] = openspec.New(openspec.Config{Dir: openspecDir})
	}

	return out, nil
}

// resolveEngineProject returns the effective project for the sync engine by
// applying PRD-6 precedence: CLI flag > global Config.Project.
// Returns an error if the resolved project is empty — an empty project would
// cause all adapters to return zero records, which is never intentional.
func resolveEngineProject(cliFlag, globalProject string) (string, error) {
	if cliFlag != "" {
		return cliFlag, nil
	}
	if globalProject != "" {
		return globalProject, nil
	}
	return "", fmt.Errorf("project is required: set it in config.yaml (project:) or pass --project <name>")
}
