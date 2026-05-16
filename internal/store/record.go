package store

import (
	"encoding/json"
	"fmt"
)

// CanonicalRecord is one observation line in memory.jsonl.
// All fields are serialized via encoding/json.
type CanonicalRecord struct {
	CanonicalID   string   `json:"canonical_id"`
	LineULID      string   `json:"line_ulid"`
	SchemaVersion int      `json:"schema_version"`
	Kind          string   `json:"kind"`
	Revision      int      `json:"revision"`
	Supersedes    string   `json:"supersedes"`
	Deleted       bool     `json:"deleted"`
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	ContentFormat string   `json:"content_format"`
	Origin        Origin   `json:"origin"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	Tags          []string `json:"tags"`
	TopicKey      string   `json:"topic_key"`
	Type          string   `json:"type"`
}

// Origin describes the provider source of a record.
type Origin struct {
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
	Author     string `json:"author"`
	SessionID  string `json:"session_id"`
}

// rawRecord is the minimal decode used to gate by schema_version
// before full unmarshalling (REQ-READ-03). The second pass re-unmarshals
// from the original data bytes directly.
type rawRecord struct {
	SchemaVersion int        `json:"schema_version"`
	CanonicalID   string     `json:"canonical_id"`
	LineULID      string     `json:"line_ulid"`
	Revision      int        `json:"revision"`
	UpdatedAt     string     `json:"updated_at"`
	Deleted       bool       `json:"deleted"`
	Origin        rawOrigin  `json:"origin"`
}

// rawOrigin is the minimal origin decode used during rebuild to populate ByProvider.
type rawOrigin struct {
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
}

var validKinds = map[string]bool{
	"observation":     true,
	"session_summary": true,
	"relation":        true,
}

// validateRecord enforces the schema invariants for a CanonicalRecord.
// It does NOT assign any fields; callers must fill canonical_id, line_ulid,
// revision, and supersedes before calling.
func validateRecord(r CanonicalRecord) error {
	if !validKinds[r.Kind] {
		return fmt.Errorf("validate: kind %q not in {observation,session_summary,relation}: %w", r.Kind, ErrInvalidRecord)
	}
	if !r.Deleted && r.ContentFormat == "" {
		return fmt.Errorf("validate: content_format required for non-tombstone: %w", ErrInvalidRecord)
	}
	if r.Deleted && r.CanonicalID != "" && r.Supersedes != r.CanonicalID {
		return fmt.Errorf("validate: tombstone supersedes %q must equal canonical_id %q: %w", r.Supersedes, r.CanonicalID, ErrInvalidRecord)
	}
	return nil
}

// decodeLine performs a two-pass decode of a single JSONL line.
// Pass 1: decode only schema_version (rawRecord). Lines with schema_version
// greater than StoreSupportedVersion are skipped (returned as zero value, false).
// Pass 2: full decode into CanonicalRecord.
func decodeLine(data []byte) (CanonicalRecord, bool, error) {
	var raw rawRecord
	if err := json.Unmarshal(data, &raw); err != nil {
		return CanonicalRecord{}, false, fmt.Errorf("decodeLine pass1: %w", err)
	}
	if raw.SchemaVersion > StoreSupportedVersion {
		return CanonicalRecord{}, false, nil // skip silently
	}
	var r CanonicalRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return CanonicalRecord{}, false, fmt.Errorf("decodeLine pass2: %w", err)
	}
	return r, true, nil
}
