package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
	"github.com/agustincastanol/wrapper-mems/internal/adapter/claudemem"
	"github.com/agustincastanol/wrapper-mems/internal/adapter/engram"
	"github.com/agustincastanol/wrapper-mems/internal/store"
	enginesync "github.com/agustincastanol/wrapper-mems/internal/sync"
)

// syncFlags holds flags shared across sync, sync pull, and sync push.
var syncFlags struct {
	dryRun       bool
	providers    []string
	mirrorEngram bool
	noMirror     bool
	commit       bool
	max          int
}

// syncCmd is the parent "sync" command: runs pull then push (REQ-SE-07).
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronise canonical store with all configured providers",
	Long: `sync runs pull then push against all enabled providers in order.

Subcommands:
  sync pull    pull provider records into the canonical store
  sync push    push canonical store records to providers

Exit codes:
  0  all providers succeeded (or no work to do)
  1  hard error (store unavailable, all providers unhealthy)
  2  unresolved conflicts detected after the run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := projectDir()
		if err != nil {
			return err
		}
		s, err := requireStore(dir)
		if err != nil {
			return err
		}
		defer s.Close()

		engine, err := buildSyncEngine(s, dir)
		if err != nil {
			return err
		}

		ctx := cmd.Context()
		report, err := engine.Sync(ctx)
		if err != nil {
			return err
		}

		report.WriteSummary(cmd.OutOrStdout())
		return syncExitErr(s, report)
	},
}

// syncPullCmd implements `sync pull`.
var syncPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull provider records into the canonical store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := projectDir()
		if err != nil {
			return err
		}
		s, err := requireStore(dir)
		if err != nil {
			return err
		}
		defer s.Close()

		engine, err := buildSyncEngine(s, dir)
		if err != nil {
			return err
		}

		report, err := engine.Pull(cmd.Context())
		if err != nil {
			return err
		}

		report.WriteSummary(cmd.OutOrStdout())
		return syncExitErr(s, report)
	},
}

// syncPushCmd implements `sync push`.
var syncPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push canonical store records to providers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := projectDir()
		if err != nil {
			return err
		}
		s, err := requireStore(dir)
		if err != nil {
			return err
		}
		defer s.Close()

		engine, err := buildSyncEngine(s, dir)
		if err != nil {
			return err
		}

		report, err := engine.Push(cmd.Context())
		if err != nil {
			return err
		}

		report.WriteSummary(cmd.OutOrStdout())
		return syncExitErr(s, report)
	},
}

func init() {
	// Attach flags to the parent syncCmd; cobra propagates to subcommands.
	syncCmd.PersistentFlags().BoolVar(&syncFlags.dryRun, "dry-run", false,
		"preview what would be written without making any changes")
	syncCmd.PersistentFlags().StringArrayVar(&syncFlags.providers, "provider", nil,
		"restrict sync to named provider(s) (repeatable)")
	syncCmd.PersistentFlags().BoolVar(&syncFlags.mirrorEngram, "mirror-engram", false,
		"trigger engram sync shell-out regardless of config.yaml setting")
	syncCmd.PersistentFlags().BoolVar(&syncFlags.noMirror, "no-mirror", false,
		"disable mirror-engram even if config.yaml enables it")
	syncCmd.PersistentFlags().BoolVar(&syncFlags.commit, "commit", false,
		"git add .wrapper-mems/ && git commit after a successful sync")
	syncCmd.PersistentFlags().IntVar(&syncFlags.max, "max", 0,
		"cap records processed per provider per run (0 = unlimited)")

	syncCmd.AddCommand(syncPullCmd)
	syncCmd.AddCommand(syncPushCmd)
	rootCmd.AddCommand(syncCmd)
}

// buildSyncEngine wires adapters and constructs the Engine for this run.
func buildSyncEngine(s *store.Store, dir string) (*enginesync.Engine, error) {
	cfg, err := enginesync.Load(configPath(storePath(dir)))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	opts := enginesync.Options{
		DryRun:         syncFlags.dryRun,
		ProviderFilter: syncFlags.providers,
		Max:            syncFlags.max,
		Verbose:        rootFlags.verbose,
		Commit:         syncFlags.commit,
		Project:        rootFlags.project,
	}

	// --mirror-engram flag overrides config; --no-mirror suppresses it.
	if syncFlags.noMirror {
		opts.MirrorEngram = false
		cfg.MirrorEngram = false
	} else if syncFlags.mirrorEngram {
		opts.MirrorEngram = true
	}

	// Build adapter map. In v1, adapters are hard-wired; a registry is future work.
	adapters := map[string]adapter.Adapter{
		"engram":    engram.New(nil, nil),
		"claude-mem": claudemem.New(nil),
	}

	return enginesync.New(s, adapters, cfg, opts, os.Stderr), nil
}

// syncExitErr maps a RunReport to an exit-code sentinel error (D6 / REQ-SE-51).
// Returns nil (exit 0), errConflicts (exit 2), or a wrapped hard error (exit 1).
func syncExitErr(s *store.Store, report *enginesync.RunReport) error {
	if report == nil {
		return nil
	}

	// All providers failed Health → hard error (exit 1).
	if len(report.HardErrors) > 0 && len(report.PerProvider) == 0 {
		return report.HardErrors[0]
	}

	// Conflicts present → exit 2.
	if len(s.Conflicts()) > 0 {
		fmt.Fprintln(os.Stderr,
			"conflicts detected — run `wrapper-mems status --conflicts` and `wrapper-mems sync resolve`")
		return errConflicts
	}

	return nil
}

// requireStoreForSync is requireStore with a sync-specific error hint.
func requireStoreForSync(dir string) (*store.Store, error) {
	s, err := requireStore(dir)
	if err != nil {
		if errors.Is(err, errNoStore) {
			return nil, errNoStore
		}
		return nil, err
	}
	return s, nil
}
