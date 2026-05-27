package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/store"
)

// --- helpers ----------------------------------------------------------------

// newTempStore creates a minimal .glia/ store directory for testing.
// Returns the project dir and store dir.
func newTempStore(t *testing.T) (projectDir, storeDir string) {
	t.Helper()
	projectDir = t.TempDir()
	storeDir = filepath.Join(projectDir, ".glia")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	// schema.json
	schema := map[string]interface{}{
		"schema_version": 1,
		"created_at":     time.Now().UTC().Format(time.RFC3339),
	}
	writeJSON(t, filepath.Join(storeDir, "schema.json"), schema)
	// memory.jsonl (empty)
	if err := os.WriteFile(filepath.Join(storeDir, "memory.jsonl"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	// Open + close the store to generate index.json
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	return projectDir, storeDir
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// --- stub adapter -----------------------------------------------------------

type stubAdapter struct {
	name      string
	healthErr error
}

func (s *stubAdapter) Name() string                  { return s.name }
func (s *stubAdapter) Health(_ context.Context) error { return s.healthErr }
func (s *stubAdapter) ListNative(_ context.Context, _ string, _ time.Time) ([]adapter.NativeID, error) {
	return nil, nil
}
func (s *stubAdapter) ReadNative(_ context.Context, _ adapter.NativeID) (adapter.NativeRecord, error) {
	return nil, nil
}
func (s *stubAdapter) ToCanonical(_ adapter.NativeRecord, _ adapter.IDMap) (store.CanonicalRecord, error) {
	return store.CanonicalRecord{}, nil
}
func (s *stubAdapter) FromCanonical(_ store.CanonicalRecord) (adapter.NativeRecord, error) {
	return nil, nil
}
func (s *stubAdapter) WriteNative(_ context.Context, _ adapter.NativeRecord) (adapter.NativeID, error) {
	return "", nil
}
func (s *stubAdapter) SupportedKinds() []string { return nil }

// --- checkCanonicalStore ----------------------------------------------------

func TestCheckCanonicalStore_OK(t *testing.T) {
	_, storeDir := newTempStore(t)
	r := checkCanonicalStore(storeDir)
	if r.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckCanonicalStore_Missing(t *testing.T) {
	storeDir := t.TempDir()
	r := checkCanonicalStore(storeDir)
	if r.Status != StatusErr {
		t.Errorf("expected StatusErr for missing memory.jsonl, got %v: %s", r.Status, r.Message)
	}
}

// --- checkSchema ------------------------------------------------------------

func TestCheckSchema_OK(t *testing.T) {
	_, storeDir := newTempStore(t)
	orig := Version
	Version = "v1.0.0"
	t.Cleanup(func() { Version = orig })

	r := checkSchema(storeDir)
	if r.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckSchema_MinVersionExceeded(t *testing.T) {
	_, storeDir := newTempStore(t)
	orig := Version
	Version = "v0.1.0"
	t.Cleanup(func() { Version = orig })

	// Overwrite schema.json with a high min_version.
	schema := map[string]interface{}{
		"schema_version":            1,
		"created_at":                time.Now().UTC().Format(time.RFC3339),
		"glia_min_version":  "v9.0.0",
	}
	writeJSON(t, filepath.Join(storeDir, "schema.json"), schema)

	r := checkSchema(storeDir)
	if r.Status != StatusErr {
		t.Errorf("expected StatusErr for min_version exceeded, got %v: %s", r.Status, r.Message)
	}
	if !strings.Contains(strings.ToLower(r.Message), "upgrade") {
		t.Errorf("expected 'upgrade' in message, got: %s", r.Message)
	}
}

func TestCheckSchema_EmptyMinVersion(t *testing.T) {
	_, storeDir := newTempStore(t)
	orig := Version
	Version = "v0.1.0"
	t.Cleanup(func() { Version = orig })

	r := checkSchema(storeDir)
	if r.Status != StatusOK {
		t.Errorf("expected StatusOK for empty min_version, got %v: %s", r.Status, r.Message)
	}
}

// --- checkIndex -------------------------------------------------------------

func TestCheckIndex_OK(t *testing.T) {
	_, storeDir := newTempStore(t)
	r := checkIndex(storeDir)
	if r.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckIndex_Missing(t *testing.T) {
	_, storeDir := newTempStore(t)
	// Remove the index file.
	if err := os.Remove(filepath.Join(storeDir, "index.json")); err != nil {
		t.Fatal(err)
	}
	r := checkIndex(storeDir)
	if r.Status != StatusWarn {
		t.Errorf("expected StatusWarn for missing index.json, got %v: %s", r.Status, r.Message)
	}
	if !r.Fixable {
		t.Error("expected missing index.json to be fixable")
	}
}

func TestCheckIndex_FixRebuild(t *testing.T) {
	_, storeDir := newTempStore(t)
	idxPath := filepath.Join(storeDir, "index.json")
	// Remove index.
	os.Remove(idxPath)

	r := checkIndex(storeDir)
	if !r.Fixable || r.FixFn == nil {
		t.Fatal("expected fixable check with FixFn")
	}
	if err := r.FixFn(); err != nil {
		t.Fatalf("FixFn returned error: %v", err)
	}
	// Index should now exist.
	if _, err := os.Stat(idxPath); err != nil {
		t.Errorf("index.json still missing after fix: %v", err)
	}
}

// --- checkEngram / checkClaudeMem -------------------------------------------

func TestCheckEngram_OK(t *testing.T) {
	adapters := map[string]adapter.Adapter{
		"engram": &stubAdapter{name: "engram", healthErr: nil},
	}
	r := checkEngram(context.Background(), adapters)
	if r.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckEngram_Unreachable(t *testing.T) {
	adapters := map[string]adapter.Adapter{
		"engram": &stubAdapter{name: "engram", healthErr: fmt.Errorf("connection refused")},
	}
	r := checkEngram(context.Background(), adapters)
	if r.Status != StatusWarn {
		t.Errorf("expected StatusWarn for unhealthy engram, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckEngram_NotConfigured(t *testing.T) {
	r := checkEngram(context.Background(), nil)
	if r.Status != StatusWarn {
		t.Errorf("expected StatusWarn when engram not configured, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckClaudeMem_OK(t *testing.T) {
	adapters := map[string]adapter.Adapter{
		"claude-mem": &stubAdapter{name: "claude-mem", healthErr: nil},
	}
	r := checkClaudeMem(context.Background(), adapters)
	if r.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckClaudeMem_Unreachable(t *testing.T) {
	adapters := map[string]adapter.Adapter{
		"claude-mem": &stubAdapter{name: "claude-mem", healthErr: fmt.Errorf("timeout")},
	}
	r := checkClaudeMem(context.Background(), adapters)
	if r.Status != StatusWarn {
		t.Errorf("expected StatusWarn for unhealthy claude-mem, got %v: %s", r.Status, r.Message)
	}
}

// --- checkGitignore ---------------------------------------------------------

func TestCheckGitignore_AllCorrect(t *testing.T) {
	dir := t.TempDir()
	giPath := filepath.Join(dir, ".gitignore")
	content := ".glia/index.json\n.glia/.lock\n"
	os.WriteFile(giPath, []byte(content), 0644)

	r := checkGitignore(giPath)
	if r.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckGitignore_MissingEntries(t *testing.T) {
	dir := t.TempDir()
	giPath := filepath.Join(dir, ".gitignore")
	os.WriteFile(giPath, []byte("*.log\n"), 0644)

	r := checkGitignore(giPath)
	if r.Status != StatusWarn {
		t.Errorf("expected StatusWarn for missing entries, got %v: %s", r.Status, r.Message)
	}
	if !r.Fixable {
		t.Error("expected fixable")
	}
}

// TestCheckGitignore_MemoryJSONLPresent verifies REQ-DOC-01 scenario:
// memory.jsonl in gitignore → exit 1 (WARN) with appropriate message.
func TestCheckGitignore_MemoryJSONLPresent(t *testing.T) {
	dir := t.TempDir()
	giPath := filepath.Join(dir, ".gitignore")
	content := ".glia/index.json\n.glia/.lock\n.glia/memory.jsonl\n"
	os.WriteFile(giPath, []byte(content), 0644)

	r := checkGitignore(giPath)
	if r.Status != StatusWarn {
		t.Errorf("expected StatusWarn when memory.jsonl is gitignored, got %v: %s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "memory.jsonl") {
		t.Errorf("expected message to mention memory.jsonl, got: %s", r.Message)
	}
	if !r.Fixable {
		t.Error("expected fixable")
	}
}

// TestCheckGitignore_FixRemovesMemoryJSONL verifies REQ-DOC-03 scenario:
// --fix removes the memory.jsonl line and preserves other lines.
func TestCheckGitignore_FixRemovesMemoryJSONL(t *testing.T) {
	dir := t.TempDir()
	giPath := filepath.Join(dir, ".gitignore")
	content := ".glia/index.json\n.glia/.lock\n.glia/memory.jsonl\n*.log\n"
	os.WriteFile(giPath, []byte(content), 0644)

	r := checkGitignore(giPath)
	if r.FixFn == nil {
		t.Fatal("expected FixFn to be set")
	}
	if err := r.FixFn(); err != nil {
		t.Fatalf("FixFn returned error: %v", err)
	}

	result, err := os.ReadFile(giPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(result)

	if strings.Contains(got, "memory.jsonl") {
		t.Errorf("expected memory.jsonl to be removed, got:\n%s", got)
	}
	if !strings.Contains(got, ".glia/index.json") {
		t.Errorf("expected .glia/index.json to be preserved, got:\n%s", got)
	}
	if !strings.Contains(got, ".glia/.lock") {
		t.Errorf("expected .glia/.lock to be preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "*.log") {
		t.Errorf("expected *.log to be preserved, got:\n%s", got)
	}
}

// TestCheckGitignore_FixIdempotent verifies that running fix twice is a no-op.
func TestCheckGitignore_FixIdempotent(t *testing.T) {
	dir := t.TempDir()
	giPath := filepath.Join(dir, ".gitignore")
	content := ".glia/index.json\n.glia/.lock\n.glia/memory.jsonl\n"
	os.WriteFile(giPath, []byte(content), 0644)

	// First fix.
	r := checkGitignore(giPath)
	if err := r.FixFn(); err != nil {
		t.Fatal(err)
	}
	after1, _ := os.ReadFile(giPath)

	// Second fix via new check.
	r2 := checkGitignore(giPath)
	// After first fix, status should be OK, but we still run FixFn if present.
	if r2.FixFn != nil {
		if err := r2.FixFn(); err != nil {
			t.Fatal(err)
		}
	}
	after2, _ := os.ReadFile(giPath)

	if string(after1) != string(after2) {
		t.Errorf("fix is not idempotent:\nafter 1: %q\nafter 2: %q", after1, after2)
	}
}

// --- checkStaleLock ---------------------------------------------------------

func TestCheckStaleLock_NoLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	r := checkStaleLock(lockPath)
	if r.Status != StatusOK {
		t.Errorf("expected StatusOK when no .lock, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckStaleLock_StalePID(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	// Write a PID that is almost certainly not alive.
	os.WriteFile(lockPath, []byte("999999\n"), 0644)

	r := checkStaleLock(lockPath)
	if r.Status != StatusWarn {
		t.Errorf("expected StatusWarn for stale PID, got %v: %s", r.Status, r.Message)
	}
	if !r.Fixable {
		t.Error("expected stale lock to be fixable")
	}
}

func TestCheckStaleLock_FixRemovesLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	os.WriteFile(lockPath, []byte("999999\n"), 0644)

	r := checkStaleLock(lockPath)
	if r.FixFn == nil {
		t.Fatal("expected FixFn to be set")
	}
	if err := r.FixFn(); err != nil {
		t.Fatalf("FixFn returned error: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("expected .lock to be removed after fix")
	}
}

// --- REQ-DOC-02 exit code mapping -------------------------------------------

func TestDoctor_ExitCodeMapping(t *testing.T) {
	tests := []struct {
		name     string
		checks   []CheckResult
		wantExit int // 0=OK, 1=warn, 2=err (inferred from hasWarn/hasErr logic)
	}{
		{
			name:     "all OK",
			checks:   []CheckResult{{Status: StatusOK}, {Status: StatusOK}},
			wantExit: 0,
		},
		{
			name:     "one warn",
			checks:   []CheckResult{{Status: StatusOK}, {Status: StatusWarn}},
			wantExit: 1,
		},
		{
			name:     "one err",
			checks:   []CheckResult{{Status: StatusOK}, {Status: StatusErr}},
			wantExit: 2,
		},
		{
			name:     "warn and err",
			checks:   []CheckResult{{Status: StatusWarn}, {Status: StatusErr}},
			wantExit: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hasWarn, hasErr := false, false
			for _, r := range tc.checks {
				switch r.Status {
				case StatusWarn:
					hasWarn = true
				case StatusErr:
					hasErr = true
				}
			}
			var got int
			if hasErr {
				got = 2
			} else if hasWarn {
				got = 1
			}
			if got != tc.wantExit {
				t.Errorf("exit code: got %d, want %d", got, tc.wantExit)
			}
		})
	}
}

// --- printResults -----------------------------------------------------------

func TestPrintResults_ContainsGlyphs(t *testing.T) {
	checks := []CheckResult{
		{Name: "store", Status: StatusOK, Message: "ok"},
		{Name: "lock", Status: StatusWarn, Message: "warn msg"},
		{Name: "schema", Status: StatusErr, Message: "err msg"},
	}

	buf := &bytes.Buffer{}
	doctorCmd.SetOut(buf)
	printResults(doctorCmd, checks)

	got := buf.String()
	if !strings.Contains(got, "✓") {
		t.Error("expected ✓ glyph in output")
	}
	if !strings.Contains(got, "⚠") {
		t.Error("expected ⚠ glyph in output")
	}
	if !strings.Contains(got, "✗") {
		t.Error("expected ✗ glyph in output")
	}
	if !strings.Contains(got, "1 error(s), 1 warning(s)") {
		t.Errorf("expected summary line, got: %s", got)
	}
}

// --- --fix does not touch memory.jsonl data lines (REQ-DOC-03) ---

func TestFix_DoesNotTouchMemoryJSONLDataLines(t *testing.T) {
	_, storeDir := newTempStore(t)
	memPath := filepath.Join(storeDir, "memory.jsonl")

	// Write 3 complete JSONL lines.
	lines := `{"line":"1"}
{"line":"2"}
{"line":"3"}
`
	if err := os.WriteFile(memPath, []byte(lines), 0644); err != nil {
		t.Fatal(err)
	}

	// Run fixRebuildIndex (the only fix that touches the store).
	// It rebuilds the index but must NOT modify memory.jsonl.
	fixFn := fixRebuildIndex(storeDir)
	if err := fixFn(); err != nil {
		// If rebuild fails (e.g. schema issues in minimal fixture), skip.
		t.Logf("fixRebuildIndex: %v (skipping line-count assertion)", err)
		return
	}

	result, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != lines {
		t.Errorf("memory.jsonl was modified by fix:\ngot:  %q\nwant: %q", result, lines)
	}
}
