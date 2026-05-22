package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// fileSize returns the current byte size of the file backing the store.
// Must be called with at least a read lock held.
func (s *Store) fileSize() int64 {
	info, err := s.f.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

// syncer is satisfied by *os.File and allows tests to inject a mock that
// counts Sync calls (needed for Scenario M — single fsync per AppendBatch).
type syncer interface {
	Write(p []byte) (n int, err error)
	Sync() error
	Name() string
	ReadAt(b []byte, off int64) (n int, err error)
	Stat() (os.FileInfo, error)
	Close() error
}

// Store is an append-only JSONL observation log backed by a single directory.
// All exported methods are safe for concurrent use from multiple goroutines
// within the same process, but only one process may hold the advisory lock.
type Store struct {
	rootDir string

	mu     sync.RWMutex
	idx    *index
	f      syncer        // append-only memory.jsonl handle
	w      *bufio.Writer // 64 KB buffer on top of f
	lock   *fileLock
	ulid   ulidSource
	dirty  bool // true if buffered writes exist or index needs persist
	closed bool
}

// Open initializes a Store rooted at rootDir using the bootstrap sequence:
// MkdirAll → TryLock → schema → recover → open O_APPEND → loadOrRebuild.
// It acquires a non-blocking advisory lock; returns ErrLocked if another
// process holds it. The caller MUST defer Close().
func Open(rootDir string) (*Store, error) {
	absPath, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("store: abs path: %w", err)
	}

	// Step 1: create root directory.
	if err := os.MkdirAll(absPath, 0755); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}

	// Step 2: acquire advisory lock.
	lockPath := filepath.Join(absPath, lockFilename)
	lk := newFileLock(lockPath)
	if err := lk.tryAcquire(); err != nil {
		return nil, err // ErrLocked or OS error
	}
	// If any subsequent step fails, release the lock.
	var lockReleased bool
	releaseLock := func() {
		if !lockReleased {
			lk.release()
			lockReleased = true
		}
	}

	// Step 3: bootstrap/validate schema.json.
	schemaPath := filepath.Join(absPath, schemaFilename)
	if err := loadOrBootstrapSchema(schemaPath); err != nil {
		releaseLock()
		return nil, err // ErrSchemaTooNew or OS error
	}

	// Step 4: crash recovery.
	memPath := filepath.Join(absPath, memoryFilename)
	rwFile, err := os.OpenFile(memPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		releaseLock()
		return nil, fmt.Errorf("store: open rw: %w", err)
	}
	truncated, err := recoverPartialLine(rwFile)
	rwFile.Close()
	if err != nil {
		releaseLock()
		return nil, fmt.Errorf("store: recover: %w", err)
	}
	if truncated > 0 {
		log.Printf("store: recovered %d bytes of partial trailing line", truncated)
	}

	// Step 5: open append handle.
	f, err := os.OpenFile(memPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		releaseLock()
		return nil, fmt.Errorf("store: open append: %w", err)
	}
	w := bufio.NewWriterSize(f, 64*1024)

	// Step 6: load or rebuild index.
	idx, dirty, err := loadOrRebuild(absPath)
	if err != nil {
		f.Close()
		releaseLock()
		return nil, fmt.Errorf("store: index: %w", err)
	}

	// Step 7: persist index if dirty or if index.json was absent.
	idxPath := filepath.Join(absPath, indexFilename)
	if dirty {
		if err := idx.persist(idxPath); err != nil {
			f.Close()
			releaseLock()
			return nil, fmt.Errorf("store: persist index: %w", err)
		}
	} else {
		// Ensure index.json exists on first-run (empty file case).
		if _, err := os.Stat(idxPath); os.IsNotExist(err) {
			if err := idx.persist(idxPath); err != nil {
				f.Close()
				releaseLock()
				return nil, fmt.Errorf("store: persist index (bootstrap): %w", err)
			}
		}
	}

	lockReleased = true // we're keeping the lock; cancel deferred release
	return &Store{
		rootDir: absPath,
		idx:     idx,
		f:       f,
		w:       w,
		lock:    lk,
		ulid:    defaultULIDSource{},
		dirty:   false,
		closed:  false,
	}, nil
}

