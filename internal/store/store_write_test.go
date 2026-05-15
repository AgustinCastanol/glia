package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mockSyncer — wraps *os.File and counts Sync() calls for Scenario M.
// ---------------------------------------------------------------------------

type mockSyncer struct {
	*os.File
	syncCount atomic.Int64
}

func (m *mockSyncer) Sync() error {
	m.syncCount.Add(1)
	return m.File.Sync()
}

// injectMockSyncer replaces the store's syncer with a mockSyncer that shares
// the same file descriptor. Must be called before any writes happen.
func injectMockSyncer(t *testing.T, s *Store) *mockSyncer {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	real, ok := s.f.(*os.File)
	require.True(t, ok, "expected *os.File as syncer")
	mock := &mockSyncer{File: real}
	_ = s.w.Flush() // drain any existing buffer first
	s.f = mock
	s.w = bufio.NewWriterSize(mock, 64*1024)
	return mock
}

// ---------------------------------------------------------------------------
// Scenario B — Append new record, round-trip
// ---------------------------------------------------------------------------

func TestScenarioB_AppendNewRecord_RoundTrip(t *testing.T) {
	s := newTempStore(t)

	r := CanonicalRecord{
		Kind:          "observation",
		Title:         "Hello",
		Content:       "World",
		ContentFormat: "text",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Tags:          []string{},
	}

	got, err := s.Append(r)
	require.NoError(t, err)

	// Store-assigned fields.
	assert.NotEmpty(t, got.CanonicalID, "canonical_id must be assigned")
	assert.NotEmpty(t, got.LineULID, "line_ulid must be assigned")
	assert.NotEqual(t, got.CanonicalID, got.LineULID, "canonical_id and line_ulid must differ")
	assert.Equal(t, 1, got.Revision)
	assert.Equal(t, "", got.Supersedes)
	assert.False(t, got.Deleted)

	// Round-trip via ReadLive.
	// Need to flush first so the read sees the bytes.
	require.NoError(t, s.Close())

	// Reopen to force flush persistence.
	s2, err := Open(s.rootDir)
	require.NoError(t, err)
	defer s2.Close()

	live, err := s2.ReadLive(got.CanonicalID)
	require.NoError(t, err)
	assert.Equal(t, got.CanonicalID, live.CanonicalID)
	assert.Equal(t, got.LineULID, live.LineULID)
	assert.Equal(t, 1, live.Revision)
	assert.Equal(t, "Hello", live.Title)
}

// ---------------------------------------------------------------------------
// Scenario C — Revision update, ReadLive returns latest
// ---------------------------------------------------------------------------

func TestScenarioC_RevisionUpdate_ReadLiveLatest(t *testing.T) {
	s := newTempStore(t)

	r1 := CanonicalRecord{
		Kind:          "observation",
		Title:         "Original",
		Content:       "v1",
		ContentFormat: "text",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Tags:          []string{},
	}
	got1, err := s.Append(r1)
	require.NoError(t, err)
	assert.Equal(t, 1, got1.Revision)

	r2 := CanonicalRecord{
		CanonicalID:   got1.CanonicalID,
		Kind:          "observation",
		Title:         "Updated",
		Content:       "v2",
		ContentFormat: "text",
		CreatedAt:     got1.CreatedAt,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Tags:          []string{},
	}
	got2, err := s.Append(r2)
	require.NoError(t, err)
	assert.Equal(t, 2, got2.Revision)
	assert.Equal(t, got1.CanonicalID, got2.Supersedes, "supersedes must equal canonical_id")

	// ReadLive returns revision 2 (without flush — index is in-memory).
	require.NoError(t, s.w.Flush())
	live, err := s.ReadLive(got1.CanonicalID)
	require.NoError(t, err)
	assert.Equal(t, 2, live.Revision)
	assert.Equal(t, "Updated", live.Title)

	// ReadAll returns both revisions in file order.
	all, err := s.ReadAll(got1.CanonicalID)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, 1, all[0].Revision)
	assert.Equal(t, 2, all[1].Revision)
}

// ---------------------------------------------------------------------------
// Scenario D — Tombstone, ReadLive returns ErrDeleted
// ---------------------------------------------------------------------------

