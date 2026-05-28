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

// BindProvider records a native-ID ↔ canonical-ID mapping for the given provider
// and atomically persists index.json. Used by the pull loop after a successful
// WriteNative to keep the ID map consistent.
func (s *Store) BindProvider(provider, nativeID, canonicalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("store: closed")
	}

	if s.idx.ByProvider == nil {
		s.idx.ByProvider = make(map[string]map[string]string)
	}
	if s.idx.ByProvider[provider] == nil {
		s.idx.ByProvider[provider] = make(map[string]string)
	}
	s.idx.ByProvider[provider][nativeID] = canonicalID

	idxPath := filepath.Join(s.rootDir, indexFilename)
	return s.idx.persist(idxPath)
}

// BindProviderWithRevision records a native-ID ↔ canonical-ID mapping AND
// the last-pushed revision for the given provider atomically in a single
// persist call (REQ-CMW-05, D2). Avoids the partial-write window that would
// exist if BindProvider and a separate revision-update were two separate calls.
func (s *Store) BindProviderWithRevision(provider, nativeID, canonicalID string, revision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("store: closed")
	}

	// ByProvider — native → canonical.
	if s.idx.ByProvider == nil {
		s.idx.ByProvider = make(map[string]map[string]string)
	}
	if s.idx.ByProvider[provider] == nil {
		s.idx.ByProvider[provider] = make(map[string]string)
	}
	s.idx.ByProvider[provider][nativeID] = canonicalID

	// ByProviderRevision — canonical → revision (lazy init, nil-safe for old indexes).
	if s.idx.ByProviderRevision == nil {
		s.idx.ByProviderRevision = make(map[string]map[string]int)
	}
	if s.idx.ByProviderRevision[provider] == nil {
		s.idx.ByProviderRevision[provider] = make(map[string]int)
	}
	s.idx.ByProviderRevision[provider][canonicalID] = revision

	idxPath := filepath.Join(s.rootDir, indexFilename)
	return s.idx.persist(idxPath)
}

// ProviderRevision returns the last-pushed revision for canonicalID in the
// given provider. Returns (0, false) when no revision has been recorded (either
// because the record was never pushed, or the index was created before Phase 4).
// Safe for concurrent use.
func (s *Store) ProviderRevision(provider, canonicalID string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.idx.ByProviderRevision == nil {
		return 0, false
	}
	provMap, ok := s.idx.ByProviderRevision[provider]
	if !ok {
		return 0, false
	}
	rev, ok := provMap[canonicalID]
	return rev, ok
}

// ProviderNativeToCanonical returns the canonical ID for the given native ID in
// the given provider. Returns ("", false) when no mapping exists.
// Safe for concurrent use.
func (s *Store) ProviderNativeToCanonical(provider, nativeID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.idx.ByProvider == nil {
		return "", false
	}
	provMap, ok := s.idx.ByProvider[provider]
	if !ok {
		return "", false
	}
	canonID, ok := provMap[nativeID]
	return canonID, ok
}