// Close flushes any buffered writes, fsyncs memory.jsonl, persists index.json
// atomically, and releases the advisory lock. Safe to call multiple times.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	if s.dirty {
		if err := s.w.Flush(); err != nil {
			return fmt.Errorf("store close: flush: %w", err)
		}
		if err := s.f.Sync(); err != nil {
			return fmt.Errorf("store close: sync: %w", err)
		}
		// Update fingerprint and line count before persisting index.
		fp, err := computeFingerprint(s.f.Name())
		if err != nil {
			return fmt.Errorf("store close: fingerprint: %w", err)
		}
		lc, err := countLines(s.f.Name())
		if err != nil {
			return fmt.Errorf("store close: countLines: %w", err)
		}
		s.idx.SourceFingerprint = fp
		s.idx.LastLineCount = lc
		s.idx.BuiltAt = time.Now().UTC().Format(time.RFC3339Nano)
		idxPath := filepath.Join(s.rootDir, indexFilename)
		if err := s.idx.persist(idxPath); err != nil {
			return fmt.Errorf("store close: persist index: %w", err)
		}
		s.dirty = false
	}

	if err := s.f.Close(); err != nil {
		return fmt.Errorf("store close: close file: %w", err)
	}
	if err := s.lock.release(); err != nil {
		return fmt.Errorf("store close: release lock: %w", err)
	}
	return nil
}

// ReadLive returns the latest non-deleted revision of canonicalID.
// Returns ErrNotFound if the chain does not exist, ErrDeleted if tombstoned.
func (s *Store) ReadLive(canonicalID string) (CanonicalRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.idx.Entries[canonicalID]
	if !ok {
		return CanonicalRecord{}, ErrNotFound
	}
	if entry.Deleted {
		return CanonicalRecord{}, ErrDeleted
	}

	return s.readLineAt(entry.LatestLineOffset)
}

// ReadAll returns every line in memory.jsonl whose canonical_id equals
// canonicalID, in append order, including tombstones.
// Returns ErrNotFound if no such canonical_id is in the index.
func (s *Store) ReadAll(canonicalID string) ([]CanonicalRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.idx.Entries[canonicalID]; !ok {
		return nil, ErrNotFound
	}

	// Full sequential scan.
	memPath := filepath.Join(s.rootDir, memoryFilename)
	f, err := os.Open(memPath)
	if err != nil {
		return nil, fmt.Errorf("ReadAll: open: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, len(buf))

	var results []CanonicalRecord
	for scanner.Scan() {
		r, ok, err := decodeLine(scanner.Bytes())
		if err != nil || !ok {
			continue
		}
		if r.CanonicalID == canonicalID {
			results = append(results, r)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ReadAll: scan: %w", err)
	}
	return results, nil
}

// readLineAt reads and decodes the line starting at byteOffset in memory.jsonl.
// The caller must hold at least a read lock.
func (s *Store) readLineAt(offset int64) (CanonicalRecord, error) {
	memPath := filepath.Join(s.rootDir, memoryFilename)
	f, err := os.Open(memPath)
	if err != nil {
		return CanonicalRecord{}, fmt.Errorf("readLineAt: open: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return CanonicalRecord{}, fmt.Errorf("readLineAt: seek: %w", err)
	}

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return CanonicalRecord{}, fmt.Errorf("readLineAt: scan: %w", err)
		}
		return CanonicalRecord{}, fmt.Errorf("readLineAt: unexpected EOF at offset %d", offset)
	}

	var r CanonicalRecord
	if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
		return CanonicalRecord{}, fmt.Errorf("readLineAt: unmarshal: %w", err)
	}
	return r, nil
}

// ListLive returns the latest non-deleted revision of every canonical_id in
// the index, in unspecified order. Deleted records are omitted. This is the
// primary read path for show and pull (PR-D).
func (s *Store) ListLive() ([]CanonicalRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []CanonicalRecord
	for _, entry := range s.idx.Entries {
		if entry.Deleted {
			continue
		}
		r, err := s.readLineAt(entry.LatestLineOffset)
		if err != nil {
			return nil, fmt.Errorf("ListLive: read offset %d: %w", entry.LatestLineOffset, err)
		}
		results = append(results, r)
	}
	return results, nil
}

// ReadLineAtOffset reads and decodes the JSONL line at byteOffset in memory.jsonl.
// It is used by the sync engine's Resolve operation to retrieve a chosen
// conflict duplicate by its stored line offset.
func (s *Store) ReadLineAtOffset(offset int64) (CanonicalRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readLineAt(offset)
}

// Rebuild discards the in-memory index and reconstructs it from a full
// streaming scan of memory.jsonl. Idempotent.
func (s *Store) Rebuild() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dirty {
		if err := s.w.Flush(); err != nil {
			return fmt.Errorf("Rebuild: flush: %w", err)
		}
		if err := s.f.Sync(); err != nil {
			return fmt.Errorf("Rebuild: sync: %w", err)
		}
	}

	memPath := filepath.Join(s.rootDir, memoryFilename)
	newIdx, err := rebuildFromFile(memPath)
	if err != nil {
		return fmt.Errorf("Rebuild: %w", err)
	}

	// SyncState has no representation in memory.jsonl, so rebuildFromFile
	// cannot reconstruct it. Carry provider watermarks forward across rebuild.
	newIdx.SyncState = s.idx.SyncState

	idxPath := filepath.Join(s.rootDir, indexFilename)
	if err := newIdx.persist(idxPath); err != nil {
		return fmt.Errorf("Rebuild: persist: %w", err)
	}

	s.idx = newIdx
	s.dirty = false
	return nil
}

