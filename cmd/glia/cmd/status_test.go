package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/config"
	"github.com/agustincastanol/glia/internal/store"
	enginesync "github.com/agustincastanol/glia/internal/sync"
)

// executeStatus runs the status command (runStatus) with the given dir and
// --conflicts flag, capturing stdout.
func executeStatus(t *testing.T, dir string, conflicts bool) string {
	t.Helper()
	var buf bytes.Buffer
	rootFlags.dir = dir
	statusCmd.SetOut(&buf)
	statusFlags.conflicts = conflicts

	runStatus(statusCmd, nil)

	rootFlags.dir = ""
	statusFlags.conflicts = false
	return buf.String()
}

// TestStatus_NoConflictsFlag checks that status without --conflicts does not
// print the conflict table.
func TestStatus_NoConflictsFlag(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	out := executeStatus(t, dir, false)
	// Provider health table should appear; conflict table should not.
	if strings.Contains(out, "CANONICAL_ID") {
		t.Errorf("expected no conflict table without --conflicts flag, got:\n%s", out)
	}
}

// TestStatus_ConflictsFlagNoConflicts checks "no conflicts" message when the
// --conflicts flag is set but there are no active conflicts (REQ-SE-36).
func TestStatus_ConflictsFlagNoConflicts(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "clean", Type: "note"},
	})

	out := executeStatus(t, dir, true)
	if !strings.Contains(out, "no conflicts") {
		t.Errorf("expected 'no conflicts' message, got:\n%s", out)
	}
}

// TestStatus_ConflictsFlagWithConflicts checks the conflict table columns when
// a ConflictEntry exists (REQ-SE-36).
func TestStatus_ConflictsFlagWithConflicts(t *testing.T) {
	dir := t.TempDir()
	_, canonicalID := seedConflict(t, dir)

	out := executeStatus(t, dir, true)

	// Table header columns must be present.
	for _, col := range []string{"CANONICAL_ID", "REVISION", "DUP_COUNT", "DETECTED_AT"} {
		if !strings.Contains(out, col) {
			t.Errorf("expected column %q in conflict table, got:\n%s", col, out)
		}
	}
	// The canonical_id must appear in the table body.
	if !strings.Contains(out, canonicalID) {
		t.Errorf("expected canonical_id %q in conflict table, got:\n%s", canonicalID, out)
	}
}

// TestStatus_ConflictTableColumns verifies the exact column set rendered by
// printConflictsTable against a StatusReport with one conflict (REQ-SE-36).
func TestStatus_ConflictTableColumns(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{},
		Conflicts: []enginesync.ConflictSummary{
			{
				CanonicalID: "test-canonical-id-abc",
				Revision:    3,
				DupCount:    2,
				DetectedAt:  now,
			},
		},
	}

	var buf bytes.Buffer
	printConflictsTable(&buf, report)
	out := buf.String()

	for _, want := range []string{"#", "CANONICAL_ID", "REVISION", "DUP_COUNT", "DETECTED_AT", "test-canonical-id-abc"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in conflict table output, got:\n%s", want, out)
		}
	}
}

// TestStatus_PrintConflictsTable_Empty checks that an empty conflict list
// prints "no conflicts".
func TestStatus_PrintConflictsTable_Empty(t *testing.T) {
	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{},
		Conflicts:      nil,
	}
	var buf bytes.Buffer
	printConflictsTable(&buf, report)
	if !strings.Contains(buf.String(), "no conflicts") {
		t.Errorf("expected 'no conflicts', got: %q", buf.String())
	}
}

// TestExitCode_ConflictsMapToTwo checks that errConflicts maps to exit code 2
// (REQ-SE-51, D6).
func TestExitCode_ConflictsMapToTwo(t *testing.T) {
	code := exitCode(errConflicts)
	if code != 2 {
		t.Errorf("expected exit code 2 for errConflicts, got %d", code)
	}
}

// TestExitCode_NilMapsToZero checks that a nil error maps to exit code 0.
func TestExitCode_NilMapsToZero(t *testing.T) {
	code := exitCode(nil)
	if code != 0 {
		t.Errorf("expected exit code 0 for nil, got %d", code)
	}
}

// TestExitCode_GenericErrorMapsToOne checks that a non-conflict error maps to
// exit code 1 (REQ-SE-51).
func TestExitCode_GenericErrorMapsToOne(t *testing.T) {
	code := exitCode(errNoStore)
	if code != 1 {
		t.Errorf("expected exit code 1 for errNoStore, got %d", code)
	}
}

