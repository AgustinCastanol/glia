package store

import (
	"fmt"
	"time"
)

// buildTombstone constructs a tombstone CanonicalRecord for an existing chain.
// The returned record has deleted=true, revision=existing.LatestRevision+1,
// supersedes=canonicalID (self-referential per REQ-DATA-04), and empty
// content/content_format. Append unconditionally overwrites line_ulid.
func buildTombstone(canonicalID string, existing IndexEntry, kind string) CanonicalRecord {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return CanonicalRecord{
		CanonicalID:   canonicalID,
		SchemaVersion: StoreSupportedVersion,
		Kind:          kind,
		Revision:      existing.LatestRevision + 1,
		Supersedes:    canonicalID,
		Deleted:       true,
		Content:       "",
		ContentFormat: "",
		UpdatedAt:     now,
		CreatedAt:     now,
		Tags:          []string{},
	}
}

// validateTombstone checks tombstone-specific invariants beyond validateRecord.
// Returns ErrInvalidRecord if deleted=true and supersedes != canonical_id.
func validateTombstone(r CanonicalRecord) error {
	if !r.Deleted {
		return nil
	}
	if r.CanonicalID != "" && r.Supersedes != r.CanonicalID {
		return fmt.Errorf("tombstone: supersedes %q must equal canonical_id %q: %w",
			r.Supersedes, r.CanonicalID, ErrInvalidRecord)
	}
	return nil
}
