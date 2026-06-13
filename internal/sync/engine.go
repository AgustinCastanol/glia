package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/store"
)

// Options controls the behaviour of a single sync run.
// All fields are optional; zero values produce the safest defaults.
type Options struct {
	// DryRun prevents any writes to memory.jsonl, index.json, or any adapter.
	DryRun bool

	// ProviderFilter, when non-empty, restricts the run to the named providers.
	// Multiple values are OR-ed. Empty means all configured providers.
	ProviderFilter []string

	// Max, when > 0, caps the number of records processed per provider per run.
	Max int

	// Verbose enables detailed progress output to the engine's writer.
	Verbose bool

	// MirrorEngram triggers the engram shell-out before pull and after push,
	// independently of config.yaml (REQ-SE-10).
	MirrorEngram bool

	// Commit, when true, runs `git add .glia/ && git commit` after a
	// successful non-dry-run sync (REQ-SE-11).
	Commit bool

	// Project is the project name forwarded to adapter.ListNative.
	// Empty string is valid; adapters handle it as "all projects".
	Project string
}

// Engine orchestrates push, pull, and full-sync operations across all
// registered provider adapters, reading from and writing to a *store.Store.
//
// Construct via New; do not use a zero-value Engine.
type Engine struct {
	store    *store.Store
	adapters map[string]adapter.Adapter
	cfg      Config
	opts     Options
	w        io.Writer
}

// New constructs an Engine. w is the destination for progress/diagnostic output
// (typically os.Stderr for warnings, os.Stdout for summaries — callers wire this).
// Pass ioutil.Discard to suppress all output.
func New(s *store.Store, adapters map[string]adapter.Adapter, cfg Config, opts Options, w io.Writer) *Engine {
	if w == nil {
		w = io.Discard
	}
	return &Engine{
		store:    s,
		adapters: adapters,
		cfg:      cfg,
		opts:     opts,
		w:        w,
	}
}

// activeProviders returns the ordered list of adapters to operate on,
// respecting both cfg.Providers and opts.ProviderFilter.
func (e *Engine) activeProviders() []adapter.Adapter {
	// Start from cfg.Providers order; fall back to map iteration order if empty.
	var names []string
	if len(e.cfg.Providers) > 0 {
		names = e.cfg.Providers
	} else {
		for name := range e.adapters {
			names = append(names, name)
		}
		sortStrings(names)
	}

	// Apply ProviderFilter if set.
	filter := make(map[string]bool, len(e.opts.ProviderFilter))
	for _, p := range e.opts.ProviderFilter {
		filter[p] = true
	}

	var result []adapter.Adapter
	for _, name := range names {
		if len(filter) > 0 && !filter[name] {
			continue
		}
		if a, ok := e.adapters[name]; ok {
			result = append(result, a)
		}
	}
	return result
}

// activeSources returns adapters registered in e.adapters whose
// WriteCapability() == "read-only" AND whose name is NOT already covered by
// activeProviders() (i.e. not in cfg.Providers). This identifies pure read-only
// sources like the openspec adapter that the provider list does not enumerate.
//
// activeSources respects opts.ProviderFilter: if a filter is set, only sources
// whose name matches the filter are returned (so --provider openspec works).
//
// Deterministic order: names are sorted before iteration.
func (e *Engine) activeSources() []adapter.Adapter {
	// Build the set of names already covered by cfg.Providers so we can exclude
	// them. We use cfg.Providers directly (not activeProviders()) because
	// activeProviders() may be further narrowed by ProviderFilter — we want the
	// full configured-provider set for the exclusion guard.
	providerSet := make(map[string]bool, len(e.cfg.Providers))
	for _, name := range e.cfg.Providers {
		providerSet[name] = true
	}

	// Collect all adapter names in deterministic order.
	allNames := make([]string, 0, len(e.adapters))
	for name := range e.adapters {
		allNames = append(allNames, name)
	}
	sortStrings(allNames)

	// Apply ProviderFilter (if set) to sources as well.
	filter := make(map[string]bool, len(e.opts.ProviderFilter))
	for _, p := range e.opts.ProviderFilter {
		filter[p] = true
	}

	var result []adapter.Adapter
	for _, name := range allNames {
		// Skip adapters already handled by activeProviders().
		if providerSet[name] {
			continue
		}
		a := e.adapters[name]
		// Only include genuinely read-only adapters (pure sources).
		if a.WriteCapability() != "read-only" {
			continue
		}
		// Apply ProviderFilter if set.
		if len(filter) > 0 && !filter[name] {
			continue
		}
		result = append(result, a)
	}
	return result
}