// computeAppendFields fills all store-managed fields (canonical_id, line_ulid,
// revision, supersedes, schema_version) for a single record given a projected
// index snapshot. It returns the completed record, the updated projected index,
// and any validation error. It does NOT write to the file or the real index.
func computeAppendFields(
	r CanonicalRecord,
	projected map[string]IndexEntry,
	u ulidSource,
) (CanonicalRecord, map[string]IndexEntry, error) {
	// Tombstone-supersedes pre-check: if caller already set deleted=true and
	// a non-empty supersedes that disagrees with canonical_id, reject early.
	if r.Deleted && r.CanonicalID != "" && r.Supersedes != "" && r.Supersedes != r.CanonicalID {
		return CanonicalRecord{}, projected,
			fmt.Errorf("append: %w", ErrInvalidRecord)
	}

	if r.CanonicalID == "" {
		// New chain — store assigns canonical_id.
		if r.Deleted {
			return CanonicalRecord{}, projected,
				fmt.Errorf("append: cannot tombstone unknown id: %w", ErrInvalidRecord)
		}
		r.CanonicalID = u.Make()
		r.Revision = 1
		r.Supersedes = ""
	} else {
		existing, ok := projected[r.CanonicalID]
		if !ok {
			// Caller-supplied canonical_id for a brand-new chain.
			if r.Deleted {
				return CanonicalRecord{}, projected,
					fmt.Errorf("append: tombstone for unknown canonical_id: %w", ErrNotFound)
			}
			if r.Revision == 0 {
				r.Revision = 1
			}
			r.Supersedes = ""
		} else {
			// Existing chain.
			if r.Deleted && existing.Deleted {
				return CanonicalRecord{}, projected,
					fmt.Errorf("append: already tombstoned: %w", ErrDeleted)
			}
			r.Revision = existing.LatestRevision + 1
			r.Supersedes = r.CanonicalID // self-referential per REQ-DATA-04/05
		}
	}

	// Tombstone invariant (defensive — catches any remaining mismatch).
	if r.Deleted && r.Supersedes != r.CanonicalID {
		return CanonicalRecord{}, projected,
			fmt.Errorf("append: tombstone supersedes mismatch: %w", ErrInvalidRecord)
	}

	// Validate the completed record fields.
	if err := validateRecord(r); err != nil {
		return CanonicalRecord{}, projected, fmt.Errorf("append: %w", err)
	}

	r.LineULID = u.Make() // unconditional per REQ-DATA-03
	r.SchemaVersion = StoreSupportedVersion

	// Update projected index so subsequent records in a batch see this revision.
	projected[r.CanonicalID] = IndexEntry{
		CanonicalID:    r.CanonicalID,
		LatestRevision: r.Revision,
		Deleted:        r.Deleted,
		UpdatedAt:      r.UpdatedAt,
		LineULID:       r.LineULID,
	}

	return r, projected, nil
}

// Append writes a single CanonicalRecord to memory.jsonl. The store assigns
// canonical_id (if empty), line_ulid (always), revision, and supersedes.
// The write is buffered; durability is guaranteed only after AppendBatch or Close.
func (s *Store) Append(r CanonicalRecord) (CanonicalRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return CanonicalRecord{}, errors.New("store: closed")
	}

	// Clone the index entries map so computeAppendFields can update a projection.
	projected := make(map[string]IndexEntry, len(s.idx.Entries))
	for k, v := range s.idx.Entries {
		projected[k] = v
	}

	completed, projected, err := computeAppendFields(r, projected, s.ulid)
	if err != nil {
		return CanonicalRecord{}, err
	}

	data, err := json.Marshal(completed)
	if err != nil {
		return CanonicalRecord{}, fmt.Errorf("append: marshal: %w", err)
	}
	data = append(data, '\n')

	offset := s.fileSize() + int64(s.w.Buffered())

	if _, err := s.w.Write(data); err != nil {
		return CanonicalRecord{}, fmt.Errorf("append: write: %w", err)
	}

	// Update the real in-memory index with the new offset.
	entry := projected[completed.CanonicalID]
	entry.LatestLineOffset = offset
	s.idx.Entries[completed.CanonicalID] = entry
	s.dirty = true

	return completed, nil
}

