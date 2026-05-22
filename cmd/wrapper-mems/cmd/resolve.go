package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	enginesync "github.com/agustincastanol/wrapper-mems/internal/sync"
)

var resolveFlags struct {
	keep int
}

// syncResolveCmd implements `sync resolve <canonical_id> --keep <dup_index>`.
// It resolves a conflict by selecting the duplicate at dup_index (1-based) as
// the authoritative version (REQ-SE-37, REQ-SE-38, REQ-SE-39, D12).
var syncResolveCmd = &cobra.Command{
	Use:   "resolve <canonical_id>",
	Short: "Resolve a sync conflict by choosing which duplicate to keep",
	Long: `resolve appends a new superseding record whose payload is taken from the chosen
duplicate (--keep index, 1-based) and removes the ConflictEntry from the store.
Watermarks are not reset; the operation is a targeted append only (REQ-SE-39).

Exit codes:
  0  conflict resolved successfully
  1  no conflict for canonical_id, invalid --keep index, or store error`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		canonicalID := args[0]

		if resolveFlags.keep < 1 {
			fmt.Fprintln(os.Stderr, "resolve: --keep must be >= 1")
			return fmt.Errorf("--keep must be >= 1")
		}

		dir, err := projectDir()
		if err != nil {
			return err
		}
		s, err := requireStore(dir)
		if err != nil {
			return err
		}
		defer s.Close()

		cfg, err := enginesync.Load(configPath(storePath(dir)))
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		engine := enginesync.New(s, nil, cfg, enginesync.Options{}, os.Stderr)

		if err := engine.Resolve(canonicalID, resolveFlags.keep); err != nil {
			fmt.Fprintln(os.Stderr, "resolve:", err)
			return err
		}

		fmt.Fprintf(cmd.OutOrStdout(), "conflict resolved: %s (kept dup_index=%d)\n",
			canonicalID, resolveFlags.keep)
		return nil
	},
}

func init() {
	syncResolveCmd.Flags().IntVar(&resolveFlags.keep, "keep", 0,
		"1-based index of the duplicate to keep (required)")
	_ = syncResolveCmd.MarkFlagRequired("keep")
	syncCmd.AddCommand(syncResolveCmd)
}