// TestSyncExitErr_ConflictsPresentReturnsErrConflicts verifies that
// syncExitErr returns errConflicts when the store has unresolved conflicts
// (REQ-SE-51 scenario — conflicts produce exit 2).
func TestSyncExitErr_ConflictsPresentReturnsErrConflicts(t *testing.T) {
	dir := t.TempDir()
	_, canonicalID := seedConflict(t, dir)
	_ = canonicalID

	sp := dir + "/.glia"
	s, err := store.Open(sp)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	report := &enginesync.RunReport{
		PerProvider: map[string]enginesync.ProviderResult{"test": {}},
	}
	err = syncExitErr(s, report)
	if err != errConflicts {
		t.Errorf("expected errConflicts, got: %v", err)
	}
}

// TestSyncExitErr_NoConflictsReturnsNil verifies that syncExitErr returns nil
// when there are no conflicts and no hard errors.
func TestSyncExitErr_NoConflictsReturnsNil(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "clean", Type: "note"},
	})

	sp := dir + "/.glia"
	s, err := store.Open(sp)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	report := &enginesync.RunReport{
		PerProvider: map[string]enginesync.ProviderResult{"test": {}},
	}
	if err := syncExitErr(s, report); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
}

// TestEngineStatus_ConflictsSurfaced checks that Engine.Status() surfaces the
// conflict count from the store (REQ-SE-52).
func TestEngineStatus_ConflictsSurfaced(t *testing.T) {
	dir := t.TempDir()
	_, canonicalID := seedConflict(t, dir)

	sp := dir + "/.glia"
	s, err := store.Open(sp)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	engine := enginesync.New(s, nil, enginesync.Config{}, enginesync.Options{}, nil)
	report, err := engine.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(report.Conflicts) == 0 {
		t.Errorf("expected at least one conflict in StatusReport, got 0")
	}
	found := false
	for _, c := range report.Conflicts {
		if c.CanonicalID == canonicalID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("canonical_id %q not found in StatusReport.Conflicts", canonicalID)
	}
}

// --- --json flag tests (T-04) ---

// openStoreForTest opens the store at dir/.glia and returns it.
// The caller must defer s.Close().
func openStoreForTest(t *testing.T, dir string) *store.Store {
	t.Helper()
	sp := filepath.Join(dir, ".glia")
	s, err := store.Open(sp)
	if err != nil {
		t.Fatalf("openStoreForTest: %v", err)
	}
	return s
}

// TestStatusJSON_KeysPresent verifies that buildStatusJSON emits all required
// top-level keys for a healthy store (T-04).
func TestStatusJSON_KeysPresent(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{"engram": nil},
		Conflicts:      nil,
	}

	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	// Encode and decode to verify JSON marshaling.
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	for _, key := range []string{"provider_health", "conflicts", "sync_state", "line_count", "file_size_bytes", "schema_version"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in JSON output", key)
		}
	}
}

// TestStatusJSON_HealthyProviderEmptyString verifies that a healthy provider
// maps to an empty string in provider_health (T-04).
func TestStatusJSON_HealthyProviderEmptyString(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hi", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{"engram": nil},
	}
	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	if v, ok := out.ProviderHealth["engram"]; !ok || v != "" {
		t.Errorf("healthy provider: want empty string, got %q", v)
	}
}

// TestStatusJSON_DegradedProviderHasErrorString verifies that a degraded
// provider maps to its error message in provider_health (T-04).
func TestStatusJSON_DegradedProviderHasErrorString(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hi", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{
			"engram": errors.New("connection refused"),
		},
	}
	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	if v := out.ProviderHealth["engram"]; v != "connection refused" {
		t.Errorf("degraded provider: want error string, got %q", v)
	}
}

// TestStatusJSON_ConflictsNeverNil verifies that the conflicts field is always
// a JSON array (never null) even when there are no conflicts (T-04).
func TestStatusJSON_ConflictsNeverNil(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hi", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{},
		Conflicts:      nil,
	}
	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if out.Conflicts == nil {
		t.Error("conflicts: want non-nil slice (JSON array), got nil")
	}
}

// TestStatusJSON_StoreStatsPopulated verifies that line_count and
// file_size_bytes are non-zero for a non-empty store (T-04).
func TestStatusJSON_StoreStatsPopulated(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "a", Type: "note"},
		{Kind: "observation", Title: "b", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{},
	}
	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	if out.LineCount == 0 {
		t.Error("line_count: want > 0")
	}
	if out.FileSizeBytes == 0 {
		t.Error("file_size_bytes: want > 0")
	}
	if out.SchemaVersion == 0 {
		t.Error("schema_version: want > 0")
	}
}

