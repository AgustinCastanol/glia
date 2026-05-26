package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
	"github.com/agustincastanol/wrapper-mems/internal/config"
	"github.com/agustincastanol/wrapper-mems/internal/store"
	enginesync "github.com/agustincastanol/wrapper-mems/internal/sync"
)

var statusFlags struct {
	conflicts bool
	json      bool
}

// statusJSON is the machine-readable form emitted by --json.
// It embeds all StatusReport fields plus store.Stats() fields so the TUI
// (and other consumers) can parse a single object without open/locked the store.
type statusJSON struct {
	// ProviderHealth maps provider name to health error string (empty string = healthy).
	ProviderHealth map[string]string `json:"provider_health"`

	// Conflicts is the current unresolved conflict list.
	Conflicts []enginesync.ConflictSummary `json:"conflicts"`

	// SyncState maps provider name to its last push/pull watermarks.
	SyncState map[string]store.ProviderSyncState `json:"sync_state"`

	// LineCount, FileSizeBytes, and SchemaVersion come from store.Stats().
	LineCount     int   `json:"line_count"`
	FileSizeBytes int64 `json:"file_size_bytes"`
	SchemaVersion int   `json:"schema_version"`
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
	statusCmd.Flags().BoolVar(&statusFlags.json, "json", false,
		"emit machine-readable JSON (provider_health, conflicts, sync_state, store stats)")
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

	// Load full config for adapter wiring (REQ-DOC-01 D3: real adapters required
	// so provider_health reflects actual reachability, not always nil).
	loadedConfig, lcErr := config.Load(dir, "")
	if lcErr != nil {
		// Non-fatal: fall back to nil adapters so status still shows store state.
		fmt.Fprintln(os.Stderr, "status: load config (adapters):", lcErr)
	}

	var adapters map[string]adapter.Adapter
	if lcErr == nil {
		var aErr error
		adapters, aErr = buildAdapters(loadedConfig)
		if aErr != nil {
			fmt.Fprintln(os.Stderr, "status: build adapters:", aErr)
		}
	}

	engine := enginesync.New(s, adapters, cfg, enginesync.Options{}, os.Stderr)

	report, err := engine.Status(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		os.Exit(1)
	}

	if statusFlags.json {
		runStatusJSON(cmd, s, report)
		return
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

// buildStatusJSON constructs the statusJSON payload from a StatusReport and an
// open store. Separated from runStatusJSON so tests can call it without os.Exit.
func buildStatusJSON(s *store.Store, report *enginesync.StatusReport) (statusJSON, error) {
	health := make(map[string]string, len(report.ProviderHealth))
	for name, herr := range report.ProviderHealth {
		if herr != nil {
			health[name] = herr.Error()
		} else {
			health[name] = ""
		}
	}

	syncState := make(map[string]store.ProviderSyncState)
	for name := range report.ProviderHealth {
		ps, _ := s.SyncState(name)
		syncState[name] = ps
	}

	st, err := store.Stats(s.RootDir())
	if err != nil {
		return statusJSON{}, fmt.Errorf("store.Stats: %w", err)
	}

	conflicts := report.Conflicts
	if conflicts == nil {
		conflicts = []enginesync.ConflictSummary{}
	}

	return statusJSON{
		ProviderHealth: health,
		Conflicts:      conflicts,
		SyncState:      syncState,
		LineCount:      st.LineCount,
		FileSizeBytes:  st.FileSizeBytes,
		SchemaVersion:  st.SchemaVersion,
	}, nil
}

// runStatusJSON emits the machine-readable JSON status object and exits with
// the same exit codes as the human-readable path (D9, REQ-TUI-08).
//
//   0  all providers healthy
//   1  at least one provider degraded
//
// Conflicts are surfaced in the JSON body (conflicts array) and do not affect
// the exit code. The TUI reads conflicts directly from the JSON body.
func runStatusJSON(cmd *cobra.Command, s *store.Store, report *enginesync.StatusReport) {
	out, err := buildStatusJSON(s, report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status --json:", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "status --json: encode:", err)
		os.Exit(1)
	}

	// Mirror exit codes of the human-readable path: exit 1 if any provider is
	// degraded, exit 0 otherwise.
	for _, v := range out.ProviderHealth {
		if v != "" {
			os.Exit(1)
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
