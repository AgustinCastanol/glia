package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agustincastanol/wrapper-mems/internal/store"
	enginesync "github.com/agustincastanol/wrapper-mems/internal/sync"
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

	sp := dir + "/.wrapper-mems"
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

	sp := dir + "/.wrapper-mems"
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

	sp := dir + "/.wrapper-mems"
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
