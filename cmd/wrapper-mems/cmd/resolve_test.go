package cmd

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// seedConflict creates a store with one record and injects a ConflictEntry
// pointing at that record's line (offset 0 — it is the only line).
// It returns the store path and the canonical_id that is in conflict.
func seedConflict(t *testing.T, dir string) (sp string, canonicalID string) {
	t.Helper()
	sp = filepath.Join(dir, ".wrapper-mems")
	s, err := store.Open(sp)
	if err != nil {
		t.Fatalf("seedConflict: open: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	earlier := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)

	// Append one line; the store assigns canonical_id, revision=1, offset=0.
	rec1, err := s.Append(store.CanonicalRecord{
		Kind:          "observation",
		Title:         "conflict-A",
		Content:       "content A",
		ContentFormat: "text",
		Type:          "note",
		CreatedAt:     earlier,
		UpdatedAt:     earlier,
	})
	if err != nil {
		t.Fatalf("seedConflict: append: %v", err)
	}
	canonicalID = rec1.CanonicalID

	// Inject a ConflictEntry whose single Duplicate points at offset 0 (the only
	// JSONL line).  Engine.Resolve uses ReadLineAtOffset(dup.LineOffset) to read
	// the chosen record, so the offset must be valid.
	dup := store.ConflictDuplicate{
		LineOffset: 0,
		LineULID:   rec1.LineULID,
		UpdatedAt:  now,
		Provider:   "test",
	}
	conflict := store.ConflictEntry{
		CanonicalID: canonicalID,
		Revision:    rec1.Revision,
		DetectedAt:  now,
		Duplicates:  []store.ConflictDuplicate{dup},
	}
	if err := s.AppendConflict(conflict); err != nil {
		t.Fatalf("seedConflict: AppendConflict: %v", err)
	}
	s.Close()

	return sp, canonicalID
}

// executeResolve runs syncResolveCmd with --keep=keep for the given
// canonicalID, capturing stdout.  Returns stdout and the RunE error.
func executeResolve(t *testing.T, dir, canonicalID string, keep int) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rootFlags.dir = dir
	syncResolveCmd.SetOut(&buf)
	resolveFlags.keep = keep

	err := syncResolveCmd.RunE(syncResolveCmd, []string{canonicalID})
	rootFlags.dir = ""
	return buf.String(), err
}

// TestResolve_ValidDupIndex checks the happy path: conflict is resolved and
// the ConflictEntry is removed (REQ-SE-37).
func TestResolve_ValidDupIndex(t *testing.T) {
	dir := t.TempDir()
	_, canonicalID := seedConflict(t, dir)

	out, err := executeResolve(t, dir, canonicalID, 1)
	if err != nil {
		t.Fatalf("resolve returned error: %v", err)
	}
	if !strings.Contains(out, "conflict resolved") {
		t.Errorf("expected 'conflict resolved' in output, got: %q", out)
	}
	if !strings.Contains(out, canonicalID) {
		t.Errorf("expected canonical_id %q in output, got: %q", canonicalID, out)
	}

	// Conflict must be gone from the store.
	sp := filepath.Join(dir, ".wrapper-mems")
	s, err := store.Open(sp)
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	defer s.Close()
	for _, c := range s.Conflicts() {
		if c.CanonicalID == canonicalID {
			t.Errorf("ConflictEntry still present after resolve")
		}
	}
}

// TestResolve_InvalidDupIndex checks that an out-of-range --keep value exits 1
// with the correct error message (REQ-SE-37 scenario).
func TestResolve_InvalidDupIndex(t *testing.T) {
	dir := t.TempDir()
	_, canonicalID := seedConflict(t, dir)

	_, err := executeResolve(t, dir, canonicalID, 99)
	if err == nil {
		t.Fatal("expected error for invalid dup_index, got nil")
	}
	if !strings.Contains(err.Error(), "invalid dup_index") {
		t.Errorf("expected 'invalid dup_index' in error, got: %v", err)
	}
}

// TestResolve_NoConflict checks that resolving a non-existent conflict exits 1
// with "no conflict found" (REQ-SE-38).
func TestResolve_NoConflict(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "normal", Type: "note"},
	})

	_, err := executeResolve(t, dir, "nonexistent-id", 1)
	if err == nil {
		t.Fatal("expected error for non-existent conflict, got nil")
	}
	if !strings.Contains(err.Error(), "no conflict found") {
		t.Errorf("expected 'no conflict found' in error, got: %v", err)
	}
}

// TestResolve_NoStore checks that resolve fails with errNoStore when the
// store directory does not exist (REQ-SE-05).
func TestResolve_NoStore(t *testing.T) {
	dir := t.TempDir() // no .wrapper-mems inside
	_, err := executeResolve(t, dir, "any-id", 1)
	if err == nil {
		t.Fatal("expected errNoStore, got nil")
	}
	if !errors.Is(err, errNoStore) {
		t.Errorf("expected errNoStore, got: %v", err)
	}
}

// TestResolve_KeepZeroRejected checks that --keep=0 is rejected before any
// store access (pre-validation guard).
func TestResolve_KeepZeroRejected(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "x", Type: "note"},
	})
	_, err := executeResolve(t, dir, "any-id", 0)
	if err == nil {
		t.Fatal("expected error for --keep=0, got nil")
	}
}
