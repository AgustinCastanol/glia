package sync

import (
	"fmt"
	"io"
	"strings"
)

// ProviderResult aggregates per-provider counters for a single sync run.
type ProviderResult struct {
	Pulled         int
	Pushed         int
	Skipped        int // equal records, no append needed
	UpdatesSkipped int // engram update-gap skips (D9)
}

// RunReport is the aggregate result returned by Push, Pull, and Sync.
type RunReport struct {
	// PerProvider maps provider name to its per-run counters.
	PerProvider map[string]ProviderResult

	// UpdatesSkipped is the sum of UpdatesSkipped across all providers.
	UpdatesSkipped int

	// Conflicts is the number of (canonical_id, revision) conflicts detected
	// or present after the run.
	Conflicts int

	// HardErrors collects per-provider errors that caused a provider to be skipped
	// entirely (e.g. Health failure). The sync run may still exit 0 if at least
	// one provider succeeded.
	HardErrors []error
}

// StatusReport is returned by Engine.Status.
type StatusReport struct {
	// ProviderHealth maps provider name to the error returned by Health().
	// A nil value means the provider is healthy.
	ProviderHealth map[string]error

	// Conflicts is the current conflict list from the store.
	Conflicts []ConflictSummary
}

// ConflictSummary is a flattened view of a store.ConflictEntry for display.
type ConflictSummary struct {
	CanonicalID string `json:"canonical_id"`
	Revision    int    `json:"revision"`
	DupCount    int    `json:"dup_count"`
	DetectedAt  string `json:"detected_at"`
}

// conflictSummary is the unexported alias kept for internal use in engine.go.
// All external code uses ConflictSummary.
type conflictSummary = ConflictSummary

// WriteSummary prints a human-readable run summary to w.
// Format matches REQ-SE-32.
func (r *RunReport) WriteSummary(w io.Writer) {
	if r == nil {
		return
	}

	providers := make([]string, 0, len(r.PerProvider))
	for p := range r.PerProvider {
		providers = append(providers, p)
	}
	// Deterministic output order.
	sortStrings(providers)

	for _, p := range providers {
		pr := r.PerProvider[p]
		fmt.Fprintf(w, "[%s] pulled=%d pushed=%d skipped=%d\n",
			p, pr.Pulled, pr.Pushed, pr.Skipped)
	}

	if r.UpdatesSkipped > 0 {
		fmt.Fprintf(w, "updates_skipped=%d (engram update gap)\n", r.UpdatesSkipped)
	}

	if r.Conflicts > 0 {
		fmt.Fprintf(w, "conflicts=%d — run `glia status --conflicts` and `glia sync resolve`\n",
			r.Conflicts)
	}

	if len(r.HardErrors) > 0 {
		parts := make([]string, len(r.HardErrors))
		for i, e := range r.HardErrors {
			parts[i] = e.Error()
		}
		fmt.Fprintf(w, "hard_errors: %s\n", strings.Join(parts, "; "))
	}
}

// sortStrings is a simple insertion sort to avoid importing sort in this package.
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}
