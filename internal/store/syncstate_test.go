package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTempStore opens a fresh Store in a temp directory for use in tests.
func openTempStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// --- UpdateSyncState ---

func TestUpdateSyncState_SetAndGet(t *testing.T) {
	s := openTempStore(t)

	st := ProviderSyncState{
		LastPulledAt:  "2026-01-01T00:00:00Z",
		LastPushedAt:  "2026-01-02T00:00:00Z",
		RecordsPulled: 5,
		RecordsPushed: 3,
	}
	require.NoError(t, s.UpdateSyncState("engram", st))

	got, ok := s.SyncState("engram")
	require.True(t, ok)
	assert.Equal(t, st, got)
}

func TestUpdateSyncState_IdempotentOverwrite(t *testing.T) {
	s := openTempStore(t)

	st1 := ProviderSyncState{LastPushedAt: "2026-01-01T00:00:00Z", RecordsPushed: 1}
	require.NoError(t, s.UpdateSyncState("engram", st1))

	st2 := ProviderSyncState{LastPushedAt: "2026-01-03T00:00:00Z", RecordsPushed: 10}
	require.NoError(t, s.UpdateSyncState("engram", st2))

	got, ok := s.SyncState("engram")
	require.True(t, ok)
	assert.Equal(t, st2, got, "second write must overwrite first")
}

func TestUpdateSyncState_MultipleProviders(t *testing.T) {
	s := openTempStore(t)

	stA := ProviderSyncState{LastPushedAt: "2026-01-01T00:00:00Z"}
	stB := ProviderSyncState{LastPulledAt: "2026-01-02T00:00:00Z"}
	require.NoError(t, s.UpdateSyncState("engram", stA))
	require.NoError(t, s.UpdateSyncState("claude-mem", stB))

	gotA, okA := s.SyncState("engram")
	require.True(t, okA)
	assert.Equal(t, stA, gotA)

	gotB, okB := s.SyncState("claude-mem")
	require.True(t, okB)
	assert.Equal(t, stB, gotB)
}

func TestUpdateSyncState_PersistedAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	st := ProviderSyncState{LastPushedAt: "2026-05-01T12:00:00Z", RecordsPushed: 7}

	s1, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, s1.UpdateSyncState("engram", st))
	require.NoError(t, s1.Close())

	s2, err := Open(dir)
	require.NoError(t, err)
	defer s2.Close()

	got, ok := s2.SyncState("engram")
	require.True(t, ok)
	assert.Equal(t, st, got)
}

func TestSyncState_MissingProvider_ReturnsFalse(t *testing.T) {
	s := openTempStore(t)
	_, ok := s.SyncState("unknown-provider")
	assert.False(t, ok)
}

// --- AppendConflict ---

func makeConflict(canonicalID string, revision int) ConflictEntry {
	return ConflictEntry{
		CanonicalID: canonicalID,
		Revision:    revision,
		DetectedAt:  time.Now().UTC().Format(time.RFC3339),
		Duplicates: []ConflictDuplicate{
			{LineOffset: 0, LineULID: "01AAAAAAAAAAAAAAAAAAAAAAAA", UpdatedAt: "2026-01-01T00:00:00Z", Provider: "engram"},
			{LineOffset: 100, LineULID: "01BBBBBBBBBBBBBBBBBBBBBBBB", UpdatedAt: "2026-01-01T00:00:00Z", Provider: "claude-mem"},
		},
	}
}

func TestAppendConflict_AddsEntry(t *testing.T) {
	s := openTempStore(t)

	c := makeConflict("01AAAAAAAAAAAAAAAAAAAAAACID", 3)
	require.NoError(t, s.AppendConflict(c))

	conflicts := s.Conflicts()
	require.Len(t, conflicts, 1)
	assert.Equal(t, c.CanonicalID, conflicts[0].CanonicalID)
	assert.Equal(t, c.Revision, conflicts[0].Revision)
}

