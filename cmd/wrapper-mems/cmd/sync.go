package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// syncCmd is a placeholder registered so the root command compiles with the
// full subcommand tree. The body is implemented in PR-D (T-50..T-57).
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronise canonical store with all configured providers (PR-D)",
	Long: `sync runs pull then push against all enabled providers. Subcommands:
  sync pull    pull from providers into canonical store
  sync push    push canonical store to providers
  sync resolve resolve a conflict by selecting a duplicate

Full implementation is in PR-D. This stub exits 0 with a not-implemented notice.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Fprintln(cmd.OutOrStdout(), "sync: not yet implemented (PR-D)")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
