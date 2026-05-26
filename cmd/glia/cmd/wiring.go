package cmd

import (
	"fmt"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/adapter/claudemem"
	"github.com/agustincastanol/glia/internal/adapter/engram"
	"github.com/agustincastanol/glia/internal/config"
	"github.com/agustincastanol/glia/internal/identity"
)

// buildAdapters constructs configured adapters from a loaded *config.Config.
// Only enabled providers are included in the returned map. Disabled providers
// are silently omitted — their absence is not an error.
//
// On any construction failure (e.g. unknown transport type) the error is
// returned immediately so callers (status, sync, doctor) can surface it.
//
// The returned map uses canonical provider names as keys ("engram",
// "claude-mem"). A nil map (all providers disabled) is valid and not an error.
func buildAdapters(cfg *config.Config) (map[string]adapter.Adapter, error) {
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
		}, tr)
	}

	if cfg.Providers.ClaudeMem.Enabled {
		tr := claudemem.NewHTTPTransport(cfg.Providers.ClaudeMem.HTTPBaseURL)
		out["claude-mem"] = claudemem.New(claudemem.Config{
			Enabled:            true,
			HTTPBaseURL:        cfg.Providers.ClaudeMem.HTTPBaseURL,
			WorkerPIDPath:      cfg.Providers.ClaudeMem.WorkerPIDPath,
			ProjectPathMapping: cfg.Providers.ClaudeMem.ProjectPathMapping,
			ExcludedSessionIDs: cfg.Privacy.ExcludedSessionIDs,
			Author:             author,
		}, tr)
	}

	return out, nil
}