func TestAppendConflict_DedupeByCanonicalIDAndRevision(t *testing.T) {
	s := openTempStore(t)

	c1 := makeConflict("01AAAAAAAAAAAAAAAAAAAAAACID", 3)
	c1.Duplicates[0].Provider = "original"
	require.NoError(t, s.AppendConflict(c1))

	// Second append for same (canonical_id, revision) must replace, not append.
	c2 := makeConflict("01AAAAAAAAAAAAAAAAAAAAAACID", 3)
	c2.Duplicates[0].Provider = "replaced"
	require.NoError(t, s.AppendConflict(c2))

	conflicts := s.Conflicts()
	require.Len(t, conflicts, 1, "dedupe must keep only one entry per (cid, revision)")
	assert.Equal(t, "replaced", conflicts[0].Duplicates[0].Provider)
}

func TestAppendConflict_DifferentRevisionKeptSeparate(t *testing.T) {
	s := openTempStore(t)

	require.NoError(t, s.AppendConflict(makeConflict("01AAAAAAAAAAAAAAAAAAAAAACID", 1)))
	require.NoError(t, s.AppendConflict(makeConflict("01AAAAAAAAAAAAAAAAAAAAAACID", 2)))

	assert.Len(t, s.Conflicts(), 2)
}

func TestAppendConflict_AutoSetsDetectedAt(t *testing.T) {
	s := openTempStore(t)

	c := ConflictEntry{
		CanonicalID: "01AAAAAAAAAAAAAAAAAAAAAACID",
		Revision:    1,
		// DetectedAt intentionally empty.
		Duplicates: []ConflictDuplicate{
			{LineOffset: 0, LineULID: "01AAAAAAAAAAAAAAAAAAAAAAAA"},
		},
	}
	require.NoError(t, s.AppendConflict(c))

	conflicts := s.Conflicts()
	require.Len(t, conflicts, 1)
	assert.NotEmpty(t, conflicts[0].DetectedAt, "DetectedAt must be auto-populated")
}

func TestAppendConflict_PersistedAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	c := makeConflict("01AAAAAAAAAAAAAAAAAAAAAACID", 5)

	s1, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, s1.AppendConflict(c))
	require.NoError(t, s1.Close())

	s2, err := Open(dir)
	require.NoError(t, err)
	defer s2.Close()

	conflicts := s2.Conflicts()
	require.Len(t, conflicts, 1)
	assert.Equal(t, c.CanonicalID, conflicts[0].CanonicalID)
}

// --- RemoveConflict ---

func TestRemoveConflict_RemovesCorrectEntry(t *testing.T) {
	s := openTempStore(t)

	cidA := "01AAAAAAAAAAAAAAAAAAAAAACDA"
	cidB := "01AAAAAAAAAAAAAAAAAAAAAACDB"
	require.NoError(t, s.AppendConflict(makeConflict(cidA, 1)))
	require.NoError(t, s.AppendConflict(makeConflict(cidB, 1)))

	require.NoError(t, s.RemoveConflict(cidA))

	conflicts := s.Conflicts()
	require.Len(t, conflicts, 1)
	assert.Equal(t, cidB, conflicts[0].CanonicalID)
}

func TestRemoveConflict_NoOpOnMissing(t *testing.T) {
	s := openTempStore(t)

	// No conflicts — remove on unknown ID must return nil.
	err := s.RemoveConflict("01AAAAAAAAAAAAAAAAAAAAAACID")
	assert.NoError(t, err)
	assert.Nil(t, s.Conflicts())
}

func TestRemoveConflict_PersistedAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	cid := "01AAAAAAAAAAAAAAAAAAAAAACID"

	s1, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, s1.AppendConflict(makeConflict(cid, 1)))
	require.NoError(t, s1.RemoveConflict(cid))
	require.NoError(t, s1.Close())

	s2, err := Open(dir)
	require.NoError(t, err)
	defer s2.Close()

	assert.Nil(t, s2.Conflicts(), "removed conflict must not reappear after reopen")
}

// --- Conflicts accessor ---

func TestConflicts_EmptyStore_ReturnsNil(t *testing.T) {
	s := openTempStore(t)
	assert.Nil(t, s.Conflicts())
}

func TestConflicts_ReturnsCopy(t *testing.T) {
	s := openTempStore(t)
	c := makeConflict("01AAAAAAAAAAAAAAAAAAAAAACID", 1)
	require.NoError(t, s.AppendConflict(c))

	got := s.Conflicts()
	require.Len(t, got, 1)

	// Mutating the returned slice must not affect the internal state.
	got[0].CanonicalID = "mutated"
	assert.Equal(t, c.CanonicalID, s.Conflicts()[0].CanonicalID)
}
