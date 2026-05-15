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

	idxPath := filepath.Join(s.rootDir, indexFilename)
	if err := newIdx.persist(idxPath); err != nil {
		return fmt.Errorf("Rebuild: persist: %w", err)
	}

	s.idx = newIdx
	s.dirty = false
	return nil
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
