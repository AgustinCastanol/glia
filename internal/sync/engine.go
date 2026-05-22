package sync

import (
	"context"
	"fmt"
	"io"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
	"github.com/agustincastanol/wrapper-mems/internal/store"
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

	// Commit, when true, runs `git add .wrapper-mems/ && git commit` after a
	// successful non-dry-run sync (REQ-SE-11).
	Commit bool
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

// Push iterates active providers, checks Health, and pushes native records
// into the canonical store. The push logic is implemented in push.go (PR-D);
// this stub returns an empty report so PR-B compiles without push.go.
//
// Stub — replaced in PR-D.
func (e *Engine) Push(ctx context.Context) (*RunReport, error) {
	return &RunReport{PerProvider: make(map[string]ProviderResult)}, nil
}

// Pull iterates active providers and writes canonical records out to each
// provider via WriteNative. The pull logic is implemented in pull.go (PR-D);
// this stub returns an empty report so PR-B compiles without pull.go.
//
// Stub — replaced in PR-D.
func (e *Engine) Pull(ctx context.Context) (*RunReport, error) {
	return &RunReport{PerProvider: make(map[string]ProviderResult)}, nil
}

// Sync runs Pull then Push (REQ-SE-07, REQ-SE-31). Mirror-engram shell-outs
// wrap the run when enabled (D11). Full implementation is in PR-D; this stub
// delegates to the stubbed Push/Pull so the package compiles.
//
// Stub — replaced in PR-D.
func (e *Engine) Sync(ctx context.Context) (*RunReport, error) {
	pullReport, err := e.Pull(ctx)
	if err != nil {
		return nil, fmt.Errorf("sync pull: %w", err)
	}
	pushReport, err := e.Push(ctx)
	if err != nil {
		return nil, fmt.Errorf("sync push: %w", err)
	}

	// Merge reports.
	merged := &RunReport{
		PerProvider:    make(map[string]ProviderResult),
		UpdatesSkipped: pullReport.UpdatesSkipped + pushReport.UpdatesSkipped,
		Conflicts:      pullReport.Conflicts + pushReport.Conflicts,
	}
	for p, r := range pullReport.PerProvider {
		merged.PerProvider[p] = r
	}
	for p, r := range pushReport.PerProvider {
		existing := merged.PerProvider[p]
		existing.Pulled += r.Pulled
		existing.Pushed += r.Pushed
		existing.Skipped += r.Skipped
		existing.UpdatesSkipped += r.UpdatesSkipped
		merged.PerProvider[p] = existing
	}
	merged.HardErrors = append(pullReport.HardErrors, pushReport.HardErrors...)
	return merged, nil
}

// Status checks Health on each active provider and returns current conflict
// state from the store (REQ-SE-23, REQ-SE-52).
func (e *Engine) Status(ctx context.Context) (*StatusReport, error) {
	providers := e.activeProviders()
	report := &StatusReport{
		ProviderHealth: make(map[string]error, len(providers)),
	}

	for _, a := range providers {
		report.ProviderHealth[a.Name()] = a.Health(ctx)
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