// ---------------------------------------------------------------------------
// Phase 8 — write_capability in status output (REQ-CMW-09)
// ---------------------------------------------------------------------------

// TestStatus_TableIncludesWriteCapabilityColumn verifies that the status table
// header contains a WRITE_CAPABILITY column (REQ-CMW-09).
func TestStatus_TableIncludesWriteCapabilityColumn(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	out := executeStatus(t, dir, false)
	if !strings.Contains(out, "WRITE_CAPABILITY") {
		t.Errorf("expected WRITE_CAPABILITY column in status table, got:\n%s", out)
	}
}

// TestStatusJSON_WriteCapabilityPresent verifies that buildStatusJSON includes
// the write_capability map when ProviderWriteCapability is set (REQ-CMW-09).
func TestStatusJSON_WriteCapabilityPresent(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{"engram": nil},
		ProviderWriteCapability: map[string]string{
			"engram": "read+write",
		},
	}

	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := m["write_capability"]; !ok {
		t.Error("expected 'write_capability' key in JSON output")
	}
}

// ---------------------------------------------------------------------------
// Phase 5 — effective_project per provider in status output (PRD-6)
// ---------------------------------------------------------------------------

// TestStatus_TableIncludesEffectiveProjectColumn verifies that the status
// table header contains an EFFECTIVE_PROJECT column (PRD-6).
func TestStatus_TableIncludesEffectiveProjectColumn(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	out := executeStatus(t, dir, false)
	if !strings.Contains(out, "EFFECTIVE_PROJECT") {
		t.Errorf("expected EFFECTIVE_PROJECT column in status table, got:\n%s", out)
	}
}

// TestStatus_ResolvedEffectiveProjectFlowsToJSON is the PRD-6 happy-path
// integration test at the wiring layer: a real .glia/config.yaml with a
// per-provider override is loaded, buildAdapters is called, the resulting
// adapters' EffectiveProject() values are collected, and buildStatusJSON
// surfaces them in the rendered output.
//
// This stops short of executeStatus because runStatus performs live Health
// and WriteSupported probes against provider endpoints that are not present
// in CI; the resolution chain itself is what we care about here.
func TestStatus_ResolvedEffectiveProjectFlowsToJSON(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	storeDir := filepath.Join(dir, ".glia")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir storeDir: %v", err)
	}
	cfgYAML := "" +
		"schema_version: 1\n" +
		"project: global-fallback\n" +
		"providers:\n" +
		"  engram:\n" +
		"    enabled: true\n" +
		"    transport: http\n" +
		"    project: engram-override\n" +
		"  claude-mem:\n" +
		"    enabled: true\n" +
		"    transport: http\n"
	if err := os.WriteFile(filepath.Join(storeDir, "config.yaml"), []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(dir, "/nonexistent/user.yaml")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	adapters, err := buildAdapters(cfg, "", "")
	if err != nil {
		t.Fatalf("buildAdapters: %v", err)
	}

	type projecter interface{ EffectiveProject() string }
	effectiveProjects := make(map[string]string, len(adapters))
	for name, a := range adapters {
		if ep, ok := a.(projecter); ok {
			effectiveProjects[name] = ep.EffectiveProject()
		}
	}

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{"engram": nil, "claude-mem": nil},
	}
	out, err := buildStatusJSON(s, report, effectiveProjects)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	if got := out.EffectiveProject["engram"]; got != "engram-override" {
		t.Errorf("effective_project[engram] = %q, want %q (per-provider override)", got, "engram-override")
	}
	if got := out.EffectiveProject["claude-mem"]; got != "global-fallback" {
		t.Errorf("effective_project[claude-mem] = %q, want %q (global fallback)", got, "global-fallback")
	}
}

// TestStatusJSON_EffectiveProjectPresent verifies that buildStatusJSON includes
// the effective_project map when adapters expose EffectiveProject() (PRD-6).
func TestStatusJSON_EffectiveProjectPresent(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{"engram": nil},
	}

	out, err := buildStatusJSON(s, report, map[string]string{"engram": "my-project"})
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := m["effective_project"]; !ok {
		t.Error("expected 'effective_project' key in JSON output")
	}
}

