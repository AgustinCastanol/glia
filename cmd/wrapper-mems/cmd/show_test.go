package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// seedStore initialises a store at dir/.wrapper-mems and appends records.
// Returns the store path.
func seedStore(t *testing.T, dir string, records []store.CanonicalRecord) string {
	t.Helper()
	sp := filepath.Join(dir, ".wrapper-mems")
	s, err := store.Open(sp)
	if err != nil {
		t.Fatalf("seedStore: open: %v", err)
	}
	defer s.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range records {
		if records[i].CreatedAt == "" {
			records[i].CreatedAt = now
		}
		if records[i].UpdatedAt == "" {
			records[i].UpdatedAt = now
		}
		if records[i].ContentFormat == "" {
			records[i].ContentFormat = "text"
		}
		if _, err := s.Append(records[i]); err != nil {
			t.Fatalf("seedStore: append: %v", err)
		}
	}
	return sp
}

// executeShow runs the show command with given args and returns stdout.
func executeShow(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rootFlags.dir = dir
	showCmd.SetOut(&buf)

	// Reset flags to defaults before each test.
	showFlags.kind = ""
	showFlags.typ = ""
	showFlags.asJSON = false

	// Parse flags from args.
	if err := showCmd.ParseFlags(args); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	runShow(showCmd, nil)
	rootFlags.dir = ""
	return buf.String(), nil
}

func TestShow_TableDefault(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "hello world", Type: "note"},
	})

	out, _ := executeShow(t, dir)

	if !strings.Contains(out, "ID") {
		t.Errorf("expected table header with ID column, got:\n%s", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("expected title in output, got:\n%s", out)
	}
	if !strings.Contains(out, "observation") {
		t.Errorf("expected kind in output, got:\n%s", out)
	}
}

func TestShow_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "json test", Type: "note"},
	})

	var buf bytes.Buffer
	rootFlags.dir = dir
	showCmd.SetOut(&buf)
	showFlags.kind = ""
	showFlags.typ = ""
	showFlags.asJSON = true

	runShow(showCmd, nil)
	rootFlags.dir = ""
	showFlags.asJSON = false

	out := buf.String()
	// Should be valid JSON per line (REQ-SE-13).
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		t.Fatal("no output lines")
	}
	for _, line := range lines {
		var r store.CanonicalRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Errorf("invalid JSON line %q: %v", line, err)
		}
	}
	// Title must be present.
	if !strings.Contains(out, "json test") {
		t.Errorf("expected title in JSON output, got:\n%s", out)
	}
}

func TestShow_KindFilter(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "obs one", Type: "note"},
		{Kind: "session_summary", Title: "summary one", Type: "summary"},
	})

	var buf bytes.Buffer
	rootFlags.dir = dir
	showCmd.SetOut(&buf)
	showFlags.kind = "observation"
	showFlags.typ = ""
	showFlags.asJSON = false

	runShow(showCmd, nil)
	rootFlags.dir = ""
	showFlags.kind = ""

	out := buf.String()
	if !strings.Contains(out, "obs one") {
		t.Errorf("expected 'obs one' in output")
	}
	if strings.Contains(out, "summary one") {
		t.Errorf("expected 'summary one' to be filtered out, got:\n%s", out)
	}
}

func TestShow_TypeFilter(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: "alpha", Type: "bugfix"},
		{Kind: "observation", Title: "beta", Type: "decision"},
	})

	var buf bytes.Buffer
	rootFlags.dir = dir
	showCmd.SetOut(&buf)
	showFlags.kind = ""
	showFlags.typ = "bugfix"
	showFlags.asJSON = false

	runShow(showCmd, nil)
	rootFlags.dir = ""
	showFlags.typ = ""

	out := buf.String()
	if !strings.Contains(out, "alpha") {
		t.Errorf("expected 'alpha' in filtered output")
	}
	if strings.Contains(out, "beta") {
		t.Errorf("expected 'beta' to be filtered out, got:\n%s", out)
	}
}

func TestShow_TitleTruncatedAt60(t *testing.T) {
	dir := t.TempDir()
	longTitle := strings.Repeat("x", 80)
	seedStore(t, dir, []store.CanonicalRecord{
		{Kind: "observation", Title: longTitle, Type: "note"},
	})

	out, _ := executeShow(t, dir)
	// The truncated form must appear (first 57 chars + "...").
	truncated := longTitle[:57] + "..."
	if !strings.Contains(out, truncated) {
		t.Errorf("expected truncated title %q in output, got:\n%s", truncated, out)
	}
}

func TestShow_NoStoreExits(t *testing.T) {
	// Verify that show exits non-zero when no store exists.
	// We can't capture os.Exit directly; instead verify requireStore returns error.
	dir := t.TempDir() // no .wrapper-mems inside
	_, err := os.Stat(filepath.Join(dir, ".wrapper-mems"))
	if !os.IsNotExist(err) {
		t.Skip("unexpected .wrapper-mems present")
	}
	// requireStore should return errNoStore.
	rootFlags.dir = dir
	_, storeErr := requireStore(dir)
	rootFlags.dir = ""
	if storeErr == nil {
		t.Error("expected error from requireStore on uninitialised dir, got nil")
	}
}
