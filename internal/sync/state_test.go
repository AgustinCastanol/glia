package sync

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/store"
)

func openStateTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, ".glia"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestReadWatermark_NoEntry returns false when no sync state exists.
func TestReadWatermark_NoEntry(t *testing.T) {
	s := openStateTestStore(t)
	_, ok := readWatermark(s, "engram")
	if ok {
		t.Error("readWatermark should return false when no entry exists")
	}
}

// TestReadPullWatermark_NoEntry returns false when no sync state exists.
func TestReadPullWatermark_NoEntry(t *testing.T) {
	s := openStateTestStore(t)
	_, ok := readPullWatermark(s, "engram")
	if ok {
		t.Error("readPullWatermark should return false when no entry exists")
	}
}

// TestWriteAndReadWatermark round-trips a push watermark.
func TestWriteAndReadWatermark(t *testing.T) {
	s := openStateTestStore(t)

	pushedAt := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	result := ProviderResult{Pushed: 3}

	if err := writeWatermark(s, "engram", pushedAt, result); err != nil {
		t.Fatalf("writeWatermark: %v", err)
	}

	got, ok := readWatermark(s, "engram")
	if !ok {
		t.Fatal("readWatermark should return true after writeWatermark")
	}
	if !got.Equal(pushedAt) {
		t.Errorf("readWatermark = %v, want %v", got, pushedAt)
	}
}

// TestWriteAndReadPullWatermark round-trips a pull watermark.
func TestWriteAndReadPullWatermark(t *testing.T) {
	s := openStateTestStore(t)

	pulledAt := time.Date(2024, 7, 15, 8, 30, 0, 0, time.UTC)
	result := ProviderResult{Pulled: 5}

	if err := writePullWatermark(s, "engram", pulledAt, result); err != nil {
		t.Fatalf("writePullWatermark: %v", err)
	}

	got, ok := readPullWatermark(s, "engram")
	if !ok {
		t.Fatal("readPullWatermark should return true after writePullWatermark")
	}
	if !got.Equal(pulledAt) {
		t.Errorf("readPullWatermark = %v, want %v", got, pulledAt)
	}
}

// TestWriteWatermark_Accumulates verifies RecordsPushed accumulates across calls.
func TestWriteWatermark_Accumulates(t *testing.T) {
	s := openStateTestStore(t)

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := writeWatermark(s, "engram", t1, ProviderResult{Pushed: 10}); err != nil {
		t.Fatal(err)
	}

	t2 := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := writeWatermark(s, "engram", t2, ProviderResult{Pushed: 5}); err != nil {
		t.Fatal(err)
	}

	st, ok := s.SyncState("engram")
	if !ok {
		t.Fatal("SyncState should exist")
	}
	if st.RecordsPushed != 15 {
		t.Errorf("RecordsPushed = %d, want 15", st.RecordsPushed)
	}

	// LastPushedAt should reflect the most recent call.
	if st.LastPushedAt != t2.Format(time.RFC3339) {
		t.Errorf("LastPushedAt = %s, want %s", st.LastPushedAt, t2.Format(time.RFC3339))
	}
}

// TestReadWatermark_InvalidTimestamp returns false on corrupt watermark.
func TestReadWatermark_InvalidTimestamp(t *testing.T) {
	s := openStateTestStore(t)

	// Write a corrupt timestamp directly via UpdateSyncState.
	if err := s.UpdateSyncState("engram", store.ProviderSyncState{LastPushedAt: "not-a-date"}); err != nil {
		t.Fatal(err)
	}

	_, ok := readWatermark(s, "engram")
	if ok {
		t.Error("readWatermark with invalid timestamp should return false")
	}
}
