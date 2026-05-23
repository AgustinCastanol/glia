package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// IndexSnapshot holds the index.json fields the TUI needs directly.
type IndexSnapshot struct {
	Conflicts []store.ConflictEntry              `json:"conflicts"`
	SyncState map[string]store.ProviderSyncState `json:"sync_state"`
}

// StatusJSON is the machine-readable payload emitted by `wrapper-mems status --json`.
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

// loadRecords reads memory.jsonl from storeDir, streaming line-by-line with a
// 1 MB buffer. It collapses duplicate canonical_ids, keeping the entry with the
// highest revision and dropping tombstones (deleted=true). This mirrors
// store.ListLive semantics without acquiring the advisory lock (REQ-TUI-02).
func loadRecords(storeDir string) ([]store.CanonicalRecord, error) {
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

	// Map from canonical_id → winning record.
	byID := make(map[string]store.CanonicalRecord)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Two-pass decode: first check schema_version to skip unsupported lines,
		// then full decode.
		var probe struct {
			SchemaVersion int    `json:"schema_version"`
			CanonicalID   string `json:"canonical_id"`
			Revision      int    `json:"revision"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue // skip malformed lines
		}
		if probe.SchemaVersion > store.StoreSupportedVersion {
			continue // skip future-schema lines
		}
		if probe.CanonicalID == "" {
			continue // skip schema_version header lines
		}

		var rec store.CanonicalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip malformed records
		}

		existing, ok := byID[rec.CanonicalID]
		if !ok || rec.Revision > existing.Revision {
			byID[rec.CanonicalID] = rec
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("loadRecords: scan %s: %w", path, err)
	}

	// Collect live records (non-deleted) then sort by CanonicalID for a
	// stable, deterministic order. Map iteration in Go is randomized, so
	// without an explicit sort the TUI list would flicker between renders.
	records := make([]store.CanonicalRecord, 0, len(byID))
	for _, r := range byID {
		if !r.Deleted {
			records = append(records, r)
		}
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
// the response. The binary defaults to os.Args[0] (the running wrapper-mems
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