// Push iterates active providers, checks Health, and pushes native records
// into the canonical store (§6.1 / REQ-SE-21..27).
//
// Read-only source adapters (those not in cfg.Providers but registered in the
// adapters map with WriteCapability == "read-only") are also ingested in Push
// via activeSources(). They are never iterated in Pull (PRD-11 §9).
func (e *Engine) Push(ctx context.Context) (*RunReport, error) {
	report := &RunReport{PerProvider: make(map[string]ProviderResult)}
	providers := e.activeProviders()

	allFailed := len(providers) > 0
	for _, a := range providers {
		if err := a.Health(ctx); err != nil {
			fmt.Fprintf(e.w, "provider %s unavailable, skipping: %v\n", a.Name(), err)
			report.HardErrors = append(report.HardErrors,
				fmt.Errorf("provider %s: %w", a.Name(), err))
			continue
		}
		allFailed = false

		result, err := e.pushProvider(ctx, a)
		if err != nil {
			fmt.Fprintf(e.w, "WARN push %s: %v\n", a.Name(), err)
			report.HardErrors = append(report.HardErrors,
				fmt.Errorf("push %s: %w", a.Name(), err))
			continue
		}
		report.PerProvider[a.Name()] = result
		report.UpdatesSkipped += result.UpdatesSkipped
	}

	// If ALL providers failed Health, surface as a hard-error report.
	if allFailed && len(providers) > 0 {
		return report, nil
	}

	// Ingest read-only sources (PRD-11 §9): sources are push-only; they are
	// never exported in Pull. activeSources() excludes any name already in
	// cfg.Providers to prevent double-counting.
	for _, a := range e.activeSources() {
		if err := a.Health(ctx); err != nil {
			fmt.Fprintf(e.w, "source %s unavailable, skipping: %v\n", a.Name(), err)
			report.HardErrors = append(report.HardErrors,
				fmt.Errorf("source %s: %w", a.Name(), err))
			continue
		}

		result, err := e.pushProvider(ctx, a)
		if err != nil {
			fmt.Fprintf(e.w, "WARN push source %s: %v\n", a.Name(), err)
			report.HardErrors = append(report.HardErrors,
				fmt.Errorf("push source %s: %w", a.Name(), err))
			continue
		}
		report.PerProvider[a.Name()] = result
		report.UpdatesSkipped += result.UpdatesSkipped
	}

	// Reflect current conflict count (REQ-SE-51).
	report.Conflicts = len(e.store.Conflicts())
	return report, nil
}

// Pull iterates active providers and writes canonical records out to each
// provider via WriteNative (§6.2 / REQ-SE-28..30).
func (e *Engine) Pull(ctx context.Context) (*RunReport, error) {
	report := &RunReport{PerProvider: make(map[string]ProviderResult)}
	providers := e.activeProviders()

	allFailed := len(providers) > 0
	for _, a := range providers {
		if err := a.Health(ctx); err != nil {
			fmt.Fprintf(e.w, "provider %s unavailable, skipping: %v\n", a.Name(), err)
			report.HardErrors = append(report.HardErrors,
				fmt.Errorf("provider %s: %w", a.Name(), err))
			continue
		}
		allFailed = false

		result, err := e.pullProvider(ctx, a)
		if err != nil {
			fmt.Fprintf(e.w, "WARN pull %s: %v\n", a.Name(), err)
			report.HardErrors = append(report.HardErrors,
				fmt.Errorf("pull %s: %w", a.Name(), err))
			continue
		}
		report.PerProvider[a.Name()] = result
		report.UpdatesSkipped += result.UpdatesSkipped
		report.WriteErrors += result.WriteErrors
	}

	if allFailed && len(providers) > 0 {
		return report, nil
	}

	report.Conflicts = len(e.store.Conflicts())
	return report, nil
}

