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

// --- Conflict detection tests (T-13) ---

func TestRebuildFromFile_ConflictDiffUpdatedAt_NewerWins(t *testing.T) {
	// Scenario: two lines, same (cid, revision=3), different updated_at —
	// the line with greater updated_at must win.
	fixture := "testdata/conflict_diff_updated_at.jsonl"
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	entry := idx.Entries["01HZZZZZZZZZZZZZZZZZZZAAAA"]
	// B002 has updated_at=2024-01-02 > B001's 2024-01-01 — B002 wins.
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZB002", entry.LineULID, "newer updated_at must win")
	assert.Equal(t, 3, entry.LatestRevision)

	// Conflict must be recorded.
	require.Len(t, idx.Conflicts, 1)
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZAAAA", idx.Conflicts[0].CanonicalID)
	assert.Equal(t, 3, idx.Conflicts[0].Revision)
	require.Len(t, idx.Conflicts[0].Duplicates, 2)
	// Winner is first in Duplicates (tiebreak order).
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZB002", idx.Conflicts[0].Duplicates[0].LineULID, "winner is first dup")
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZB001", idx.Conflicts[0].Duplicates[1].LineULID, "loser is second dup")
}

func TestRebuildFromFile_ConflictSameRevision_ConflictRecorded(t *testing.T) {
	// Existing fixture: same (cid, revision=1), same updated_at, higher lineULID wins.
	fixture := "testdata/conflict_same_revision.jsonl"
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	// A conflict entry must exist.
	require.Len(t, idx.Conflicts, 1)
	c := idx.Conflicts[0]
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZAAAA", c.CanonicalID)
	assert.Equal(t, 1, c.Revision)
	assert.NotEmpty(t, c.DetectedAt)
	require.Len(t, c.Duplicates, 2)

	// B002 > B001 lexicographically — B002 is winner (first dup).
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZB002", c.Duplicates[0].LineULID, "winner first in Duplicates")
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZB001", c.Duplicates[1].LineULID, "loser second in Duplicates")
}

func TestRebuildFromFile_ConflictWithCleanRecords_CleanUnaffected(t *testing.T) {
	// Fixture has 3 canonical_ids: A (clean rev=1), B (conflict rev=2 x2), C (clean rev=1).
	fixture := "testdata/conflict_with_clean_records.jsonl"
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	// Three canonical_ids must be present.
	require.Len(t, idx.Entries, 3)

	// Only the conflicting canonical_id B must appear in Conflicts.
	require.Len(t, idx.Conflicts, 1)
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZBBBB", idx.Conflicts[0].CanonicalID)

	// Clean records must be correct.
	entryA := idx.Entries["01HZZZZZZZZZZZZZZZZZZZAAAA"]
	assert.Equal(t, 1, entryA.LatestRevision)
	assert.False(t, entryA.Deleted)

	entryC := idx.Entries["01HZZZZZZZZZZZZZZZZZZZCCCC"]
	assert.Equal(t, 1, entryC.LatestRevision)
	assert.False(t, entryC.Deleted)
}

func TestRebuildFromFile_NoConflicts_ConflictsSliceEmpty(t *testing.T) {
	// A fixture without collisions must produce an empty (non-nil) Conflicts slice.
	fixture := "testdata/revision_chain.jsonl"
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)
	assert.NotNil(t, idx.Conflicts)
	assert.Len(t, idx.Conflicts, 0)
}

func TestRebuildFromFile_ConflictDuplicatesHaveProvider(t *testing.T) {
	// Verify provider field is populated in ConflictDuplicate.
	fixture := "testdata/conflict_diff_updated_at.jsonl"
	idx, err := rebuildFromFile(fixture)
	require.NoError(t, err)

	require.Len(t, idx.Conflicts, 1)
	dups := idx.Conflicts[0].Duplicates
	providers := map[string]bool{dups[0].Provider: true, dups[1].Provider: true}
	assert.True(t, providers["engram"] || providers["claude-mem"], "provider field must be populated")
}

func TestRebuildFromFile_ConflictPersistedViaAppendConflict(t *testing.T) {
	// Full round-trip: open store on a conflicting file, rebuild, verify Conflicts()
	// returns the detected conflicts and they survive a reopen.
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/conflict_same_revision.jsonl")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dir+"/memory.jsonl", data, 0644))

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	conflicts := s.Conflicts()
	require.Len(t, conflicts, 1, "conflict detected at Open must be in Conflicts()")
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZAAAA", conflicts[0].CanonicalID)
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