// AppendBatch appends multiple records and performs exactly one flush + fsync
// + index persist at the end. If any record fails validation the entire batch
// is rejected and memory.jsonl is left unchanged. An empty slice is a no-op.
func (s *Store) AppendBatch(rs []CanonicalRecord) ([]CanonicalRecord, error) {
	if len(rs) == 0 {
		return []CanonicalRecord{}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errors.New("store: closed")
	}

	// Pass 1: pre-validate ALL records against a projected index.
	// This ensures batch[i]'s revision/supersedes reflects batch[0..i-1].
	projected := make(map[string]IndexEntry, len(s.idx.Entries)+len(rs))
	for k, v := range s.idx.Entries {
		projected[k] = v
	}

	completed := make([]CanonicalRecord, len(rs))
	for i, r := range rs {
		var err error
		completed[i], projected, err = computeAppendFields(r, projected, s.ulid)
		if err != nil {
			return nil, fmt.Errorf("batch[%d]: %w", i, err)
		}
	}

	// Pass 2: write all records through buffer (no error expected after validation).
	offsets := make([]int64, len(completed))
	for i, r := range completed {
		offsets[i] = s.fileSize() + int64(s.w.Buffered())
		data, err := json.Marshal(r)
		if err != nil {
			return nil, fmt.Errorf("batch marshal[%d]: %w", i, err)
		}
		data = append(data, '\n')
		if _, err := s.w.Write(data); err != nil {
			return nil, fmt.Errorf("batch write[%d]: %w", i, err)
		}
	}

	// Single flush + fsync.
	if err := s.w.Flush(); err != nil {
		return nil, fmt.Errorf("AppendBatch: flush: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		return nil, fmt.Errorf("AppendBatch: sync: %w", err)
	}

	// Update real index and persist.
	for i, r := range completed {
		entry := projected[r.CanonicalID]
		entry.LatestLineOffset = offsets[i]
		s.idx.Entries[r.CanonicalID] = entry
	}

	fp, err := computeFingerprint(s.f.Name())
	if err != nil {
		return nil, fmt.Errorf("AppendBatch: fingerprint: %w", err)
	}
	lc, err := countLines(s.f.Name())
	if err != nil {
		return nil, fmt.Errorf("AppendBatch: countLines: %w", err)
	}
	s.idx.SourceFingerprint = fp
	s.idx.LastLineCount = lc
	s.idx.BuiltAt = time.Now().UTC().Format(time.RFC3339Nano)

	idxPath := filepath.Join(s.rootDir, indexFilename)
	if err := s.idx.persist(idxPath); err != nil {
		return nil, fmt.Errorf("AppendBatch: persist index: %w", err)
	}
	s.dirty = false

	return completed, nil
}

// ProviderIDMapSnapshot is a point-in-time copy of the native↔canonical ID
// mapping for a single provider. It uses plain string keys — callers that need
// to satisfy adapter.IDMap (which uses named types) must wrap it in a thin
// adapter (see internal/sync.providerIDMapAdapter). This keeps internal/store
// free of any internal/adapter import (CON-01).
type ProviderIDMapSnapshot struct {
	forward map[string]string // nativeID -> canonicalID
	reverse map[string]string // canonicalID -> nativeID (built lazily on first use)
}

// CanonicalFromNative returns the canonical ID for the given native provider ID.
func (m *ProviderIDMapSnapshot) CanonicalFromNative(nativeID string) (string, bool) {
	v, ok := m.forward[nativeID]
	return v, ok
}

// NativeFromCanonical returns the native provider ID for the given canonical ID.
func (m *ProviderIDMapSnapshot) NativeFromCanonical(canonicalID string) (string, bool) {
	if m.reverse == nil {
		m.reverse = make(map[string]string, len(m.forward))
		for k, v := range m.forward {
			m.reverse[v] = k
		}
	}
	v, ok := m.reverse[canonicalID]
	return v, ok
}

// ProviderIDMap returns a read-only snapshot view of the ID mapping for provider.
// If provider is unknown, a non-nil snapshot is returned that returns false for all lookups.
// Safe for concurrent use; acquires a read lock for the snapshot only.
func (s *Store) ProviderIDMap(provider string) *ProviderIDMapSnapshot {
	s.mu.RLock()
	inner := s.idx.ByProvider[provider] // nil if provider unknown
	s.mu.RUnlock()

	// Copy the inner map to avoid holding a reference to the live index.
	snapshot := make(map[string]string, len(inner))
	for k, v := range inner {
		snapshot[k] = v
	}
	return &ProviderIDMapSnapshot{forward: snapshot}
}

// RootDir returns the absolute path of the store directory (e.g. .wrapper-mems/).
// Used by the Engine's git-commit helper to locate the working tree root.
func (s *Store) RootDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rootDir
}

// isClosed reports whether the store has been closed. Useful for tests.
func (s *Store) isClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// isErrSchemaTooNew is a helper for testing ErrSchemaTooNew unwrapping.
func isErrSchemaTooNew(err error) bool {
	return errors.Is(err, ErrSchemaTooNew)
}
