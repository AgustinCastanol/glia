package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/agustincastanol/glia/internal/store"
)

// IndexSnapshot holds the index.json fields the TUI needs directly.
type IndexSnapshot struct {
	Conflicts []store.ConflictEntry              `json:"conflicts"`
	SyncState map[string]store.ProviderSyncState `json:"sync_state"`
}

// StatusJSON is the machine-readable payload emitted by `glia status --json`.
// It mirrors cmd.statusJSON (unexported there) — defined here for the data layer.
type StatusJSON struct {
	ProviderHealth map[string]string              `json:"provider_health"`
	Conflicts      []ConflictSummary              `json:"conflicts"`
	SyncState      map[string]store.ProviderSyncState `json:"sync_state"`
	LineCount      int                            `json:"line_count"`
	FileSizeBytes  int64                          `json:"file_size_bytes"`
	SchemaVersion  int                            `json:"schema_version"`
}

// ConflictSummary is a flattened conflict view as returned by status --json.
type ConflictSummary struct {
	CanonicalID string `json:"canonical_id"`
	Revision    int    `json:"revision"`
	DupCount    int    `json:"dup_count"`
	DetectedAt  string `json:"detected_at"`
}

// commandRunner is a function type for running a subprocess and returning its
// combined output. Abstracted so tests can inject a fake runner without
// spawning real processes.
type commandRunner func(name string, args ...string) ([]byte, error)

// dataLayer holds the data-access dependencies for the TUI.
// The runner field is injectable for tests.
type dataLayer struct {
	runner commandRunner
}

// defaultRunner is the real subprocess runner used in production.
func defaultRunner(name string, args ...string) ([]byte, error) {
	// Use os/exec via a local import-free approach — we import it below.
	return runCommand(name, args...)
}

// indexLine is the minimal decode of a JSONL line used during loadRecords.
// Only the fields needed for the list view and collapse logic are decoded.
// The full line bytes are kept in RawLine so the detail pane can do a full
// decode when the record is selected. This lazy approach keeps startup under
// the 100ms budget for 100k-line stores. (REQ-TUI-04)
type indexLine struct {
	SchemaVersion int    `json:"schema_version"`
	CanonicalID   string `json:"canonical_id"`
	Revision      int    `json:"revision"`
	Deleted       bool   `json:"deleted"`
	// Title, Kind, Type: displayed in the list — decoded here.
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Type  string `json:"type"`
}

// decodeFullRecord performs a full unmarshal of rawLine into a CanonicalRecord.
// Called lazily when the user selects a record and the detail pane needs the body.
func decodeFullRecord(rawLine []byte) (store.CanonicalRecord, error) {
	var rec store.CanonicalRecord
	if err := json.Unmarshal(rawLine, &rec); err != nil {
		return rec, fmt.Errorf("decodeFullRecord: %w", err)
	}
	return rec, nil
}

// LazyRecord holds the index fields decoded on load plus the raw JSONL bytes
// for full decoding on demand. (REQ-TUI-04: lazy content parsing)
type LazyRecord struct {
	// Index fields — decoded eagerly for list display.
	CanonicalID string
	Revision    int
	Title       string
	Kind        string
	Type        string

	// rawLine contains the original JSONL bytes. Call Decode() to get the full
	// store.CanonicalRecord including the Content body.
	rawLine []byte
}

// Decode returns the full CanonicalRecord by unmarshaling rawLine.
// Call this only when the detail pane needs the record body.
func (lr *LazyRecord) Decode() (store.CanonicalRecord, error) {
	return decodeFullRecord(lr.rawLine)
}

// FilterValue exposes fields used by the filter function.
func (lr *LazyRecord) FilterValue() string { return lr.Title }

// storeSubdir resolves the .glia store directory under a project root. The TUI
// sub-models carry the project root (their subprocess --dir calls require it),
// but loadRecords/loadIndexFile read files inside the .glia subdirectory.
func storeSubdir(projectDir string) string {
	return filepath.Join(projectDir, ".glia")
}

