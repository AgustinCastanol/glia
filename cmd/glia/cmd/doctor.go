package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/config"
	"github.com/agustincastanol/glia/internal/store"
)

// CheckStatus represents the severity of a check result.
type CheckStatus int

const (
	StatusOK   CheckStatus = iota // check passed
	StatusWarn                    // non-blocking issue
	StatusErr                     // error that blocked the check
)

// CheckResult holds the outcome of one doctor check.
type CheckResult struct {
	Name    string
	Status  CheckStatus
	Message string
	// Fixable is true when --fix can repair the issue.
	Fixable bool
	// FixFn is called by --fix when Fixable is true and Status != StatusOK.
	FixFn func() error
}

var doctorFlags struct {
	fix bool
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run health checks on the local store and configured providers",
	Long: `doctor checks the integrity of the canonical store, schema compatibility,
index consistency, provider reachability, .gitignore entries, and stale lock files.

Exit codes (REQ-DOC-02):
  0  all checks healthy
  1  one or more warnings (non-blocking)
  2  one or more errors that blocked checks

Use --fix to automatically repair fixable issues:
  - Add missing .gitignore entries (.glia/index.json, .glia/.lock)
  - Remove .glia/memory.jsonl from .gitignore (D2: it must be committed)
  - Rebuild index.json when corrupt or inconsistent with memory.jsonl
  - Remove a stale .lock file whose PID is no longer alive
  - Truncate memory.jsonl to last complete line (partial-write recovery)

--fix NEVER modifies provider data, removes complete JSONL lines, or runs git commands.`,
	Args: cobra.NoArgs,
	RunE: runDoctor,
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorFlags.fix, "fix", false,
		"apply safe-only automatic repairs (see command description)")
	rootCmd.AddCommand(doctorCmd)
}

// memoryJSONLGitignorePattern matches lines that gitignore memory.jsonl
// (with or without the .glia/ prefix, REQ-DOC-03).
var memoryJSONLGitignorePattern = regexp.MustCompile(`^(\.glia/)?memory\.jsonl\s*$`)

func runDoctor(cmd *cobra.Command, _ []string) error {
	dir, err := projectDir()
	if err != nil {
		return fmt.Errorf("doctor: resolve dir: %w", err)
	}

	sp := storePath(dir)
	giPath := filepath.Join(dir, ".gitignore")
	lockPath := filepath.Join(sp, ".lock")

	// Load config for provider health checks (D3: real adapters required).
	// Non-fatal if config is missing — provider checks become errors instead.
	loadedConfig, _ := config.Load(dir, "")

	// Build adapters for health checks (best-effort; nil if config unavailable).
	var adapters map[string]adapter.Adapter
	if loadedConfig != nil {
		adapters, _ = buildAdapters(loadedConfig, rootFlags.project)
	}

	// Run all checks in order (ADR-D8: data first, providers, git, lock).
	checks := []CheckResult{
		checkCanonicalStore(sp),
		checkSchema(sp),
		checkIndex(sp),
		checkEngram(cmd.Context(), adapters),
		checkClaudeMem(cmd.Context(), adapters),
		checkGitignore(giPath),
		checkStaleLock(lockPath),
	}

	if doctorFlags.fix {
		runFixes(cmd, sp, giPath, lockPath, checks)
		// Re-run checks after fixes so the final output reflects the new state.
		checks = []CheckResult{
			checkCanonicalStore(sp),
			checkSchema(sp),
			checkIndex(sp),
			checkEngram(cmd.Context(), adapters),
			checkClaudeMem(cmd.Context(), adapters),
			checkGitignore(giPath),
			checkStaleLock(lockPath),
		}
	}

	printResults(cmd, checks)

	// Determine exit code (REQ-DOC-02).
	hasWarn, hasErr := false, false
	for _, r := range checks {
		switch r.Status {
		case StatusWarn:
			hasWarn = true
		case StatusErr:
			hasErr = true
		}
	}

	if hasErr {
		os.Exit(2)
	}
	if hasWarn {
		os.Exit(1)
	}
	return nil
}

