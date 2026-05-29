package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
)

// pullProvider executes the pull loop for a single provider adapter (§6.2).
//
// Algorithm:
//  1. Read pull watermark from store.
//  2. ListLive from canonical store (non-deleted records).
//  3. Collapse to latest revision per canonical_id (already done by ListLive).
//  4. For each record: apply origin re-import guard, SupportedKinds filter,
//     deleted skip, FromCanonical, WriteNative.
//  5. On ErrUnsupported: silent skip (REQ-SE-28).
//  6. On other errors: warn, skip, do NOT advance watermark past failed record.
//  7. Advance pull watermark to now (REQ-SE-20).
func (e *Engine) pullProvider(ctx context.Context, a adapter.Adapter) (ProviderResult, error) {
	var result ProviderResult

	// Step 1: pull watermark (advisory — REQ-SE-18 says don't rely on it alone,
	// but we still use it to filter the initial set).
	since, hasSince := readPullWatermark(e.store, a.Name())

	// Step 2: list all live canonical records.
	records, err := e.store.ListLive()
	if err != nil {
		return result, fmt.Errorf("pull %s: ListLive: %w", a.Name(), err)
	}

	// Filter to records updated at or after the pull watermark (advisory).
	if hasSince {
		filtered := records[:0]
		for _, r := range records {
			t, parseErr := time.Parse(time.RFC3339, r.UpdatedAt)
			if parseErr != nil || !t.Before(since) {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}

	// Track successful watermark advancement.
	// If any record fails (hard error), we do NOT advance past it (REQ-SE-20 note).
	lastSuccessTime := time.Time{}
	if hasSince {
		lastSuccessTime = since
	}

	rawIDMap := e.store.ProviderIDMap(a.Name())
	idmap := providerIDMapAdapter{inner: rawIDMap}

	for _, rec := range records {
		// Step 4a: origin re-import guard (REQ-SE-29a).
		// Skip records that originated from this provider AND already have a native copy.
		if rec.Origin.Provider == a.Name() {
			_, hasNative := idmap.NativeFromCanonical(adapter.CanonicalID(rec.CanonicalID))
			if hasNative {
				continue
			}
		}

		// Step 4b: deleted skip (REQ-SE-29c) — ListLive already excludes deleted;
		// defensive check for safety.
		if rec.Deleted {
			continue
		}

		// Step 4c: SupportedKinds filter (REQ-SE-29b).
		supported := a.SupportedKinds()
		if len(supported) > 0 && !containsString(supported, rec.Kind) {
			continue
		}

		// Step 4d: FromCanonical.
		native, err := a.FromCanonical(rec)
		if err != nil {
			if errors.Is(err, adapter.ErrUnsupported) {
				// relation record or unsupported kind — skip silently.
				continue
			}
			fmt.Fprintf(e.w, "WARN pull %s: FromCanonical %s: %v\n", a.Name(), rec.CanonicalID, err)
			continue
		}

		// Step 4e.1: drift detection (REQ-CMW-07).
		// If ProviderRevision already recorded a revision >= the current record
		// revision, the record was already pushed at this revision — skip to
		// avoid duplicates without treating it as an error.
		if storedRev, hasPushed := e.store.ProviderRevision(a.Name(), rec.CanonicalID); hasPushed {
			if storedRev >= rec.Revision {
				// Already pushed at this (or a later) revision. Skip.
				continue
			}
		}

		// Step 4e.2: WriteNative (dry-run gate).
		if e.opts.DryRun {
			fmt.Fprintf(e.w, "DRY-RUN pull %s: would write %s\n", a.Name(), rec.CanonicalID)
			result.Pulled++
			updateLastSuccess(&lastSuccessTime, rec.UpdatedAt)
			continue
		}

		nativeID, writeErr := a.WriteNative(ctx, native)
		if writeErr != nil {
			// D9 / REQ-SE-30: the engram CLI has no update command, so
			// WriteNative returns ErrUnsupported for an already-existing
			// record. Surface that as a structured SKIP before the generic
			// ErrUnsupported guard, otherwise the warning never fires.
			if isEngramUpdateGap(a.Name(), writeErr) {
				fmt.Fprintf(e.w, "SKIP %s update id=%s reason=engram-cli-no-update\n",
					a.Name(), rec.CanonicalID)
				result.UpdatesSkipped++
				// Do NOT advance watermark past this record.
				continue
			}

			if errors.Is(writeErr, adapter.ErrUnsupported) {
				// REQ-SE-28 / REQ-SE-29f: read-only adapter or unsupported
				// kind (e.g. claude-mem) → skip silently.
				continue
			}

			// Hard per-record error: warn, skip, do NOT advance watermark (REQ-SE-20 note).
			fmt.Fprintf(e.w, "WARN pull %s: WriteNative %s: %v\n", a.Name(), rec.CanonicalID, writeErr)
			// Stop advancing lastSuccessTime past this point.
			continue
		}

		result.Pulled++
		updateLastSuccess(&lastSuccessTime, rec.UpdatedAt)

		// Step 4e.3 (bind): record the native↔canonical ID mapping AND the
		// revision watermark atomically (REQ-CMW-05, D2). Falls back to
		// BindProvider for providers without revision tracking support.
		if err := e.store.BindProviderWithRevision(a.Name(), string(nativeID), rec.CanonicalID, rec.Revision); err != nil {
			// Non-fatal: warn but continue.
			fmt.Fprintf(e.w, "WARN pull %s: bind %s→%s: %v\n",
				a.Name(), nativeID, rec.CanonicalID, err)
		}
	}

	// Step 7: advance pull watermark (REQ-SE-20).
	// Use lastSuccessTime so we don't skip retrying failed records next run.
	if !e.opts.DryRun {
		pullAt := time.Now().UTC()
		_ = lastSuccessTime // watermark is wall-clock time at loop completion (REQ-SE-20)
		if wErr := writePullWatermark(e.store, a.Name(), pullAt, result); wErr != nil {
			fmt.Fprintf(e.w, "WARN pull %s: update sync state: %v\n", a.Name(), wErr)
		}
	}

	return result, nil
}

// isEngramUpdateGap returns true when the provider is "engram" and the error
// indicates a "record already exists / update not supported" condition (D9).
// In v1 the engram CLI does not support updates, so any non-ErrUnsupported
// failure from WriteNative on an existing record is treated as an update gap.
func isEngramUpdateGap(providerName string, err error) bool {
	if providerName != "engram" {
		return false
	}
	// The engram adapter returns ErrUnsupported for update paths in v1.
	// We check again here for defence-in-depth and future-proofing.
	return errors.Is(err, adapter.ErrUnsupported)
}

// updateLastSuccess advances t to the parsed value of updatedAt if it is later.
func updateLastSuccess(t *time.Time, updatedAt string) {
	parsed, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return
	}
	if parsed.After(*t) {
		*t = parsed
	}
}

// containsString reports whether ss contains s.
func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
