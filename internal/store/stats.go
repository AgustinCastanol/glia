package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// StoreStats holds a point-in-time snapshot of key store metrics.
// All fields are derived from index.json and os.Stat — no advisory lock is
// acquired, making this safe to call while another process holds the store.
type StoreStats struct {
	// LineCount is the number of JSONL lines recorded in index.json at the
	// time the index was last persisted (LastLineCount field).
	LineCount int

	// FileSizeBytes is the current byte size of memory.jsonl as reported by
	// os.Stat. May be slightly ahead of LineCount if an append is in progress.
	FileSizeBytes int64

	// SchemaVersion is the schema_version stored in index.json.
	SchemaVersion int
}

// Stats reads index.json and stats memory.jsonl without acquiring the advisory
// lock. It is safe to call concurrently with another process that holds the
// store. If memory.jsonl does not yet exist, FileSizeBytes is 0.
func Stats(rootDir string) (StoreStats, error) {
	idxPath := filepath.Join(rootDir, indexFilename)
	idx, err := loadIndex(idxPath)
	if err != nil {
		return StoreStats{}, fmt.Errorf("store.Stats: load index: %w", err)
	}

	memPath := filepath.Join(rootDir, memoryFilename)
	fi, err := os.Stat(memPath)
	if err != nil && !os.IsNotExist(err) {
		return StoreStats{}, fmt.Errorf("store.Stats: stat memory file: %w", err)
	}

	var fileSize int64
	if fi != nil {
		fileSize = fi.Size()
	}

	return StoreStats{
		LineCount:     idx.LastLineCount,
		FileSizeBytes: fileSize,
		SchemaVersion: idx.SchemaVersion,
	}, nil
}