func TestScenarioD_Tombstone_ReadLiveErrDeleted(t *testing.T) {
	s := newTempStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Append rev 1.
	r1, err := s.Append(CanonicalRecord{
		Kind: "observation", Title: "T", Content: "C", ContentFormat: "text",
		CreatedAt: now, UpdatedAt: now, Tags: []string{},
	})
	require.NoError(t, err)

	// Append rev 2.
	r2, err := s.Append(CanonicalRecord{
		CanonicalID: r1.CanonicalID, Kind: "observation", Title: "T2",
		Content: "C2", ContentFormat: "text", CreatedAt: now,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Tags: []string{},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, r2.Revision)

	// Tombstone (rev 3).
	tomb, err := s.Append(CanonicalRecord{
		CanonicalID: r1.CanonicalID,
		Kind:        "observation",
		Deleted:     true,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		CreatedAt:   now,
		Tags:        []string{},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, tomb.Revision)
	assert.Equal(t, r1.CanonicalID, tomb.Supersedes)
	assert.True(t, tomb.Deleted)

	require.NoError(t, s.w.Flush())

	// ReadLive must return ErrDeleted.
	_, err = s.ReadLive(r1.CanonicalID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeleted)

	// ReadAll must return all 3 records.
	all, err := s.ReadAll(r1.CanonicalID)
	require.NoError(t, err)
	assert.Len(t, all, 3)
	assert.False(t, all[0].Deleted)
	assert.False(t, all[1].Deleted)
	assert.True(t, all[2].Deleted)
}

// ---------------------------------------------------------------------------
// Scenario E — Double tombstone rejected
// ---------------------------------------------------------------------------

func TestScenarioE_DoubleTombstone_ErrDeleted(t *testing.T) {
	s := newTempStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	r1, err := s.Append(CanonicalRecord{
		Kind: "observation", Title: "T", Content: "C", ContentFormat: "text",
		CreatedAt: now, UpdatedAt: now, Tags: []string{},
	})
	require.NoError(t, err)

	// First tombstone.
	_, err = s.Append(CanonicalRecord{
		CanonicalID: r1.CanonicalID, Kind: "observation", Deleted: true,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), CreatedAt: now, Tags: []string{},
	})
	require.NoError(t, err)

	// Capture file size before second tombstone attempt.
	require.NoError(t, s.w.Flush())
	info, err := os.Stat(filepath.Join(s.rootDir, memoryFilename))
	require.NoError(t, err)
	sizeBefore := info.Size()

	// Second tombstone must be rejected.
	_, err = s.Append(CanonicalRecord{
		CanonicalID: r1.CanonicalID, Kind: "observation", Deleted: true,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), CreatedAt: now, Tags: []string{},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeleted)

	// File must not have grown (no partial write).
	require.NoError(t, s.w.Flush())
	info2, err := os.Stat(filepath.Join(s.rootDir, memoryFilename))
	require.NoError(t, err)
	assert.Equal(t, sizeBefore, info2.Size())
}

// ---------------------------------------------------------------------------
// Scenario M — AppendBatch: single fsync, all records readable
// ---------------------------------------------------------------------------

func TestScenarioM_AppendBatch_SingleFsync(t *testing.T) {
	s := newTempStore(t)
	mock := injectMockSyncer(t, s)

	const count = 100 // keep test fast; enough to prove single-fsync guarantee
	now := time.Now().UTC().Format(time.RFC3339Nano)
	batch := make([]CanonicalRecord, count)
	for i := range batch {
		batch[i] = CanonicalRecord{
			Kind: "observation", Title: "batch", Content: "x",
			ContentFormat: "text", CreatedAt: now, UpdatedAt: now, Tags: []string{},
		}
	}

	results, err := s.AppendBatch(batch)
	require.NoError(t, err)
	require.Len(t, results, count)

	// Exactly one Sync call.
	assert.Equal(t, int64(1), mock.syncCount.Load(), "AppendBatch must call Sync exactly once")

	// All records readable.
	for _, r := range results {
		live, err := s.ReadLive(r.CanonicalID)
		require.NoError(t, err)
		assert.Equal(t, r.CanonicalID, live.CanonicalID)
	}
}

// ---------------------------------------------------------------------------
// Scenario N — AppendBatch with invalid record — no partial write
// ---------------------------------------------------------------------------

func TestScenarioN_AppendBatch_InvalidRecord_NoPartialWrite(t *testing.T) {
	s := newTempStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Prime the store with one record so the file is non-empty.
	_, err := s.Append(CanonicalRecord{
		Kind: "observation", Content: "existing", ContentFormat: "text",
		CreatedAt: now, UpdatedAt: now, Tags: []string{},
	})
	require.NoError(t, err)
	require.NoError(t, s.w.Flush())

	info, err := os.Stat(filepath.Join(s.rootDir, memoryFilename))
	require.NoError(t, err)
	sizeBefore := info.Size()

	// Batch: record[0] valid, record[1] has empty kind (invalid).
	batch := []CanonicalRecord{
		{Kind: "observation", Content: "good", ContentFormat: "text",
			CreatedAt: now, UpdatedAt: now, Tags: []string{}},
		{Kind: "", Content: "bad", ContentFormat: "text", // invalid: empty kind
			CreatedAt: now, UpdatedAt: now, Tags: []string{}},
		{Kind: "observation", Content: "also good", ContentFormat: "text",
			CreatedAt: now, UpdatedAt: now, Tags: []string{}},
	}

	_, err = s.AppendBatch(batch)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRecord)

	// File must be unchanged.
	info2, err := os.Stat(filepath.Join(s.rootDir, memoryFilename))
	require.NoError(t, err)
	assert.Equal(t, sizeBefore, info2.Size())
}