// loadRecords reads memory.jsonl from storeDir, streaming line-by-line with a
// 1 MB buffer. It returns LazyRecords — only index fields are decoded eagerly;
// the full record body is decoded on demand via LazyRecord.Decode().
// This keeps startup under the 100ms budget for 100k-line stores. (REQ-TUI-04)
// It mirrors store.ListLive semantics without the advisory lock. (REQ-TUI-02)
func loadRecords(storeDir string) ([]LazyRecord, error) {
	path := filepath.Join(storeDir, "memory.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // empty store is valid
		}
		return nil, fmt.Errorf("loadRecords: open %s: %w", path, err)
	}
	defer f.Close()

	// 1 MB scanner buffer — handles very long records without truncation.
	buf := make([]byte, 1<<20)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(buf, 1<<20)

	type winner struct {
		idx indexLine
		raw []byte
	}
	byID := make(map[string]winner)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var idx indexLine
		if err := json.Unmarshal(line, &idx); err != nil {
			continue // skip malformed lines
		}
		if idx.SchemaVersion > store.StoreSupportedVersion {
			continue // skip future-schema lines
		}
		if idx.CanonicalID == "" {
			continue // skip schema_version header lines
		}
		if idx.Deleted {
			// If a tombstone arrives after a live record, remove it.
			delete(byID, idx.CanonicalID)
			continue
		}

		existing, ok := byID[idx.CanonicalID]
		if !ok || idx.Revision > existing.idx.Revision {
			// Copy line bytes — scanner reuses its buffer.
			lineCopy := make([]byte, len(line))
			copy(lineCopy, line)
			byID[idx.CanonicalID] = winner{idx: idx, raw: lineCopy}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("loadRecords: scan %s: %w", path, err)
	}

	// Collect and sort by CanonicalID for deterministic order.
	records := make([]LazyRecord, 0, len(byID))
	for _, w := range byID {
		records = append(records, LazyRecord{
			CanonicalID: w.idx.CanonicalID,
			Revision:    w.idx.Revision,
			Title:       w.idx.Title,
			Kind:        w.idx.Kind,
			Type:        w.idx.Type,
			rawLine:     w.raw,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CanonicalID < records[j].CanonicalID
	})
	return records, nil
}

// loadIndexFile reads index.json from storeDir and returns conflicts and sync
// state. It does NOT open the store (REQ-TUI-02).
func loadIndexFile(storeDir string) (*IndexSnapshot, error) {
	path := filepath.Join(storeDir, "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &IndexSnapshot{}, nil // no index yet is valid
		}
		return nil, fmt.Errorf("loadIndexFile: read %s: %w", path, err)
	}

	var snap IndexSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("loadIndexFile: parse %s: %w", path, err)
	}
	if snap.SyncState == nil {
		snap.SyncState = make(map[string]store.ProviderSyncState)
	}
	if snap.Conflicts == nil {
		snap.Conflicts = []store.ConflictEntry{}
	}
	return &snap, nil
}

// callStatusJSON shells out `<binary> --dir <dir> status --json` and unmarshals
// the response. The binary defaults to os.Args[0] (the running glia
// process) so the TUI always calls the same version it was launched from.
//
// Tests MUST inject a fake runner via dataLayer.runner — calling the test
// binary recursively would cause circular execution.
func (d *dataLayer) callStatusJSON(dir string) (*StatusJSON, error) {
	runner := d.runner
	if runner == nil {
		runner = defaultRunner
	}

	out, err := runner(os.Args[0], "--dir", dir, "status", "--json")
	if err != nil {
		// Non-zero exit is expected when providers are degraded (exit 1).
		// We still parse the JSON body — only a parse failure is fatal.
		// Use len(out)==0 rather than out==nil: cmd.Run returns []byte{} (not nil)
		// when the process exits non-zero with empty stdout.
		if len(out) == 0 {
			return nil, fmt.Errorf("callStatusJSON: run: %w", err)
		}
	}

	var result StatusJSON
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("callStatusJSON: parse output: %w", err)
	}
	return &result, nil
}