// checkCanonicalStore verifies memory.jsonl exists and is readable.
func checkCanonicalStore(storeDir string) CheckResult {
	memPath := filepath.Join(storeDir, "memory.jsonl")
	fi, err := os.Stat(memPath)
	if os.IsNotExist(err) {
		return CheckResult{
			Name:    "canonical store",
			Status:  StatusErr,
			Message: "memory.jsonl not found — run `glia init`",
		}
	}
	if err != nil {
		return CheckResult{
			Name:    "canonical store",
			Status:  StatusErr,
			Message: fmt.Sprintf("stat memory.jsonl: %v", err),
		}
	}

	// Use store.Stats to get line count (index-based, lock-free).
	st, statsErr := store.Stats(storeDir)
	if statsErr != nil {
		// Stats fails if index is missing — that's OK, the index check covers it.
		return CheckResult{
			Name:    "canonical store",
			Status:  StatusOK,
			Message: fmt.Sprintf("memory.jsonl exists (%d bytes)", fi.Size()),
		}
	}

	return CheckResult{
		Name:    "canonical store",
		Status:  StatusOK,
		Message: fmt.Sprintf("memory.jsonl exists (%d bytes, %d lines)", fi.Size(), st.LineCount),
	}
}

// checkSchema verifies schema.json exists and GliaMinVersion is
// compatible with the current binary Version.
func checkSchema(storeDir string) CheckResult {
	info, err := store.ReadSchema(storeDir)
	if err != nil {
		return CheckResult{
			Name:    "schema",
			Status:  StatusErr,
			Message: fmt.Sprintf("cannot read schema.json: %v", err),
		}
	}

	if refuseErr := config.Refuse(Version, info.GliaMinVersion); refuseErr != nil {
		return CheckResult{
			Name:    "schema",
			Status:  StatusErr,
			Message: refuseErr.Error(),
		}
	}

	minVer := info.GliaMinVersion
	if minVer == "" {
		minVer = "(any)"
	}
	return CheckResult{
		Name:    "schema",
		Status:  StatusOK,
		Message: fmt.Sprintf("schema v%d, min_version=%s, binary=%s", info.SchemaVersion, minVer, Version),
	}
}

// checkIndex verifies index.json exists and is consistent with memory.jsonl.
// If the index is missing or stale, the result is Fixable (rebuild via open store).
func checkIndex(storeDir string) CheckResult {
	idxPath := filepath.Join(storeDir, "index.json")
	if _, err := os.Stat(idxPath); os.IsNotExist(err) {
		return CheckResult{
			Name:    "index.json",
			Status:  StatusWarn,
			Message: "index.json missing — run doctor --fix to rebuild",
			Fixable: true,
			FixFn:   fixRebuildIndex(storeDir),
		}
	}

	// Stats internally validates index fingerprint.
	_, err := store.Stats(storeDir)
	if err != nil {
		return CheckResult{
			Name:    "index.json",
			Status:  StatusWarn,
			Message: fmt.Sprintf("index may be stale or corrupt: %v — run doctor --fix to rebuild", err),
			Fixable: true,
			FixFn:   fixRebuildIndex(storeDir),
		}
	}

	return CheckResult{
		Name:    "index.json",
		Status:  StatusOK,
		Message: "index.json present and consistent",
	}
}

// fixRebuildIndex returns a FixFn that opens the store and calls Rebuild().
// Opening acquires the advisory lock; the fix aborts if the store is locked.
func fixRebuildIndex(storeDir string) func() error {
	return func() error {
		s, err := store.Open(storeDir)
		if err != nil {
			return fmt.Errorf("rebuild index: open store: %w", err)
		}
		defer s.Close()
		if err := s.Rebuild(); err != nil {
			return fmt.Errorf("rebuild index: %w", err)
		}
		return nil
	}
}

// checkEngram calls Health() on the engram adapter if present.
func checkEngram(ctx context.Context, adapters map[string]adapter.Adapter) CheckResult {
	a, ok := adapters["engram"]
	if !ok {
		return CheckResult{
			Name:    "engram",
			Status:  StatusWarn,
			Message: "engram adapter not configured or disabled",
		}
	}
	if err := a.Health(ctx); err != nil {
		return CheckResult{
			Name:    "engram",
			Status:  StatusWarn,
			Message: fmt.Sprintf("not reachable: %v", err),
		}
	}
	return CheckResult{
		Name:    "engram",
		Status:  StatusOK,
		Message: "reachable",
	}
}