// TestStatusJSON_EffectiveProjectValue verifies that the effective_project map
// carries the per-provider project values passed in (PRD-6).
func TestStatusJSON_EffectiveProjectValue(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{
			"engram":     nil,
			"claude-mem": nil,
		},
	}

	effectiveProjects := map[string]string{
		"engram":     "eng-project",
		"claude-mem": "",
	}
	out, err := buildStatusJSON(s, report, effectiveProjects)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	if v := out.EffectiveProject["engram"]; v != "eng-project" {
		t.Errorf("effective_project[engram] = %q, want %q", v, "eng-project")
	}
	if v := out.EffectiveProject["claude-mem"]; v != "" {
		t.Errorf("effective_project[claude-mem] = %q, want empty string", v)
	}
}

// TestStatusJSON_WriteCapabilityValue verifies the write_capability value for
// a provider is correctly propagated to the JSON output (REQ-CMW-09).
func TestStatusJSON_WriteCapabilityValue(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{"claude-mem": nil},
		ProviderWriteCapability: map[string]string{
			"claude-mem": "read-only (write_enabled=false)",
		},
	}

	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	cap, ok := out.WriteCapability["claude-mem"]
	if !ok {
		t.Fatal("write_capability map missing claude-mem entry")
	}
	if cap != "read-only (write_enabled=false)" {
		t.Errorf("write_capability[claude-mem] = %q, want %q", cap, "read-only (write_enabled=false)")
	}
}

// ---------------------------------------------------------------------------
// Task 6 — sources block in status output (PRD-11 §10)
// ---------------------------------------------------------------------------

// TestStatusJSON_SourcesKeyPresent verifies that buildStatusJSON includes a
// "sources" key in JSON output when sources entries are populated.
func TestStatusJSON_SourcesKeyPresent(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hi", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{},
		Sources: []enginesync.SourceStatus{
			{Name: "openspec", WriteCapability: "read-only", Healthy: true},
		},
	}

	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := m["sources"]; !ok {
		t.Error("expected 'sources' key in JSON output")
	}
}

// TestStatusJSON_SourcesEntryHasOpenspecFields verifies the openspec source
// entry carries name, write_capability, and healthy fields.
func TestStatusJSON_SourcesEntryHasOpenspecFields(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hi", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{},
		Sources: []enginesync.SourceStatus{
			{
				Name:            "openspec",
				WriteCapability: "read-only",
				Healthy:         true,
				ArtifactCount:   3,
				NewestArtifact:  "2026-06-12T00:00:00Z",
			},
		},
	}

	out, err := buildStatusJSON(s, report, nil)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	if len(out.Sources) != 1 {
		t.Fatalf("want 1 source entry, got %d", len(out.Sources))
	}
	src := out.Sources[0]
	if src.Name != "openspec" {
		t.Errorf("source name: want %q, got %q", "openspec", src.Name)
	}
	if src.WriteCapability != "read-only" {
		t.Errorf("write_capability: want %q, got %q", "read-only", src.WriteCapability)
	}
	if !src.Healthy {
		t.Error("healthy: want true")
	}
	if src.ArtifactCount != 3 {
		t.Errorf("artifact_count: want 3, got %d", src.ArtifactCount)
	}
	if src.NewestArtifact != "2026-06-12T00:00:00Z" {
		t.Errorf("newest_artifact: want %q, got %q", "2026-06-12T00:00:00Z", src.NewestArtifact)
	}
}

// TestStatus_TableHasSourcesBlock verifies that when sources are present the
// table output contains a SOURCES section distinct from the PROVIDER table.
func TestStatus_TableHasSourcesBlock(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hi", Type: "note"},
	})

	s := openStoreForTest(t, dir)
	defer s.Close()

	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{},
		Sources: []enginesync.SourceStatus{
			{Name: "openspec", WriteCapability: "read-only", Healthy: true},
		},
	}

	var buf strings.Builder
	printSourcesTable(&buf, report)
	out := buf.String()

	if !strings.Contains(out, "SOURCE") {
		t.Errorf("expected SOURCE column header in sources table, got:\n%s", out)
	}
	if !strings.Contains(out, "openspec") {
		t.Errorf("expected 'openspec' in sources table, got:\n%s", out)
	}
	if !strings.Contains(out, "read-only") {
		t.Errorf("expected 'read-only' in sources table, got:\n%s", out)
	}
}

// TestStatus_TableNoSourcesBlock verifies printSourcesTable produces no output
// when the Sources slice is empty.
func TestStatus_TableNoSourcesBlock(t *testing.T) {
	report := &enginesync.StatusReport{
		ProviderHealth: map[string]error{},
		Sources:        nil,
	}
	var buf strings.Builder
	printSourcesTable(&buf, report)
	if buf.String() != "" {
		t.Errorf("expected empty output for no sources, got: %q", buf.String())
	}
}