// Sync runs Pull then Push, with optional mirror-engram shell-outs (§6.3 /
// REQ-SE-07, REQ-SE-31, REQ-SE-40). After a non-dry-run success, if
// opts.Commit is set, stages and commits the .glia/ directory
// (REQ-SE-11).
func (e *Engine) Sync(ctx context.Context) (*RunReport, error) {
	if e.mirrorEngramEnabled() {
		e.mirrorEngram(e.opts.Project)
	}

	pullReport, err := e.Pull(ctx)
	if err != nil {
		return nil, fmt.Errorf("sync pull: %w", err)
	}

	pushReport, err := e.Push(ctx)
	if err != nil {
		return nil, fmt.Errorf("sync push: %w", err)
	}

	if e.mirrorEngramEnabled() {
		e.mirrorEngram(e.opts.Project)
	}

	// Merge reports (REQ-SE-32).
	merged := &RunReport{
		PerProvider:    make(map[string]ProviderResult),
		UpdatesSkipped: pullReport.UpdatesSkipped + pushReport.UpdatesSkipped,
		WriteErrors:    pullReport.WriteErrors + pushReport.WriteErrors,
		Conflicts:      pullReport.Conflicts + pushReport.Conflicts,
	}
	maps.Copy(merged.PerProvider, pullReport.PerProvider)
	for p, r := range pushReport.PerProvider {
		existing := merged.PerProvider[p]
		existing.Pulled += r.Pulled
		existing.Pushed += r.Pushed
		existing.Skipped += r.Skipped
		existing.UpdatesSkipped += r.UpdatesSkipped
		existing.WriteErrors += r.WriteErrors
		merged.PerProvider[p] = existing
	}
	merged.HardErrors = append(pullReport.HardErrors, pushReport.HardErrors...)
	merged.Conflicts = len(e.store.Conflicts())

	// --commit flag (REQ-SE-11).
	if e.opts.Commit && !e.opts.DryRun {
		e.gitCommit()
	}

	return merged, nil
}