// checkClaudeMem calls Health() on the claude-mem adapter if present.
func checkClaudeMem(ctx context.Context, adapters map[string]adapter.Adapter) CheckResult {
	a, ok := adapters["claude-mem"]
	if !ok {
		return CheckResult{
			Name:    "claude-mem",
			Status:  StatusWarn,
			Message: "claude-mem adapter not configured or disabled",
		}
	}
	if err := a.Health(ctx); err != nil {
		return CheckResult{
			Name:    "claude-mem",
			Status:  StatusWarn,
			Message: fmt.Sprintf("not reachable: %v", err),
		}
	}
	return CheckResult{
		Name:    "claude-mem",
		Status:  StatusOK,
		Message: "reachable",
	}
}

// checkGitignore verifies .gitignore has the required entries and does NOT
// contain memory.jsonl (which must be committed per D2).
func checkGitignore(giPath string) CheckResult {
	lines, err := readLines(giPath)
	if err != nil && os.IsNotExist(err) {
		return CheckResult{
			Name:    "gitignore",
			Status:  StatusWarn,
			Message: ".gitignore missing — required entries absent; run doctor --fix",
			Fixable: true,
			FixFn:   fixGitignore(giPath),
		}
	}
	if err != nil {
		return CheckResult{
			Name:    "gitignore",
			Status:  StatusErr,
			Message: fmt.Sprintf("read .gitignore: %v", err),
		}
	}

	existingSet := make(map[string]struct{}, len(lines))
	for _, l := range lines {
		existingSet[strings.TrimSpace(l)] = struct{}{}
	}

	// Check for the two required entries.
	var missingEntries []string
	for _, e := range gitignoreEntries {
		if _, found := existingSet[e]; !found {
			missingEntries = append(missingEntries, e)
		}
	}

	// Check for stale memory.jsonl entry (D2 violation).
	var memoryJSONLLines []string
	for _, l := range lines {
		if memoryJSONLGitignorePattern.MatchString(l) {
			memoryJSONLLines = append(memoryJSONLLines, l)
		}
	}

	hasIssues := len(missingEntries) > 0 || len(memoryJSONLLines) > 0
	if !hasIssues {
		return CheckResult{
			Name:    "gitignore",
			Status:  StatusOK,
			Message: "required entries present; memory.jsonl not gitignored",
		}
	}

	var msgs []string
	if len(missingEntries) > 0 {
		msgs = append(msgs, fmt.Sprintf("missing entries: %s", strings.Join(missingEntries, ", ")))
	}
	if len(memoryJSONLLines) > 0 {
		msgs = append(msgs, "memory.jsonl is gitignored but must be committed (D2)")
	}

	return CheckResult{
		Name:    "gitignore",
		Status:  StatusWarn,
		Message: strings.Join(msgs, "; ") + " — run doctor --fix",
		Fixable: true,
		FixFn:   fixGitignore(giPath),
	}
}

// fixGitignore returns a FixFn that adds missing entries and removes stale
// memory.jsonl lines from .gitignore (REQ-DOC-03).
func fixGitignore(giPath string) func() error {
	return func() error {
		lines, err := readLines(giPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read .gitignore: %w", err)
		}

		// Build set of existing trimmed lines.
		existingSet := make(map[string]struct{}, len(lines))
		for _, l := range lines {
			existingSet[strings.TrimSpace(l)] = struct{}{}
		}

		// Filter out stale memory.jsonl lines.
		var filtered []string
		var removed []string
		for _, l := range lines {
			if memoryJSONLGitignorePattern.MatchString(l) {
				removed = append(removed, l)
				continue
			}
			filtered = append(filtered, l)
		}

		// Determine missing required entries.
		var missing []string
		for _, e := range gitignoreEntries {
			if _, found := existingSet[e]; !found {
				missing = append(missing, e)
			}
		}

		// If nothing to change, no-op.
		if len(removed) == 0 && len(missing) == 0 {
			return nil
		}

		// Print a unified diff-style summary before applying.
		for _, r := range removed {
			fmt.Printf("~ .gitignore\n- %s\n", r)
		}
		for _, m := range missing {
			fmt.Printf("~ .gitignore\n+ %s\n", m)
		}

		// Write the filtered lines back, then append missing entries.
		var sb strings.Builder
		for _, l := range filtered {
			sb.WriteString(l)
			sb.WriteByte('\n')
		}
		for _, m := range missing {
			sb.WriteString(m)
			sb.WriteByte('\n')
		}

		return os.WriteFile(giPath, []byte(sb.String()), 0644)
	}
}