// ---------------------------------------------------------------------------
// Scenario Q — Tombstone with wrong supersedes field → ErrInvalidRecord
// ---------------------------------------------------------------------------

func TestScenarioQ_TombstoneWrongSupersedes_ErrInvalidRecord(t *testing.T) {
	s := newTempStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	r1, err := s.Append(CanonicalRecord{
		Kind: "observation", Content: "C", ContentFormat: "text",
		CreatedAt: now, UpdatedAt: now, Tags: []string{},
	})
	require.NoError(t, err)

	require.NoError(t, s.w.Flush())
	info, err := os.Stat(filepath.Join(s.rootDir, memoryFilename))
	require.NoError(t, err)
	sizeBefore := info.Size()

	// Tombstone with wrong supersedes.
	_, err = s.Append(CanonicalRecord{
		CanonicalID: r1.CanonicalID,
		Kind:        "observation",
		Deleted:     true,
		Supersedes:  "01WRONGWRONGWRONGWRONGWRONG", // must equal canonical_id
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		CreatedAt:   now,
		Tags:        []string{},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRecord)

	// File must be unchanged.
	require.NoError(t, s.w.Flush())
	info2, err := os.Stat(filepath.Join(s.rootDir, memoryFilename))
	require.NoError(t, err)
	assert.Equal(t, sizeBefore, info2.Size())
}

// ---------------------------------------------------------------------------
// line_ulid present on every line (acceptance criterion §14.8)
// ---------------------------------------------------------------------------

func TestLineULID_PresentOnEveryLine(t *testing.T) {
	s := newTempStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	for i := 0; i < 5; i++ {
		_, err := s.Append(CanonicalRecord{
			Kind: "observation", Content: "x", ContentFormat: "text",
			CreatedAt: now, UpdatedAt: now, Tags: []string{},
		})
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())

	// Read raw bytes and verify every line has a non-empty line_ulid.
	data, err := os.ReadFile(filepath.Join(s.rootDir, memoryFilename))
	require.NoError(t, err)

	lines := splitLines(data)
	require.Len(t, lines, 5)
	for i, line := range lines {
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(line, &raw), "line %d must be valid JSON", i)
		rawULID, ok := raw["line_ulid"]
		require.True(t, ok, "line %d missing line_ulid field", i)
		var ulidStr string
		require.NoError(t, json.Unmarshal(rawULID, &ulidStr))
		assert.NotEmpty(t, ulidStr, "line %d line_ulid must not be empty", i)
	}
}

// splitLines splits newline-terminated byte slice into individual lines (no trailing empty).
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	return lines
}
