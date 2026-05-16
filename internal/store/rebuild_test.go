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

// S-09: ByProvider survives a stale-index rebuild (regression).
// This test MUST NOT be guarded by testing.Short() — no binary is required.
func TestByProvider_SurvivesStaleIndexRebuild(t *testing.T) {
	dir := t.TempDir()
	memPath := filepath.Join(dir, memoryFilename)
	idxPath := filepath.Join(dir, indexFilename)

	// Canonical IDs used in the fixture.
	const (
		canonA = "01JVZZZZZZZZZZZZZZZZZZCANA"
		canonB = "01JVZZZZZZZZZZZZZZZZZZCAB"
	)

	// Build JSONL with two engram-origin records, followed by a tombstone for B.
	// Record A: live, provider=engram, provider_id=obs-A
	// Record B: live initial revision, then tombstoned.
	lines := []string{
		`{"canonical_id":"` + canonA + `","line_ulid":"01JVZZZZZZZZZZZZZZZZZZA001","schema_version":1,"kind":"observation","revision":1,"supersedes":"","deleted":false,"title":"A","content":"content A","content_format":"markdown","origin":{"provider":"engram","provider_id":"obs-A","author":"test","session_id":"s1"},"created_at":"2026-05-16T00:00:00.000000000Z","updated_at":"2026-05-16T00:00:00.000000000Z","tags":[],"topic_key":"","type":"manual"}`,
		`{"canonical_id":"` + canonB + `","line_ulid":"01JVZZZZZZZZZZZZZZZZZZA002","schema_version":1,"kind":"observation","revision":1,"supersedes":"","deleted":false,"title":"B","content":"content B","content_format":"markdown","origin":{"provider":"engram","provider_id":"obs-B","author":"test","session_id":"s1"},"created_at":"2026-05-16T00:00:01.000000000Z","updated_at":"2026-05-16T00:00:01.000000000Z","tags":[],"topic_key":"","type":"manual"}`,
		`{"canonical_id":"` + canonB + `","line_ulid":"01JVZZZZZZZZZZZZZZZZZZA003","schema_version":1,"kind":"observation","revision":2,"supersedes":"` + canonB + `","deleted":true,"title":"","content":"","content_format":"","origin":{"provider":"engram","provider_id":"obs-B","author":"test","session_id":"s1"},"created_at":"2026-05-16T00:00:01.000000000Z","updated_at":"2026-05-16T00:00:02.000000000Z","tags":[],"topic_key":"","type":""}`,
	}

	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	require.NoError(t, os.WriteFile(memPath, []byte(content), 0644))

	// Ensure no index.json exists — forces a full rebuild on Open.
	// (It may not exist yet; ignore the error.)
	_ = os.Remove(idxPath)

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	idmap := s.ProviderIDMap("engram")
	require.NotNil(t, idmap)

	t.Run("live_record_A_present", func(t *testing.T) {
		got, ok := idmap.CanonicalFromNative("obs-A")
		assert.True(t, ok, "obs-A should be in ByProvider")
		assert.Equal(t, canonA, got)
	})

	t.Run("tombstoned_record_B_absent", func(t *testing.T) {
		_, ok := idmap.CanonicalFromNative("obs-B")
		assert.False(t, ok, "obs-B should be absent — tombstone must win")
	})

	t.Run("reverse_lookup_A_works", func(t *testing.T) {
		got, ok := idmap.NativeFromCanonical(canonA)
		assert.True(t, ok)
		assert.Equal(t, "obs-A", got)
	})
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
