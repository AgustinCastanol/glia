package store

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/cespare/xxhash/v2"
)

// ProviderSyncState tracks watermark and counters for a single provider.
type ProviderSyncState struct {
	LastPulledAt     string `json:"last_pulled_at"`
	LastPushedAt     string `json:"last_pushed_at"`
	LastNativeCursor string `json:"last_native_cursor,omitempty"`
	RecordsPulled    int    `json:"records_pulled"`
	RecordsPushed    int    `json:"records_pushed"`
}

// ConflictDuplicate is one entry in a ConflictEntry's Duplicates slice.
// LineOffset and LineULID identify the physical JSONL line.
type ConflictDuplicate struct {
	LineOffset int64  `json:"line_offset"`
	LineULID   string `json:"line_ulid"`
	UpdatedAt  string `json:"updated_at"`
	Provider   string `json:"provider"`
}

// ConflictEntry records a (canonical_id, revision) collision detected during rebuild.
// Duplicates holds all competing lines (winner-first, ordered by tiebreak).
type ConflictEntry struct {
	CanonicalID string              `json:"canonical_id"`
	Revision    int                 `json:"revision"`
	DetectedAt  string              `json:"detected_at"`
	Duplicates  []ConflictDuplicate `json:"duplicates"`
}

// IndexEntry is the per-canonical_id pointer to the winning line.
type IndexEntry struct {
	CanonicalID      string `json:"canonical_id"`
	LatestRevision   int    `json:"latest_revision"`
	LatestLineOffset int64  `json:"latest_line_offset"`
	Deleted          bool   `json:"deleted"`
	UpdatedAt        string `json:"updated_at"`
	LineULID         string `json:"line_ulid"`
}

// index is the on-disk + in-memory cache. Persisted as index.json.
type index struct {
	SchemaVersion     int                              `json:"schema_version"`
	SourceFingerprint string                           `json:"source_fingerprint"`
	LastLineCount     int                              `json:"last_line_count"`
	BuiltAt           string                           `json:"built_at"`
	Entries           map[string]IndexEntry            `json:"entries"`
	ByProvider        map[string]map[string]string     `json:"by_provider,omitempty"`
	SyncState         map[string]ProviderSyncState     `json:"sync_state,omitempty"`
	Conflicts         []ConflictEntry                  `json:"conflicts,omitempty"`
}

// loadIndex reads and deserializes index.json from path.
func loadIndex(path string) (*index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("index: read %s: %w", path, err)
	}
	var idx index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("index: parse %s: %w", path, ErrCorrupt)
	}
	if idx.Entries == nil {
		idx.Entries = make(map[string]IndexEntry)
	}
	if idx.ByProvider == nil {
		idx.ByProvider = make(map[string]map[string]string)
	}
	if idx.SyncState == nil {
		idx.SyncState = make(map[string]ProviderSyncState)
	}
	if idx.Conflicts == nil {
		idx.Conflicts = []ConflictEntry{}
	}
	return &idx, nil
}

// persist writes the index to path atomically (tmp + rename).
func (idx *index) persist(path string) error {
	return atomicWriteJSON(path, idx)
}

// computeFingerprint computes hex(xxh64(size_u64_le ++ first_4KB ++ last_4KB)).
// For files smaller than 4096 bytes the full content is used (no tail double-write).
func computeFingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("fingerprint: open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("fingerprint: stat %s: %w", path, err)
	}
	size := info.Size()

	const window = 4096
	head := make([]byte, min64(window, size))
	if _, err := io.ReadFull(f, head); err != nil && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("fingerprint: read head %s: %w", path, err)
	}

	var tail []byte
	if size > window {
		tailStart := size - window
		tail = make([]byte, window)
		if _, err := f.ReadAt(tail, tailStart); err != nil {
			return "", fmt.Errorf("fingerprint: read tail %s: %w", path, err)
		}
	}

	h := xxhash.New()
	var sizeBuf [8]byte
	binary.LittleEndian.PutUint64(sizeBuf[:], uint64(size))
	h.Write(sizeBuf[:])
	h.Write(head)
	if size > window {
		h.Write(tail)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// min64 returns the smaller of two int64 values.
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
