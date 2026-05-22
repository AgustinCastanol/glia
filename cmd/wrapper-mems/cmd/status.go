package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	enginesync "github.com/agustincastanol/wrapper-mems/internal/sync"
)

var statusFlags struct {
	conflicts bool
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show provider health and last-sync timestamps",
	Long: `status checks Health() on each configured provider and reports whether it is
reachable. Exit codes (REQ-SE-52):
  0  all providers healthy
  1  at least one provider degraded
  2  wrapper-mems itself misconfigured (no store, corrupt index, schema mismatch)`,
	Args: cobra.NoArgs,
	Run:  runStatus,
}

func init() {
	statusCmd.Flags().BoolVar(&statusFlags.conflicts, "conflicts", false,
		"also print the current conflict table")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, _ []string) {
	dir, err := projectDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "status: resolve dir:", err)
		os.Exit(2)
	}

	s, err := requireStore(dir)
	if err != nil {
		// requireStore already wrote to stderr.
		// errNoStore / corrupt store → exit 2 (misconfigured wrapper-mems).
		os.Exit(2)
	}
	defer s.Close()

	sp := storePath(dir)
	cfg, cfgErr := enginesync.Load(configPath(sp))
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "status: load config:", cfgErr)
	}

	// Build a zero-adapter engine — Status only needs Health checks and the
	// conflict list from the store. Adapters are wired in PR-D.
	engine := enginesync.New(s, nil, cfg, enginesync.Options{}, os.Stderr)

	report, err := engine.Status(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		os.Exit(1)
	}

	w := cmd.OutOrStdout()
	tw := newTabWriter(w)

	// Header.
	fmt.Fprintln(tw, "PROVIDER\tSTATUS\tLAST_PUSHED\tLAST_PULLED")

	anyDegraded := false
	for _, name := range sortedKeys(report.ProviderHealth) {
		healthErr := report.ProviderHealth[name]
		status := "healthy"
		if healthErr != nil {
			status = "degraded: " + healthErr.Error()
			anyDegraded = true
		}
		ps, _ := s.SyncState(name)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			name, status,
			dashIfEmpty(ps.LastPushedAt),
			dashIfEmpty(ps.LastPulledAt),
		)
	}
	tw.Flush()

	if statusFlags.conflicts {
		printConflictsTable(w, report)
	}

	if anyDegraded {
		os.Exit(1)
	}
}

// printConflictsTable renders the conflict list as a table (REQ-SE-36).
func printConflictsTable(w io.Writer, report *enginesync.StatusReport) {
	if len(report.Conflicts) == 0 {
		fmt.Fprintln(w, "no conflicts")
		return
	}
	tw := newTabWriter(w)
	fmt.Fprintln(tw, "#\tCANONICAL_ID\tREVISION\tDUP_COUNT\tDETECTED_AT")
	for i, c := range report.Conflicts {
		fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%s\n",
			i+1, c.CanonicalID, c.Revision, c.DupCount, c.DetectedAt)
	}
	tw.Flush()
}

// sortedKeys returns the keys of m in sorted order.
func sortedKeys(m map[string]error) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStringSlice(keys)
	return keys
}

// sortStringSlice sorts ss in-place (insertion sort, no stdlib import needed).
func sortStringSlice(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

// dashIfEmpty returns s if non-empty, otherwise "-".
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
