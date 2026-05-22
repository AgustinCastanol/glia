package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// providerIDMapAdapter wraps *store.ProviderIDMapSnapshot to satisfy adapter.IDMap.
// ProviderIDMapSnapshot uses plain string keys; adapter.IDMap uses named types.
type providerIDMapAdapter struct {
	inner *store.ProviderIDMapSnapshot
}

func (a providerIDMapAdapter) CanonicalFromNative(id adapter.NativeID) (adapter.CanonicalID, bool) {
	v, ok := a.inner.CanonicalFromNative(string(id))
	return adapter.CanonicalID(v), ok
}

func (a providerIDMapAdapter) NativeFromCanonical(id adapter.CanonicalID) (adapter.NativeID, bool) {
	v, ok := a.inner.NativeFromCanonical(string(id))
	return adapter.NativeID(v), ok
}

// pushProvider executes the push loop for a single provider adapter (§6.1).
//
// Algorithm:
//  1. Read watermark from store (last_pushed_at).
//  2. ListNative since watermark.
//  3. Warn at ≥1000 records (D7). Cap at opts.Max if set.
//  4. For each id: ReadNative → ToCanonical → equality check → batch.
//  5. AppendBatch (single fsync).
//  6. Advance watermark.
func (e *Engine) pushProvider(ctx context.Context, a adapter.Adapter) (ProviderResult, error) {
	var result ProviderResult

	// Step 1: watermark.
	since, _ := readWatermark(e.store, a.Name())

	// Step 2: list native IDs updated since watermark.
	rawIDMap := e.store.ProviderIDMap(a.Name())
	idmap := providerIDMapAdapter{inner: rawIDMap}
	ids, err := a.ListNative(ctx, e.project(), since)
	if err != nil {
		return result, fmt.Errorf("push %s: ListNative: %w", a.Name(), err)
	}

	// Step 3: large-run warning (D7 / REQ-SE-26).
	if len(ids) >= 1000 {
		fmt.Fprintf(e.w, "WARN large sync: %d records from %s, this may take a while\n",
			len(ids), a.Name())
	}

	// Cap at opts.Max if set (REQ-SE-27).
	if e.opts.Max > 0 && len(ids) > e.opts.Max {
		ids = ids[:e.opts.Max]
	}

	// Step 4: build the batch.
	var batch []store.CanonicalRecord
	var maxUpdatedAt time.Time

	for _, nativeID := range ids {
		native, err := a.ReadNative(ctx, nativeID)
		if err != nil {
			// REQ-SE-25: per-record soft error — warn and continue.
			fmt.Fprintf(e.w, "WARN push %s: ReadNative %s: %v\n", a.Name(), nativeID, err)
			continue
		}

		canon, err := a.ToCanonical(native, idmap)
		if err != nil {
			if errors.Is(err, adapter.ErrUnsupported) {
				// relation record — skip silently.
				continue
			}
			fmt.Fprintf(e.w, "WARN push %s: ToCanonical %s: %v\n", a.Name(), nativeID, err)
			continue
		}

		// Track max updated_at across all processed records (REQ-SE-19).
		if t, err2 := time.Parse(time.RFC3339, canon.UpdatedAt); err2 == nil {
			if t.After(maxUpdatedAt) {
				maxUpdatedAt = t
			}
		}

		// Equality check (D5 / REQ-SE-22d).
		canonicalID, known := idmap.CanonicalFromNative(adapter.NativeID(nativeID))
		if known {
			prior, readErr := e.store.ReadLive(string(canonicalID))
			if readErr == nil {
				if recordsEqualIgnoringMetadata(prior, canon) {
					// Unchanged — skip append but still count as processed.
					result.Skipped++
					continue
				}
				// Changed — carry the existing canonical_id so the store can
				// compute the correct revision+supersedes.
				canon.CanonicalID = string(canonicalID)
			} else {
				// REQ-SE-23: ReadLive failed for known ID → treat as new, warn.
				fmt.Fprintf(e.w, "WARN push %s: ReadLive %s: %v — treating as new\n",
					a.Name(), canonicalID, readErr)
				canon.CanonicalID = "" // let the store assign a new canonical_id
			}
		}
		// else: unknown native ID → new chain; canon.CanonicalID already empty from ToCanonical.

		batch = append(batch, canon)
	}

	// Step 5: write batch (dry-run gate).
	if len(batch) > 0 {
		if e.opts.DryRun {
			fmt.Fprintf(e.w, "DRY-RUN push %s: would append %d records\n", a.Name(), len(batch))
			result.Pushed += len(batch)
		} else {
			committed, appendErr := e.store.AppendBatch(batch)
			if appendErr != nil {
				return result, fmt.Errorf("push %s: AppendBatch: %w", a.Name(), appendErr)
			}
			result.Pushed += len(committed)

			// Bind native↔canonical IDs so subsequent push runs can perform
			// equality checks via CanonicalFromNative (re-uses BindProvider used by pull).
			for _, r := range committed {
				if r.Origin.ProviderID != "" {
					if bErr := e.store.BindProvider(a.Name(), r.Origin.ProviderID, r.CanonicalID); bErr != nil {
						fmt.Fprintf(e.w, "WARN push %s: bind %s→%s: %v\n",
							a.Name(), r.Origin.ProviderID, r.CanonicalID, bErr)
					}
				}
			}
		}
	}

	// Step 6: advance push watermark (REQ-SE-19).
	// Use maxUpdatedAt when available; fall back to now.
	if !e.opts.DryRun {
		if maxUpdatedAt.IsZero() {
			maxUpdatedAt = time.Now().UTC()
		}
		if wErr := writeWatermark(e.store, a.Name(), maxUpdatedAt, result); wErr != nil {
			fmt.Fprintf(e.w, "WARN push %s: update sync state: %v\n", a.Name(), wErr)
		}
	}

	return result, nil
}

// project returns the effective project name from rootFlags (CLI) or a default.
// The engine itself does not import cobra; callers set it via the project field
// added to Options (or the storePath-based default used here).
func (e *Engine) project() string {
	if e.opts.Project != "" {
		return e.opts.Project
	}
	return ""
}
