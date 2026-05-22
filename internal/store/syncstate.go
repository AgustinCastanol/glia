package store

import (
	"fmt"
	"path/filepath"
	"time"
)

// UpdateSyncState overwrites the ProviderSyncState for provider in index.json.
// It holds the store write-lock, updates the in-memory index, and atomically
// persists index.json. Safe to call from a single goroutine while the store is open.
func (s *Store) UpdateSyncState(provider string, st ProviderSyncState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("store: closed")
	}

	if s.idx.SyncState == nil {
		s.idx.SyncState = make(map[string]ProviderSyncState)
	}
	s.idx.SyncState[provider] = st

	idxPath := filepath.Join(s.rootDir, indexFilename)
	return s.idx.persist(idxPath)
}

// AppendConflict adds a ConflictEntry to index.json.conflicts, deduplicating by
// (canonical_id, revision). If an entry with the same (canonical_id, revision)
// already exists it is replaced. Atomically persists index.json.
func (s *Store) AppendConflict(c ConflictEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("store: closed")
	}

	if c.DetectedAt == "" {
		c.DetectedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// Dedupe: replace existing entry with same (canonical_id, revision).
	replaced := false
	for i, existing := range s.idx.Conflicts {
		if existing.CanonicalID == c.CanonicalID && existing.Revision == c.Revision {
			s.idx.Conflicts[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		s.idx.Conflicts = append(s.idx.Conflicts, c)
	}

	idxPath := filepath.Join(s.rootDir, indexFilename)
	return s.idx.persist(idxPath)
}

// RemoveConflict removes all ConflictEntry records matching canonicalID from
// index.json.conflicts and atomically persists index.json.
// If no matching entry is found this is a no-op (returns nil).
func (s *Store) RemoveConflict(canonicalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("store: closed")
	}

	filtered := s.idx.Conflicts[:0]
	for _, c := range s.idx.Conflicts {
		if c.CanonicalID != canonicalID {
			filtered = append(filtered, c)
		}
	}
	s.idx.Conflicts = filtered

	idxPath := filepath.Join(s.rootDir, indexFilename)
	return s.idx.persist(idxPath)
}

// Conflicts returns a copy of the current conflict list.
// Safe for concurrent use.
func (s *Store) Conflicts() []ConflictEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.idx.Conflicts) == 0 {
		return nil
	}
	out := make([]ConflictEntry, len(s.idx.Conflicts))
	copy(out, s.idx.Conflicts)
	return out
}

// SyncState returns the ProviderSyncState for provider and whether it exists.
// Safe for concurrent use.
func (s *Store) SyncState(provider string) (ProviderSyncState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.idx.SyncState[provider]
	return st, ok
}
