package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTempStore opens a Store in a fresh t.TempDir(). Registers cleanup.
func newTempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// copyFixture copies testdata/<name> into dir/<destName>.
func copyFixture(t *testing.T, name, dir, destName string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, destName), data, 0644))
}

// TestScenarioA_FirstRunBootstrap verifies that Open on a non-existent dir
// creates all expected files and returns a valid Store.
func TestScenarioA_FirstRunBootstrap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-store")

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	// rootDir was created.
	_, err = os.Stat(dir)
	require.NoError(t, err)

	// schema.json exists.
	_, err = os.Stat(filepath.Join(dir, schemaFilename))
	assert.NoError(t, err)

	// memory.jsonl exists.
	_, err = os.Stat(filepath.Join(dir, memoryFilename))
	assert.NoError(t, err)

	// index.json exists.
	_, err = os.Stat(filepath.Join(dir, indexFilename))
	assert.NoError(t, err)

	// .lock exists.
	_, err = os.Stat(filepath.Join(dir, lockFilename))
	assert.NoError(t, err)

	// Index has no entries on a fresh store.
	assert.Empty(t, s.idx.Entries)
}

// TestScenarioF_DoubleLock verifies that a second Open on the same dir
// while S1 holds the lock returns ErrLocked.
func TestScenarioF_DoubleLock(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	require.NoError(t, err)
	defer s1.Close()

	_, err = Open(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLocked)
}

// TestScenarioK_SchemaTooNew verifies that schema_version=9999 causes ErrSchemaTooNew.
func TestScenarioK_SchemaTooNew(t *testing.T) {
	dir := t.TempDir()

	// Copy the "too new" schema fixture into the store directory.
	copyFixture(t, "schema/schema_too_new.json", dir, schemaFilename)

	_, err := Open(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSchemaTooNew)
}

// TestScenarioG_RecoveryPartialLine verifies that Open truncates a partial
// trailing line and proceeds with a clean index.
func TestScenarioG_RecoveryPartialLine(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "partial_trailing_line.jsonl", dir, memoryFilename)

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	// memory.jsonl must now end with \n.
	data, err := os.ReadFile(filepath.Join(dir, memoryFilename))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	assert.Equal(t, byte('\n'), data[len(data)-1])
}

// TestScenarioH_RecoveryNoNewline verifies that Open truncates a file with
// no newlines anywhere to 0 bytes.
func TestScenarioH_RecoveryNoNewline(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "no_newline_anywhere.jsonl", dir, memoryFilename)

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	info, err := os.Stat(filepath.Join(dir, memoryFilename))
	require.NoError(t, err)
	assert.Equal(t, int64(0), info.Size())

	assert.Empty(t, s.idx.Entries)
}

// TestScenarioI_StaleFingerprint_RebuildOnReopen verifies that a fingerprint
// mismatch triggers a rebuild when the store is re-opened.
func TestScenarioI_StaleFingerprint_RebuildOnReopen(t *testing.T) {
	dir := t.TempDir()

	// Copy single_record as memory.jsonl and write a stale index.
	copyFixture(t, "single_record.jsonl", dir, memoryFilename)
	copyFixture(t, "index/stale_index.json", dir, indexFilename)
	copyFixture(t, "schema/schema_v1.json", dir, schemaFilename)

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	// Index should now reflect the actual memory.jsonl content.
	assert.Len(t, s.idx.Entries, 1)
	entry, ok := s.idx.Entries["01HZZZZZZZZZZZZZZZZZZZAAAA"]
	require.True(t, ok)
	assert.Equal(t, 1, entry.LatestRevision)
}

// TestScenarioP_CloseFlushesReleasesLock verifies that Close persists state
// and releases the lock so a subsequent Open succeeds.
func TestScenarioP_CloseFlushesReleasesLock(t *testing.T) {
	dir := t.TempDir()

	s1, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, s1.Close())

	// After Close, a second Open must succeed.
	s2, err := Open(dir)
	require.NoError(t, err)
	defer s2.Close()
}

// TestClose_Idempotent verifies that calling Close twice returns nil.
func TestClose_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)

	require.NoError(t, s.Close())
	require.NoError(t, s.Close()) // second call must be no-op
}

// TestReadLive_EmptyStore_ErrNotFound verifies ErrNotFound on an empty store.
func TestReadLive_EmptyStore_ErrNotFound(t *testing.T) {
	s := newTempStore(t)
	_, err := s.ReadLive("NONEXISTENT")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestReadAll_EmptyStore_ErrNotFound verifies ErrNotFound for ReadAll on unknown id.
func TestReadAll_EmptyStore_ErrNotFound(t *testing.T) {
	s := newTempStore(t)
	_, err := s.ReadAll("NONEXISTENT")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestReadLive_TombstonedEntry_ErrDeleted verifies that ReadLive returns
// ErrDeleted when the index has a deleted entry.
func TestReadLive_TombstonedEntry_ErrDeleted(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "tombstoned_chain.jsonl", dir, memoryFilename)
	copyFixture(t, "schema/schema_v1.json", dir, schemaFilename)

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	_, err = s.ReadLive("01HZZZZZZZZZZZZZZZZZZZAAAA")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeleted)
}

// TestReadAll_TombstonedChain_ReturnsAllRecords verifies that ReadAll returns
// all lines including the tombstone.
func TestReadAll_TombstonedChain_ReturnsAllRecords(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "tombstoned_chain.jsonl", dir, memoryFilename)
	copyFixture(t, "schema/schema_v1.json", dir, schemaFilename)

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	records, err := s.ReadAll("01HZZZZZZZZZZZZZZZZZZZAAAA")
	require.NoError(t, err)
	assert.Len(t, records, 2)
	assert.False(t, records[0].Deleted)
	assert.True(t, records[1].Deleted)
}

// TestRebuild_Idempotent verifies that calling Rebuild three times returns
// nil each time and the final index state is identical.
func TestRebuild_Idempotent(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "revision_chain.jsonl", dir, memoryFilename)
	copyFixture(t, "schema/schema_v1.json", dir, schemaFilename)

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, s.Rebuild())
	entries1 := s.idx.Entries

	require.NoError(t, s.Rebuild())
	entries2 := s.idx.Entries

	require.NoError(t, s.Rebuild())
	entries3 := s.idx.Entries

	assert.Equal(t, entries1, entries2)
	assert.Equal(t, entries2, entries3)
}

// TestReadLive_ThreeRecords_ReturnsAllFromIndex verifies ReadLive works for
// multiple independent canonical_ids loaded from a fixture.
func TestReadLive_ThreeRecords_ReturnsAllFromIndex(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "three_records.jsonl", dir, memoryFilename)
	copyFixture(t, "schema/schema_v1.json", dir, schemaFilename)

	s, err := Open(dir)
	require.NoError(t, err)
	defer s.Close()

	// three_records.jsonl has 3 independent canonical_ids; all should be live.
	assert.Len(t, s.idx.Entries, 3)
	for id, entry := range s.idx.Entries {
		assert.False(t, entry.Deleted, "entry %s should not be deleted", id)
		r, err := s.ReadLive(id)
		require.NoError(t, err)
		assert.Equal(t, id, r.CanonicalID)
	}
}

// TestScenarioO_ReadAll_UnknownCanonicalID verifies Scenario O.
func TestScenarioO_ReadAll_UnknownCanonicalID(t *testing.T) {
	s := newTempStore(t)
	_, err := s.ReadAll("01UNKNOWNID00000000000000X")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}