// checkStaleLock checks whether a stale .lock file is present.
// A lock is "stale" when the file exists but the PID recorded inside is not alive.
func checkStaleLock(lockPath string) CheckResult {
	_, err := os.Stat(lockPath)
	if os.IsNotExist(err) {
		return CheckResult{
			Name:    "lock",
			Status:  StatusOK,
			Message: "no .lock file",
		}
	}
	if err != nil {
		return CheckResult{
			Name:    "lock",
			Status:  StatusErr,
			Message: fmt.Sprintf("stat .lock: %v", err),
		}
	}

	// Read PID from .lock file (gofrs/flock writes just the PID as text).
	pid, err := readLockPID(lockPath)
	if err != nil {
		// Cannot read PID — treat as stale.
		return CheckResult{
			Name:    "lock",
			Status:  StatusWarn,
			Message: fmt.Sprintf(".lock exists but PID unreadable (%v) — may be stale; run doctor --fix", err),
			Fixable: true,
			FixFn:   fixStaleLock(lockPath),
		}
	}

	if isProcessAlive(pid) {
		return CheckResult{
			Name:    "lock",
			Status:  StatusWarn,
			Message: fmt.Sprintf(".lock held by PID %d (process is alive)", pid),
		}
	}

	return CheckResult{
		Name:    "lock",
		Status:  StatusWarn,
		Message: fmt.Sprintf(".lock is stale (PID %d not alive) — run doctor --fix to remove", pid),
		Fixable: true,
		FixFn:   fixStaleLock(lockPath),
	}
}

// isProcessAlive returns true if a process with the given PID is running.
// Sends signal 0 (no-op probe) — returns true if the process is alive or we
// lack permission to signal it (EPERM), false if ESRCH (no such process).
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true // process alive and we can signal it
	}
	// EPERM → process exists but we cannot signal it → treat as alive (conservative).
	if strings.Contains(err.Error(), "operation not permitted") {
		return true
	}
	// ESRCH or "process already finished" → dead.
	return false
}

// readLockPID reads the PID integer from a gofrs/flock-written .lock file.
// gofrs/flock writes the PID as plain decimal text.
func readLockPID(lockPath string) (int, error) {
	f, err := os.Open(lockPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			pid, err := strconv.Atoi(line)
			if err == nil {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("no valid PID in lock file")
}

// fixStaleLock returns a FixFn that removes the stale .lock file.
func fixStaleLock(lockPath string) func() error {
	return func() error {
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale lock: %w", err)
		}
		return nil
	}
}

// fixRecoverMemory returns a FixFn that truncates memory.jsonl to the last
// complete line (PRD-3 §9 recovery). Only partial-last-line truncation is done;
// no complete lines are removed (REQ-DOC-03).
func fixRecoverMemory(memPath string) func() error {
	return func() error {
		f, err := os.OpenFile(memPath, os.O_RDWR, 0644)
		if err != nil {
			return fmt.Errorf("open memory.jsonl for recovery: %w", err)
		}
		defer f.Close()
		discarded, err := store.RecoverPartialLine(f)
		if err != nil {
			return fmt.Errorf("recover partial line: %w", err)
		}
		if discarded > 0 {
			fmt.Printf("~ memory.jsonl: truncated %d bytes (partial last line removed)\n", discarded)
		}
		return nil
	}
}

// runFixes iterates check results and calls FixFn for each fixable issue.
func runFixes(cmd *cobra.Command, storeDir, giPath, lockPath string, checks []CheckResult) {
	for _, r := range checks {
		if r.Fixable && r.Status != StatusOK && r.FixFn != nil {
			if err := r.FixFn(); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "fix %s: %v\n", r.Name, err)
			}
		}
	}
}

// printResults renders an aligned table with glyphs and a summary line.
func printResults(cmd *cobra.Command, checks []CheckResult) {
	w := cmd.OutOrStdout()

	const nameWidth = 20
	warnCount, errCount := 0, 0

	for _, r := range checks {
		glyph := "✓"
		switch r.Status {
		case StatusWarn:
			glyph = "⚠"
			warnCount++
		case StatusErr:
			glyph = "✗"
			errCount++
		}

		name := r.Name
		pad := nameWidth - len(name)
		if pad < 1 {
			pad = 1
		}
		fmt.Fprintf(w, "%s  %s%s%s\n", glyph, name, strings.Repeat(" ", pad), r.Message)
	}

	fmt.Fprintln(w)
	if errCount == 0 && warnCount == 0 {
		fmt.Fprintln(w, "All checks passed.")
	} else {
		fmt.Fprintf(w, "%d error(s), %d warning(s)\n", errCount, warnCount)
	}
}
