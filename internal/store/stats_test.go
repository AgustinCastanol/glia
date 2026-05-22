package store

import (
	"os"
	"path/filepath"
	"testing"
)

// openAndPopulate opens (or creates) a store at dir and appends records,
// closing it cleanly so the index is persisted.
func openAndPopulate(t *testing.T, dir string, records []CanonicalRecord) {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("openAndPopulate: open: %v", err)
	}
	for _, r := range records {
		if _, err := s.Append(r); err != nil {
			s.Close()
			t.Fatalf("openAndPopulate: append: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("openAndPopulate: close: %v", err)
	}
}

// TestStats_AllThreeFields verifies that Stats returns LineCount, FileSizeBytes,
// and SchemaVersion from a freshly-populated store directory.
func TestStats_AllThreeFields(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")

	records := []CanonicalRecord{
		{Kind: "observation", Title: "alpha", Type: "note", ContentFormat: "markdown"},
		{Kind: "observation", Title: "beta", Type: "note", ContentFormat: "markdown"},
	}
	openAndPopulate(t, storeDir, records)

	st, err := Stats(storeDir)
	if err != nil {
		t.Fatalf("Stats: unexpected error: %v", err)
	}
	// st is of type StoreStats; all fields are value types.
	if st.LineCount != len(records) {
		t.Errorf("LineCount: want %d, got %d", len(records), st.LineCount)
	}
	if st.FileSizeBytes <= 0 {
		t.Errorf("FileSizeBytes: want > 0, got %d", st.FileSizeBytes)
	}
	if st.SchemaVersion != StoreSupportedVersion {
		t.Errorf("SchemaVersion: want %d, got %d", StoreSupportedVersion, st.SchemaVersion)
	}
}

// TestStats_ReflectsAppend verifies that Stats after an append reflects the
// updated FileSizeBytes (REQ-TUI-03 scenario: stats after append).
func TestStats_ReflectsAppend(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")

	openAndPopulate(t, storeDir, []CanonicalRecord{
		{Kind: "observation", Title: "first", Type: "note", ContentFormat: "markdown"},
	})

	before, err := Stats(storeDir)
	if err != nil {
		t.Fatalf("Stats (before): %v", err)
	}

	// Append another record.
	openAndPopulate(t, storeDir, []CanonicalRecord{
		{Kind: "observation", Title: "second", Type: "note", ContentFormat: "markdown"},
	})

	after, err := Stats(storeDir)
	if err != nil {
		t.Fatalf("Stats (after): %v", err)
	}

	if after.LineCount <= before.LineCount {
		t.Errorf("LineCount should grow: before=%d after=%d", before.LineCount, after.LineCount)
	}
	if after.FileSizeBytes <= before.FileSizeBytes {
		t.Errorf("FileSizeBytes should grow: before=%d after=%d", before.FileSizeBytes, after.FileSizeBytes)
	}
}

// TestStats_MissingMemoryFile verifies that Stats succeeds when memory.jsonl
// does not exist yet (empty store bootstrap case).
func TestStats_MissingMemoryFile(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")

	// Create the store (which bootstraps index.json + schema.json) then
	// immediately delete memory.jsonl to simulate a bare-index scenario.
	openAndPopulate(t, storeDir, nil)

	if err := os.Remove(filepath.Join(storeDir, memoryFilename)); err != nil {
		t.Fatalf("remove memory.jsonl: %v", err)
	}

	st, err := Stats(storeDir)
	if err != nil {
		t.Fatalf("Stats with missing memory.jsonl: unexpected error: %v", err)
	}
	if st.FileSizeBytes != 0 {
		t.Errorf("FileSizeBytes: want 0 for missing file, got %d", st.FileSizeBytes)
	}
}

// TestStats_MissingIndex verifies that Stats returns an error when index.json
// is absent (uninitialized directory).
func TestStats_MissingIndex(t *testing.T) {
	dir := t.TempDir()
	_, err := Stats(dir) // no store initialised
	if err == nil {
		t.Error("Stats on uninitialised dir: expected error, got nil")
	}
}
