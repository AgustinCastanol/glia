package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"time"
)

// lineCandidate holds the per-line data needed for tiebreak selection.
type lineCandidate struct {
	offset    int64
	revision  int
	updatedAt string
	lineULID  string
	deleted   bool
}

// tiebreakWinner selects the winning line from candidates using the rule:
// revision DESC, updatedAt DESC (RFC3339 lex), lineULID DESC (lex).
func tiebreakWinner(cands []lineCandidate) lineCandidate {
	sorted := make([]lineCandidate, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.revision != b.revision {
			return a.revision > b.revision
		}
		if a.updatedAt != b.updatedAt {
			return a.updatedAt > b.updatedAt
		}
		return a.lineULID > b.lineULID
	})
	return sorted[0]
}

// rebuildFromFile performs a full streaming scan of path and builds a fresh index.
// Lines with schema_version > StoreSupportedVersion are skipped with a warning.
// Lines that fail JSON decode are also skipped with a warning.
func rebuildFromFile(path string) (*index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("rebuild: open %s: %w", path, err)
	}
	defer f.Close()

	// candidateOrigins tracks the origin for each canonical_id line (last write wins
	// per canonical_id; safe because canonical_id is stable across revisions).
	type originEntry struct {
		provider   string
		providerID string
		deleted    bool
	}
	candidates := make(map[string][]lineCandidate)
	origins := make(map[string]originEntry)

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 1024*1024) // 1 MB max token
	scanner.Buffer(buf, len(buf))

	var offset int64
	lineCount := 0

	for scanner.Scan() {
		raw := scanner.Bytes()
		lineLen := int64(len(raw)) + 1 // +1 for '\n' stripped by scanner

		var rr rawRecord
		if err := json.Unmarshal(raw, &rr); err != nil {
			log.Printf("store rebuild: skipping malformed line at offset %d: %v", offset, err)
			offset += lineLen
			continue
		}
		if rr.SchemaVersion > StoreSupportedVersion {
			log.Printf("store rebuild: skipping line at offset %d: schema_version %d > supported %d",
				offset, rr.SchemaVersion, StoreSupportedVersion)
			offset += lineLen
			continue
		}
		if rr.CanonicalID == "" || rr.LineULID == "" {
			log.Printf("store rebuild: skipping line at offset %d: missing canonical_id or line_ulid", offset)
			offset += lineLen
			continue
		}

		candidates[rr.CanonicalID] = append(candidates[rr.CanonicalID], lineCandidate{
			offset:    offset,
			revision:  rr.Revision,
			updatedAt: rr.UpdatedAt,
			lineULID:  rr.LineULID,
			deleted:   rr.Deleted,
		})

		// Track origin for ByProvider population; every line updates so the
		// winning revision's origin (last-wins) is what we record.
		if rr.Origin.Provider != "" && rr.Origin.ProviderID != "" {
			origins[rr.CanonicalID] = originEntry{
				provider:   rr.Origin.Provider,
				providerID: rr.Origin.ProviderID,
				deleted:    rr.Deleted,
			}
		} else if rr.Deleted {
			// Tombstone with no origin — still marks canonical_id as deleted.
			if oe, ok := origins[rr.CanonicalID]; ok {
				oe.deleted = true
				origins[rr.CanonicalID] = oe
			}
		}

		offset += lineLen
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("rebuild: scan %s: %w", path, err)
	}

	entries := make(map[string]IndexEntry, len(candidates))
	for cid, lines := range candidates {
		winner := tiebreakWinner(lines)
		entries[cid] = IndexEntry{
			CanonicalID:      cid,
			LatestRevision:   winner.revision,
			LatestLineOffset: winner.offset,
			Deleted:          winner.deleted,
			UpdatedAt:        winner.updatedAt,
			LineULID:         winner.lineULID,
		}
	}

	// Build ByProvider from origins: only include live (non-deleted) mappings.
	byProvider := make(map[string]map[string]string)
	for cid, oe := range origins {
		if oe.deleted {
			continue
		}
		if _, ok := byProvider[oe.provider]; !ok {
			byProvider[oe.provider] = make(map[string]string)
		}
		byProvider[oe.provider][oe.providerID] = cid
	}

	fp, err := computeFingerprint(path)
	if err != nil {
		return nil, fmt.Errorf("rebuild: fingerprint: %w", err)
	}

	return &index{
		SchemaVersion:     StoreSupportedVersion,
		SourceFingerprint: fp,
		LastLineCount:     lineCount,
		BuiltAt:           time.Now().UTC().Format(time.RFC3339Nano),
		Entries:           entries,
		ByProvider:        byProvider,
	}, nil
}

// countLines counts newline-terminated lines in path using a buffered scan.
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("countLines: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	n := 0
	for scanner.Scan() {
		n++
	}
	return n, scanner.Err()
}

// loadOrRebuild loads the cached index from rootDir/index.json and validates
// it against the current fingerprint and line count of rootDir/memory.jsonl.
// Returns (index, dirty, error). dirty==true means the index was rebuilt and
// needs to be persisted to disk.
func loadOrRebuild(rootDir string) (*index, bool, error) {
	memPath := rootDir + "/" + memoryFilename
	idxPath := rootDir + "/" + indexFilename

	_, err := os.Stat(idxPath)
	if os.IsNotExist(err) {
		// No index file — build fresh.
		idx, err := rebuildFromFile(memPath)
		if err != nil {
			return nil, false, err
		}
		return idx, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("loadOrRebuild: stat index: %w", err)
	}

	idx, err := loadIndex(idxPath)
	if err != nil {
		// Corrupt index — rebuild.
		log.Printf("store: index parse error, rebuilding: %v", err)
		idx, err := rebuildFromFile(memPath)
		if err != nil {
			return nil, false, err
		}
		return idx, true, nil
	}

	// Validate fingerprint and line count.
	currentFP, err := computeFingerprint(memPath)
	if err != nil {
		return nil, false, fmt.Errorf("loadOrRebuild: fingerprint: %w", err)
	}
	currentLC, err := countLines(memPath)
	if err != nil {
		return nil, false, fmt.Errorf("loadOrRebuild: countLines: %w", err)
	}

	if idx.SourceFingerprint == currentFP && idx.LastLineCount == currentLC {
		return idx, false, nil // cache hit
	}

	log.Printf("store: index stale (fp=%s want=%s, lines=%d want=%d), rebuilding",
		idx.SourceFingerprint, currentFP, idx.LastLineCount, currentLC)
	rebuilt, err := rebuildFromFile(memPath)
	if err != nil {
		return nil, false, err
	}
	return rebuilt, true, nil
}
