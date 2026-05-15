package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_FullLifecycle exercises the complete store lifecycle:
// Open → Append → Close → reopen → ReadLive → Tombstone → ReadLive excludes.
// It also verifies acceptance criterion §14.8: line_ulid present on every line.
func TestIntegration_FullLifecycle(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// -----------------------------------------------------------------------
	// Step 1: Open → Append two records → Close
	// -----------------------------------------------------------------------
	s1, err := Open(dir)
	require.NoError(t, err)

	r1, err := s1.Append(CanonicalRecord{
		Kind:          "observation",
		Title:         "first",
		Content:       "content1",
		ContentFormat: "markdown",
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags:          []string{"a"},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, r1.Revision)
	assert.NotEmpty(t, r1.CanonicalID)
	assert.NotEmpty(t, r1.LineULID)
	assert.NotEqual(t, r1.CanonicalID, r1.LineULID)

	r2, err := s1.Append(CanonicalRecord{
		Kind:          "session_summary",
		Title:         "second",
		Content:       "content2",
		ContentFormat: "text",
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags:          []string{},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, r2.Revision)

	require.NoError(t, s1.Close())

	// -----------------------------------------------------------------------
	// Step 2: Reopen → verify lock was released and data is intact
	// -----------------------------------------------------------------------
	s2, err := Open(dir)
	require.NoError(t, err)

	live1, err := s2.ReadLive(r1.CanonicalID)
	require.NoError(t, err)
	assert.Equal(t, r1.CanonicalID, live1.CanonicalID)
	assert.Equal(t, 1, live1.Revision)
	assert.Equal(t, "first", live1.Title)

	live2, err := s2.ReadLive(r2.CanonicalID)
	require.NoError(t, err)
	assert.Equal(t, "second", live2.Title)

	// -----------------------------------------------------------------------
	// Step 3: Update r1 (revision 2)
	// -----------------------------------------------------------------------
	r1v2, err := s2.Append(CanonicalRecord{
		CanonicalID:   r1.CanonicalID,
		Kind:          "observation",
		Title:         "first updated",
		Content:       "content1-v2",
		ContentFormat: "markdown",
		CreatedAt:     r1.CreatedAt,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Tags:          []string{"a", "b"},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, r1v2.Revision)
	assert.Equal(t, r1.CanonicalID, r1v2.Supersedes)

	// ReadLive sees revision 2.
	require.NoError(t, s2.w.Flush())
	liveUpdated, err := s2.ReadLive(r1.CanonicalID)
	require.NoError(t, err)
	assert.Equal(t, 2, liveUpdated.Revision)
	assert.Equal(t, "first updated", liveUpdated.Title)

	// -----------------------------------------------------------------------
	// Step 4: Tombstone r1
	// -----------------------------------------------------------------------
	tomb, err := s2.Append(CanonicalRecord{
		CanonicalID: r1.CanonicalID,
		Kind:        "observation",
		Deleted:     true,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		CreatedAt:   r1.CreatedAt,
		Tags:        []string{},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, tomb.Revision)
	assert.Equal(t, r1.CanonicalID, tomb.Supersedes)
	assert.True(t, tomb.Deleted)

	// ReadLive(r1) now returns ErrDeleted.
	require.NoError(t, s2.w.Flush())
	_, err = s2.ReadLive(r1.CanonicalID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeleted)

	// ReadAll(r1) returns all 3 lines (rev1, rev2, tombstone).
	all, err := s2.ReadAll(r1.CanonicalID)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	// -----------------------------------------------------------------------
	// Step 5: Rebuild → index consistency
	// -----------------------------------------------------------------------
	require.NoError(t, s2.Rebuild())

	// After rebuild, the tombstone still reflects in the index.
	_, err = s2.ReadLive(r1.CanonicalID)
	assert.ErrorIs(t, err, ErrDeleted)

	// r2 is still live.
	_, err = s2.ReadLive(r2.CanonicalID)
	require.NoError(t, err)

	require.NoError(t, s2.Close())

	// -----------------------------------------------------------------------
	// Step 6: Verify acceptance criterion §14.8 — line_ulid on every raw line
	// -----------------------------------------------------------------------
	memPath := filepath.Join(dir, memoryFilename)
	data, err := os.ReadFile(memPath)
	require.NoError(t, err)

	rawLines := splitLines(data)
	// We wrote: r1(rev1), r2(rev1), r1(rev2), tomb — total 4 lines.
	require.Len(t, rawLines, 4, "expected 4 lines in memory.jsonl")

	for i, line := range rawLines {
		var obj map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(line, &obj), "line %d must be valid JSON", i)
		rawULID, ok := obj["line_ulid"]
		require.True(t, ok, "line %d missing line_ulid", i)
		var ulidVal string
		require.NoError(t, json.Unmarshal(rawULID, &ulidVal))
		assert.NotEmpty(t, ulidVal, "line %d line_ulid must not be empty", i)
	}
}

// TestIntegration_AppendBatch_ThenReadAll verifies batch write + ReadAll after Close.
func TestIntegration_AppendBatch_ThenReadAll(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s, err := Open(dir)
	require.NoError(t, err)

	// Append one record to get a canonical_id.
	first, err := s.Append(CanonicalRecord{
		Kind:          "observation",
		Title:         "first",
		Content:       "c",
		ContentFormat: "text",
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags:          []string{},
	})
	require.NoError(t, err)

	// Use AppendBatch to write two revisions of first + a new record.
	results, err := s.AppendBatch([]CanonicalRecord{
		{
			CanonicalID:   first.CanonicalID,
			Kind:          "observation",
			Title:         "first-v2",
			Content:       "c2",
			ContentFormat: "text",
			CreatedAt:     now,
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
			Tags:          []string{},
		},
		{
			Kind:          "observation",
			Title:         "second",
			Content:       "c",
			ContentFormat: "text",
			CreatedAt:     now,
			UpdatedAt:     now,
			Tags:          []string{},
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, 2, results[0].Revision)

	require.NoError(t, s.Close())

	// Reopen and verify.
	s2, err := Open(dir)
	require.NoError(t, err)
	defer s2.Close()

	live, err := s2.ReadLive(first.CanonicalID)
	require.NoError(t, err)
	assert.Equal(t, 2, live.Revision)
	assert.Equal(t, "first-v2", live.Title)

	allFirst, err := s2.ReadAll(first.CanonicalID)
	require.NoError(t, err)
	assert.Len(t, allFirst, 2)

	live2, err := s2.ReadLive(results[1].CanonicalID)
	require.NoError(t, err)
	assert.Equal(t, "second", live2.Title)
}
