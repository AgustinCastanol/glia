package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadIndex_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	idx := &index{
		SchemaVersion:     1,
		SourceFingerprint: "abc123",
		LastLineCount:     3,
		BuiltAt:           "2024-01-01T00:00:00Z",
		Entries: map[string]IndexEntry{
			"AAAA": {
				CanonicalID:      "AAAA",
				LatestRevision:   2,
				LatestLineOffset: 100,
				Deleted:          false,
				UpdatedAt:        "2024-01-01T00:00:00Z",
				LineULID:         "BBBB",
			},
		},
	}

	err := idx.persist(path)
	require.NoError(t, err)

	loaded, err := loadIndex(path)
	require.NoError(t, err)

	assert.Equal(t, idx.SchemaVersion, loaded.SchemaVersion)
	assert.Equal(t, idx.SourceFingerprint, loaded.SourceFingerprint)
	assert.Equal(t, idx.LastLineCount, loaded.LastLineCount)
	assert.Equal(t, idx.Entries["AAAA"], loaded.Entries["AAAA"])
}

func TestLoadIndex_MissingFile(t *testing.T) {
	_, err := loadIndex("/nonexistent/path/index.json")
	assert.Error(t, err)
}

func TestLoadIndex_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	require.NoError(t, os.WriteFile(path, []byte("not json {"), 0644))

	_, err := loadIndex(path)
	assert.Error(t, err)
}

func TestLoadIndex_NilEntriesInitialized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	// Write an index with null entries field.
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":1,"entries":null}`), 0644))

	idx, err := loadIndex(path)
	require.NoError(t, err)
	assert.NotNil(t, idx.Entries)
}

func TestPersist_ProducesValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	idx := &index{
		SchemaVersion:     1,
		SourceFingerprint: "deadbeef",
		LastLineCount:     0,
		BuiltAt:           "2024-01-01T00:00:00Z",
		Entries:           map[string]IndexEntry{},
	}
	err := idx.persist(path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"schema_version":1`)
	assert.Contains(t, string(data), `"source_fingerprint":"deadbeef"`)
}

func TestComputeFingerprint_SameInputSameOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	content := []byte(`{"canonical_id":"X","line_ulid":"Y"}` + "\n")
	require.NoError(t, os.WriteFile(path, content, 0644))

	fp1, err := computeFingerprint(path)
	require.NoError(t, err)

	fp2, err := computeFingerprint(path)
	require.NoError(t, err)

	assert.Equal(t, fp1, fp2)
	assert.NotEmpty(t, fp1)
}

func TestComputeFingerprint_DetectsAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	content := []byte(`{"canonical_id":"X","line_ulid":"Y"}` + "\n")
	require.NoError(t, os.WriteFile(path, content, 0644))

	fp1, err := computeFingerprint(path)
	require.NoError(t, err)

	// Append more bytes.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"canonical_id":"Z","line_ulid":"W"}` + "\n")
	require.NoError(t, err)
	f.Close()

	fp2, err := computeFingerprint(path)
	require.NoError(t, err)

	assert.NotEqual(t, fp1, fp2)
}

func TestComputeFingerprint_ShortFileLessThan4096(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.jsonl")
	// 10 bytes — well under 4096.
	require.NoError(t, os.WriteFile(path, []byte("hello\nworld"), 0644))

	fp, err := computeFingerprint(path)
	require.NoError(t, err)
	assert.NotEmpty(t, fp)
	assert.Len(t, fp, 16) // xxh64 = 8 bytes = 16 hex chars
}

func TestComputeFingerprint_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(path, []byte{}, 0644))

	fp, err := computeFingerprint(path)
	require.NoError(t, err)
	assert.NotEmpty(t, fp)
}
