package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// writeJSONL writes a JSONL file from a slice of any values (one per line).
func writeJSONL(t *testing.T, path string, records []any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encode record: %v", err)
		}
	}
}

// makeRecord returns a minimal CanonicalRecord for test fixtures.
func makeRecord(id string, rev int, deleted bool) store.CanonicalRecord {
	return store.CanonicalRecord{
		CanonicalID:   id,
		SchemaVersion: 1,
		Kind:          "observation",
		Revision:      rev,
		Deleted:       deleted,
		Title:         "title-" + id,
		Content:       "content",
		ContentFormat: "markdown",
	}
}

func TestLoadRecords_CollapseKeepsHigherRevision(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	low := makeRecord("abc", 1, false)
	low.Title = "old title"
	high := makeRecord("abc", 2, false)
	high.Title = "new title"

	writeJSONL(t, filepath.Join(storeDir, "memory.jsonl"), []any{low, high})

	records, err := loadRecords(storeDir)
	if err != nil {
		t.Fatalf("loadRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Revision != 2 {
		t.Errorf("expected revision 2, got %d", records[0].Revision)
	}
	if records[0].Title != "new title" {
		t.Errorf("expected 'new title', got %q", records[0].Title)
	}
}

func TestLoadRecords_LowerRevisionFirst_StillKeepsHigher(t *testing.T) {
	// Same as above but high revision comes before low in the file.
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	high := makeRecord("xyz", 5, false)
	high.Title = "winner"
	low := makeRecord("xyz", 3, false)
	low.Title = "loser"

	writeJSONL(t, filepath.Join(storeDir, "memory.jsonl"), []any{high, low})

	records, err := loadRecords(storeDir)
	if err != nil {
		t.Fatalf("loadRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Revision != 5 {
		t.Errorf("expected revision 5, got %d", records[0].Revision)
	}
}

func TestLoadRecords_SkipsDeleted(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	live := makeRecord("live1", 1, false)
	dead := makeRecord("dead1", 1, true) // tombstone

	writeJSONL(t, filepath.Join(storeDir, "memory.jsonl"), []any{live, dead})

	records, err := loadRecords(storeDir)
	if err != nil {
		t.Fatalf("loadRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 live record, got %d", len(records))
	}
	if records[0].CanonicalID != "live1" {
		t.Errorf("expected live1, got %q", records[0].CanonicalID)
	}
}

func TestLoadRecords_SkipsBlankLines(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write raw file with blank lines interspersed.
	path := filepath.Join(storeDir, "memory.jsonl")
	rec := makeRecord("r1", 1, false)
	data, _ := json.Marshal(rec)
	content := "\n" + string(data) + "\n\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := loadRecords(storeDir)
	if err != nil {
		t.Fatalf("loadRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
}

func TestLoadRecords_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// memory.jsonl does not exist — should return empty slice, not error.
	records, err := loadRecords(storeDir)
	if err != nil {
		t.Fatalf("expected nil error for missing memory.jsonl, got: %v", err)
	}
	if records != nil && len(records) != 0 {
		t.Errorf("expected empty/nil slice, got %d records", len(records))
	}
}

func TestLoadRecords_SkipsFutureSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	good := makeRecord("good", 1, false)
	// Build a future-schema record inline.
	futureLine := []byte(`{"canonical_id":"future","schema_version":999,"revision":1,"kind":"observation","title":"x","content_format":"markdown"}`)

	goodData, _ := json.Marshal(good)
	content := string(goodData) + "\n" + string(futureLine) + "\n"
	if err := os.WriteFile(filepath.Join(storeDir, "memory.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := loadRecords(storeDir)
	if err != nil {
		t.Fatalf("loadRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record (future-schema skipped), got %d", len(records))
	}
	if records[0].CanonicalID != "good" {
		t.Errorf("expected 'good', got %q", records[0].CanonicalID)
	}
}

// TestLoadIndexFile_ConflictParsing checks that conflicts array is loaded.
func TestLoadIndexFile_ConflictParsing(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	idx := map[string]any{
		"schema_version": 1,
		"conflicts": []map[string]any{
			{
				"canonical_id": "c1",
				"revision":     2,
				"dup_count":    2,
				"detected_at":  "2026-01-01T00:00:00Z",
			},
		},
		"sync_state": map[string]any{},
	}
	data, _ := json.Marshal(idx)
	if err := os.WriteFile(filepath.Join(storeDir, "index.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := loadIndexFile(storeDir)
	if err != nil {
		t.Fatalf("loadIndexFile: %v", err)
	}
	if len(snap.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(snap.Conflicts))
	}
	if snap.Conflicts[0].CanonicalID != "c1" {
		t.Errorf("expected canonical_id c1, got %q", snap.Conflicts[0].CanonicalID)
	}
}

func TestLoadIndexFile_MissingFile(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No index.json — should return empty snapshot, not error.
	snap, err := loadIndexFile(storeDir)
	if err != nil {
		t.Fatalf("expected no error for missing index.json, got: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if len(snap.Conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(snap.Conflicts))
	}
}

// TestCallStatusJSON_InjectFakeRunner verifies the data layer calls the runner
// with the correct argv and parses the JSON response without spawning a real
// process (guarding against the circular-execution risk documented in tasks).
func TestCallStatusJSON_InjectFakeRunner(t *testing.T) {
	fakeOutput := []byte(`{
		"provider_health": {"engram": ""},
		"conflicts": [],
		"sync_state": {},
		"line_count": 42,
		"file_size_bytes": 1024,
		"schema_version": 1
	}`)

	var capturedName string
	var capturedArgs []string

	dl := &dataLayer{
		runner: func(name string, args ...string) ([]byte, error) {
			capturedName = name
			capturedArgs = args
			return fakeOutput, nil
		},
	}

	result, err := dl.callStatusJSON("/tmp/testdir")
	if err != nil {
		t.Fatalf("callStatusJSON: %v", err)
	}

	// Verify argv construction.
	if capturedName == "" {
		t.Error("expected runner to be called")
	}
	if len(capturedArgs) < 4 {
		t.Fatalf("expected at least 4 args, got %v", capturedArgs)
	}
	if capturedArgs[0] != "--dir" {
		t.Errorf("expected --dir flag, got %q", capturedArgs[0])
	}
	if capturedArgs[1] != "/tmp/testdir" {
		t.Errorf("expected /tmp/testdir, got %q", capturedArgs[1])
	}
	if capturedArgs[2] != "status" {
		t.Errorf("expected 'status' subcommand, got %q", capturedArgs[2])
	}
	if capturedArgs[3] != "--json" {
		t.Errorf("expected --json flag, got %q", capturedArgs[3])
	}

	// Verify parsed result.
	if result.LineCount != 42 {
		t.Errorf("expected LineCount 42, got %d", result.LineCount)
	}
	if result.FileSizeBytes != 1024 {
		t.Errorf("expected FileSizeBytes 1024, got %d", result.FileSizeBytes)
	}
}