// gitCommit runs `git add .glia/ && git commit -m "chore: sync [timestamp]"`
// in the store's parent directory. Failures are warnings, never hard errors (REQ-SE-11).
func (e *Engine) gitCommit() {
	storeParent := e.store.RootDir()
	if storeParent == "" {
		fmt.Fprintf(e.w, "WARN --commit: cannot determine store root\n")
		return
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	msg := fmt.Sprintf("chore: sync [%s]", ts)

	addCmd := exec.Command("git", "-C", storeParent, "add", ".glia/")
	var addErr bytes.Buffer
	addCmd.Stderr = &addErr
	if err := addCmd.Run(); err != nil {
		fmt.Fprintf(e.w, "WARN --commit: git add: %v\n", err)
		return
	}

	commitCmd := exec.Command("git", "-C", storeParent, "commit", "-m", msg)
	var commitErr bytes.Buffer
	commitCmd.Stderr = &commitErr
	if err := commitCmd.Run(); err != nil {
		fmt.Fprintf(e.w, "WARN --commit: git commit: %s\n", commitErr.String())
	}
}

// Status checks Health on each active provider and returns current conflict
// state from the store (REQ-SE-23, REQ-SE-52).
//
// Adapters whose WriteCapability() == "read-only" are treated as sources
// (PRD-11 §10): they appear in report.Sources rather than report.ProviderHealth
// so that the status CLI can render them as a distinct block without meaningless
// LAST_PUSHED columns.
func (e *Engine) Status(ctx context.Context) (*StatusReport, error) {
	providers := e.activeProviders()
	report := &StatusReport{
		ProviderHealth:          make(map[string]error, 0),
		ProviderWriteCapability: make(map[string]string, 0),
	}

	for _, a := range providers {
		if a.WriteCapability() == "read-only" {
			// Source adapter: collect health and freshness separately.
			ss := SourceStatus{
				Name:            a.Name(),
				WriteCapability: a.WriteCapability(),
			}
			healthErr := a.Health(ctx)
			if healthErr != nil {
				ss.Healthy = false
				ss.HealthError = healthErr.Error()
			} else {
				ss.Healthy = true
				// Best-effort: list artifacts to get count and newest mtime.
				if ids, listErr := a.ListNative(ctx, "", zeroTime); listErr == nil {
					ss.ArtifactCount = len(ids)
				}
			}
			report.Sources = append(report.Sources, ss)
			continue
		}
		report.ProviderHealth[a.Name()] = a.Health(ctx)
		// REQ-CMW-09: surface write capability per provider for glia status display.
		report.ProviderWriteCapability[a.Name()] = a.WriteCapability()
	}

	// Expose current conflicts as summaries.
	conflicts := e.store.Conflicts()
	report.Conflicts = make([]conflictSummary, len(conflicts))
	for i, c := range conflicts {
		report.Conflicts[i] = conflictSummary{
			CanonicalID: c.CanonicalID,
			Revision:    c.Revision,
			DupCount:    len(c.Duplicates),
			DetectedAt:  c.DetectedAt,
		}
	}

	return report, nil
}

// zeroTime is the zero value of time.Time used for unfiltered ListNative calls.
var zeroTime = time.Time{}

// Resolve resolves a conflict by selecting the duplicate at dupIndex (1-based)
// as the canonical version (REQ-SE-37, REQ-SE-38, REQ-SE-39).
//
// It appends a new superseding record with the chosen duplicate's payload,
// removes the ConflictEntry from the store, and triggers an index rebuild
// for the affected canonical_id.
func (e *Engine) Resolve(canonicalID string, dupIndex int) error {
	conflicts := e.store.Conflicts()

	// Find the ConflictEntry.
	var found *store.ConflictEntry
	for i := range conflicts {
		if conflicts[i].CanonicalID == canonicalID {
			found = &conflicts[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("no conflict found for %s", canonicalID)
	}

	// Validate dupIndex (1-based).
	if dupIndex < 1 || dupIndex > len(found.Duplicates) {
		return fmt.Errorf("invalid dup_index: %d (conflict has %d duplicates)",
			dupIndex, len(found.Duplicates))
	}

	chosen := found.Duplicates[dupIndex-1]

	// Read the chosen line from the store.
	chosenRecord, err := e.store.ReadLineAtOffset(chosen.LineOffset)
	if err != nil {
		return fmt.Errorf("resolve: read chosen line at offset %d: %w", chosen.LineOffset, err)
	}

	// Read the current live record to determine winner revision.
	liveRecord, err := e.store.ReadLive(canonicalID)
	if err != nil {
		return fmt.Errorf("resolve: read live record %s: %w", canonicalID, err)
	}

	// Build the superseding record: new revision, same payload as chosen duplicate.
	superseding := store.CanonicalRecord{
		CanonicalID:   canonicalID,
		Kind:          chosenRecord.Kind,
		Title:         chosenRecord.Title,
		Content:       chosenRecord.Content,
		ContentFormat: chosenRecord.ContentFormat,
		Type:          chosenRecord.Type,
		TopicKey:      chosenRecord.TopicKey,
		Tags:          chosenRecord.Tags,
		Origin:        chosenRecord.Origin,
		CreatedAt:     chosenRecord.CreatedAt,
		UpdatedAt:     chosenRecord.UpdatedAt,
		Revision:      liveRecord.Revision + 1,
		Supersedes:    canonicalID,
	}

	if _, err := e.store.Append(superseding); err != nil {
		return fmt.Errorf("resolve: append superseding record: %w", err)
	}

	// Remove the conflict from the index.
	if err := e.store.RemoveConflict(canonicalID); err != nil {
		return fmt.Errorf("resolve: remove conflict: %w", err)
	}

	// Trigger a rebuild so the new record becomes the live revision.
	if err := e.store.Rebuild(); err != nil {
		return fmt.Errorf("resolve: rebuild: %w", err)
	}

	return nil
}
