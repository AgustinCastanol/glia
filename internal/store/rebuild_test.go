package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRebuildFromFile_SingleRecord(t *testing.T) {
	fixture := filepath.Join("testdata", "single_record.jsonl")
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	assert.Len(t, idx.Entries, 1)
	assert.Equal(t, 1, idx.LastLineCount)

	entry, ok := idx.Entries["01HZZZZZZZZZZZZZZZZZZZAAAA"]
	require.True(t, ok)
	assert.Equal(t, 1, entry.LatestRevision)
	assert.False(t, entry.Deleted)
}

func TestRebuildFromFile_RevisionChain_WinnerIsRev3(t *testing.T) {
	fixture := filepath.Join("testdata", "revision_chain.jsonl")
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	assert.Len(t, idx.Entries, 1)
	assert.Equal(t, 3, idx.LastLineCount)

	entry := idx.Entries["01HZZZZZZZZZZZZZZZZZZZAAAA"]
	assert.Equal(t, 3, entry.LatestRevision)
	assert.False(t, entry.Deleted)
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZB003", entry.LineULID)
}

func TestRebuildFromFile_ConflictSameRevision_HigherLineULIDWins(t *testing.T) {
	// Scenario J: two lines, same (cid, revision), same updated_at,
	// different line_ulid — higher lex line_ulid must win.
	fixture := filepath.Join("testdata", "conflict_same_revision.jsonl")
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	entry := idx.Entries["01HZZZZZZZZZZZZZZZZZZZAAAA"]
	// B002 > B001 lexicographically — B002 must win.
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZB002", entry.LineULID)
}

func TestRebuildFromFile_TombstonedChain_DeletedTrue(t *testing.T) {
	fixture := filepath.Join("testdata", "tombstoned_chain.jsonl")
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	entry := idx.Entries["01HZZZZZZZZZZZZZZZZZZZAAAA"]
	assert.True(t, entry.Deleted)
	assert.Equal(t, 2, entry.LatestRevision)
}

func TestRebuildFromFile_MixedSchemaVersions_UnknownSkipped(t *testing.T) {
	// Scenario L: 3 lines schema_version=1 + 1 line schema_version=99 — unknown skipped.
	fixture := filepath.Join("testdata", "mixed_schema_versions.jsonl")
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	// The v99 line should be skipped; only v1 lines count.
	assert.Equal(t, 3, idx.LastLineCount)
}

func TestRebuildFromFile_EmptyFile(t *testing.T) {
	fixture := filepath.Join("testdata", "empty_memory.jsonl")
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)
	assert.Len(t, idx.Entries, 0)
	assert.Equal(t, 0, idx.LastLineCount)
}

func TestRebuildIdempotent_ScenarioR(t *testing.T) {
	// Scenario R: rebuild three times in succession = same result.
	fixture := filepath.Join("testdata", "revision_chain.jsonl")

	idx1, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	idx2, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	idx3, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	assert.Equal(t, idx1.Entries, idx2.Entries)
	assert.Equal(t, idx2.Entries, idx3.Entries)
	assert.Equal(t, idx1.LastLineCount, idx2.LastLineCount)
}

func TestTiebreakWinner_RevisionPrimary(t *testing.T) {
	cands := []lineCandidate{
		{offset: 0, revision: 1, updatedAt: "2024-01-01T00:00:00Z", lineULID: "Z"},
		{offset: 100, revision: 3, updatedAt: "2024-01-01T00:00:00Z", lineULID: "A"},
		{offset: 200, revision: 2, updatedAt: "2024-01-01T00:00:00Z", lineULID: "B"},
	}
	winner := tiebreakWinner(cands)
	assert.Equal(t, 3, winner.revision)
}

func TestTiebreakWinner_UpdatedAtSecondary(t *testing.T) {
	cands := []lineCandidate{
		{offset: 0, revision: 1, updatedAt: "2024-01-01T00:00:00.000000001Z", lineULID: "A"},
		{offset: 100, revision: 1, updatedAt: "2024-01-01T00:00:00.000000002Z", lineULID: "B"},
	}
	winner := tiebreakWinner(cands)
	assert.Equal(t, "2024-01-01T00:00:00.000000002Z", winner.updatedAt)
}

func TestTiebreakWinner_LineULIDTertiary(t *testing.T) {
	cands := []lineCandidate{
		{offset: 0, revision: 1, updatedAt: "2024-01-01T00:00:00Z", lineULID: "01AAAAAAAAAAAAAAAAAAAAAAAA"},
		{offset: 100, revision: 1, updatedAt: "2024-01-01T00:00:00Z", lineULID: "01BBBBBBBBBBBBBBBBBBBBBBBB"},
	}
	winner := tiebreakWinner(cands)
	assert.Equal(t, "01BBBBBBBBBBBBBBBBBBBBBBBB", winner.lineULID)
}

func TestLoadOrRebuild_NoIndexFile_Rebuilds(t *testing.T) {
	dir := t.TempDir()
	// Copy single_record fixture as memory.jsonl.
	data, err := os.ReadFile(filepath.Join("testdata", "single_record.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, memoryFilename), data, 0644))

	idx, dirty, err := loadOrRebuild(dir)
	require.NoError(t, err)
	assert.True(t, dirty)
	assert.Len(t, idx.Entries, 1)
}

func TestLoadOrRebuild_ValidCache_CacheHit(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("testdata", "single_record.jsonl"))
	require.NoError(t, err)
	memPath := filepath.Join(dir, memoryFilename)
	require.NoError(t, os.WriteFile(memPath, data, 0644))

	// Build and persist index.
	idx1, _, err := loadOrRebuild(dir)
	require.NoError(t, err)
	require.NoError(t, idx1.persist(filepath.Join(dir, indexFilename)))

	// Second call should be a cache hit (dirty==false).
	idx2, dirty, err := loadOrRebuild(dir)
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, idx1.Entries, idx2.Entries)
}

func TestLoadOrRebuild_StaleFingerprint_Rebuilds(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("testdata", "single_record.jsonl"))
	require.NoError(t, err)
	memPath := filepath.Join(dir, memoryFilename)
	require.NoError(t, os.WriteFile(memPath, data, 0644))

	// Write a stale index (wrong fingerprint).
	stale, err := os.ReadFile(filepath.Join("testdata", "index", "stale_index.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, indexFilename), stale, 0644))

	idx, dirty, err := loadOrRebuild(dir)
	require.NoError(t, err)
	assert.True(t, dirty)
	assert.Len(t, idx.Entries, 1)
}
