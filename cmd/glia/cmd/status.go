package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/config"
	"github.com/agustincastanol/glia/internal/store"
	enginesync "github.com/agustincastanol/glia/internal/sync"
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

	// WriteCapability maps provider name to its write capability string (REQ-CMW-09).
	WriteCapability map[string]string `json:"write_capability"`

	// EffectiveProject maps provider name to the project name that adapter will
	// use for list/write operations (PRD-6). Empty string means no project
	// override was configured; the value is informational only.
	EffectiveProject map[string]string `json:"effective_project"`

	// Conflicts is the current unresolved conflict list.
	Conflicts []enginesync.ConflictSummary `json:"conflicts"`

	// SyncState maps provider name to its last push/pull watermarks.
	SyncState map[string]store.ProviderSyncState `json:"sync_state"`

	// LineCount, FileSizeBytes, and SchemaVersion come from store.Stats().
	LineCount     int   `json:"line_count"`
	FileSizeBytes int64 `json:"file_size_bytes"`
	SchemaVersion int   `json:"schema_version"`

	// Sources lists read-only static file sources and their health/freshness
	// (PRD-11 §10). Always a JSON array (never null).
	Sources []enginesync.SourceStatus `json:"sources"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show provider health and last-sync timestamps",
	Long: `status checks Health() on each configured provider and reports whether it is
reachable. Exit codes (REQ-SE-52):
  0  all providers healthy
  1  at least one provider degraded
  2  glia itself misconfigured (no store, corrupt index, schema mismatch)`,
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
		// errNoStore / corrupt store → exit 2 (misconfigured glia).
		os.Exit(2)
	}
	defer s.Close()

	if err := enforceMinVersion(filepath.Join(dir, ".glia")); err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		os.Exit(1)
	}

	// Load full config once for both adapter wiring and engine config (T-3.7: removes
	// the redundant legacy enginesync.Load() call — WARNING-03 from PR-A verify-report).
	loadedConfig, lcErr := config.Load(dir, "")
	if lcErr != nil {
		// Non-fatal: fall back to nil adapters so status still shows store state.
		fmt.Fprintln(os.Stderr, "status: load config:", lcErr)
	}

	var adapters map[string]adapter.Adapter
	cfg := enginesync.Default()
	if lcErr == nil {
		cfg = toEngineConfig(loadedConfig)
		var aErr error
		adapters, aErr = buildAdapters(loadedConfig, rootFlags.project, dir)
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

	// Collect per-adapter effective project via optional EffectiveProject() accessor.
	// projecter is a subset interface satisfied by engram.EngramAdapter and
	// claudemem.ClaudeMemAdapter without changing the Adapter contract (PRD-6).
	type projecter interface{ EffectiveProject() string }
	effectiveProjects := make(map[string]string, len(adapters))
	for name, a := range adapters {
		if ep, ok := a.(projecter); ok {
			effectiveProjects[name] = ep.EffectiveProject()
		}
	}

	if statusFlags.json {
		runStatusJSON(cmd, s, report, effectiveProjects)
		return
	}

	w := cmd.OutOrStdout()
	tw := newTabWriter(w)

	// Header.
	fmt.Fprintln(tw, "PROVIDER\tSTATUS\tWRITE_CAPABILITY\tEFFECTIVE_PROJECT\tLAST_PUSHED\tLAST_PULLED")

	anyDegraded := false
	for _, name := range sortedKeys(report.ProviderHealth) {
		healthErr := report.ProviderHealth[name]
		status := "healthy"
		if healthErr != nil {
			status = "degraded: " + healthErr.Error()
			anyDegraded = true
		}
		writeCap := "-"
		if report.ProviderWriteCapability != nil {
			if cap, ok := report.ProviderWriteCapability[name]; ok && cap != "" {
				writeCap = cap
			}
		}
		effProj := dashIfEmpty(effectiveProjects[name])
		ps, _ := s.SyncState(name)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			name, status, writeCap, effProj,
			dashIfEmpty(ps.LastPushedAt),
			dashIfEmpty(ps.LastPulledAt),
		)
	}
	tw.Flush()

	if statusFlags.conflicts {
		printConflictsTable(w, report)
	}

	// Print sources block when any read-only sources are configured (PRD-11 §10).
	printSourcesTable(w, report)

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
// open store. effectiveProjects maps provider name to its EffectiveProject()
// value (PRD-6); pass nil when adapters are unavailable and the map will be
// populated with empty strings per provider.
// Separated from runStatusJSON so tests can call it without os.Exit.
func buildStatusJSON(s *store.Store, report *enginesync.StatusReport, effectiveProjects map[string]string) (statusJSON, error) {
	health := make(map[string]string, len(report.ProviderHealth))
	for name, herr := range report.ProviderHealth {
		if herr != nil {
			health[name] = herr.Error()
		} else {
			health[name] = ""
		}
	}

	// REQ-CMW-09: write capability per provider.
	writeCap := make(map[string]string, len(report.ProviderHealth))
	for name := range report.ProviderHealth {
		if report.ProviderWriteCapability != nil {
			writeCap[name] = report.ProviderWriteCapability[name]
		}
	}

	// PRD-6: effective project per provider.
	effProj := make(map[string]string, len(report.ProviderHealth))
	for name := range report.ProviderHealth {
		if effectiveProjects != nil {
			effProj[name] = effectiveProjects[name]
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

	// Sources: always a non-nil slice so JSON renders [] not null (PRD-11 §10).
	sources := report.Sources
	if sources == nil {
		sources = []enginesync.SourceStatus{}
	}

	return statusJSON{
		ProviderHealth:   health,
		WriteCapability:  writeCap,
		EffectiveProject: effProj,
		Conflicts:        conflicts,
		SyncState:        syncState,
		LineCount:        st.LineCount,
		FileSizeBytes:    st.FileSizeBytes,
		SchemaVersion:    st.SchemaVersion,
		Sources:          sources,
	}, nil
}

// printSourcesTable renders the sources block as a tab-separated table (PRD-11 §10).
// It is a no-op when report.Sources is empty so the sources block is invisible
// when openspec is disabled. Sources are intentionally separate from the providers
// table: they have no LAST_PUSHED / LAST_PULLED watermarks.
func printSourcesTable(w io.Writer, report *enginesync.StatusReport) {
	if len(report.Sources) == 0 {
		return
	}
	tw := newTabWriter(w)
	fmt.Fprintln(tw, "SOURCE\tSTATUS\tWRITE_CAPABILITY\tARTIFACTS\tNEWEST")
	for _, src := range report.Sources {
		status := "healthy"
		if !src.Healthy {
			status = "degraded: " + src.HealthError
		}
		newest := dashIfEmpty(src.NewestArtifact)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			src.Name, status, src.WriteCapability, src.ArtifactCount, newest)
	}
	tw.Flush()
}

// runStatusJSON emits the machine-readable JSON status object and exits with
// the same exit codes as the human-readable path (D9, REQ-TUI-08).
//
//   0  all providers healthy
//   1  at least one provider degraded
//
// Conflicts are surfaced in the JSON body (conflicts array) and do not affect
// the exit code. The TUI reads conflicts directly from the JSON body.
func runStatusJSON(cmd *cobra.Command, s *store.Store, report *enginesync.StatusReport, effectiveProjects map[string]string) {
	out, err := buildStatusJSON(s, report, effectiveProjects)
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
