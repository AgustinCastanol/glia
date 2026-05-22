package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// executeInit runs the init command with rootFlags.dir set to dir,
// capturing stdout. Returns the error from Execute (nil on success).
func executeInit(t *testing.T, dir string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rootFlags.dir = dir
	initCmd.SetOut(&buf)
	err := initCmd.RunE(initCmd, nil)
	rootFlags.dir = ""
	return buf.String(), err
}

func TestInit_FreshDirectory(t *testing.T) {
	dir := t.TempDir()

	_, err := executeInit(t, dir)
	if err != nil {
		t.Fatalf("init returned error: %v", err)
	}

	// REQ-SE-01: .wrapper-mems/ directory created.
	storeDir := filepath.Join(dir, ".wrapper-mems")
	if fi, err := os.Stat(storeDir); err != nil || !fi.IsDir() {
		t.Errorf(".wrapper-mems/ not created")
	}

	// REQ-SE-01: memory.jsonl exists.
	if _, err := os.Stat(filepath.Join(storeDir, "memory.jsonl")); err != nil {
		t.Errorf("memory.jsonl not created: %v", err)
	}

	// REQ-SE-01: index.json exists.
	if _, err := os.Stat(filepath.Join(storeDir, "index.json")); err != nil {
		t.Errorf("index.json not created: %v", err)
	}

	// REQ-SE-03: config.yaml created.
	cfgPath := filepath.Join(storeDir, "config.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("config.yaml not created: %v", err)
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "mirror_engram:") {
		t.Errorf("config.yaml missing mirror_engram key, got:\n%s", data)
	}

	// REQ-SE-02: .gitignore contains required entries.
	giPath := filepath.Join(dir, ".gitignore")
	gi, _ := os.ReadFile(giPath)
	for _, entry := range gitignoreEntries {
		if !strings.Contains(string(gi), entry) {
			t.Errorf(".gitignore missing entry %q", entry)
		}
	}
}

func TestInit_Idempotent(t *testing.T) {
	dir := t.TempDir()

	// First init.
	if _, err := executeInit(t, dir); err != nil {
		t.Fatalf("first init: %v", err)
	}

	// Capture .gitignore before second run.
	giPath := filepath.Join(dir, ".gitignore")
	before, _ := os.ReadFile(giPath)

	// Second init — REQ-SE-04.
	if _, err := executeInit(t, dir); err != nil {
		t.Fatalf("second init: %v", err)
	}

	after, _ := os.ReadFile(giPath)
	if !bytes.Equal(before, after) {
		t.Errorf(".gitignore changed on second init\nbefore: %q\nafter:  %q", before, after)
	}

	// config.yaml unchanged (still present, no duplicate content).
	cfgPath := filepath.Join(dir, ".wrapper-mems", "config.yaml")
	cfg, _ := os.ReadFile(cfgPath)
	count := strings.Count(string(cfg), "mirror_engram:")
	if count != 1 {
		t.Errorf("config.yaml has %d mirror_engram: entries after second init (want 1)", count)
	}
}

func TestInit_GitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()

	// Pre-populate .gitignore with all required entries.
	gi := strings.Join(gitignoreEntries, "\n") + "\n"
	giPath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(giPath, []byte(gi), 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(giPath)

	if _, err := executeInit(t, dir); err != nil {
		t.Fatalf("init: %v", err)
	}

	after, _ := os.ReadFile(giPath)
	if !bytes.Equal(before, after) {
		t.Errorf(".gitignore was modified even though all entries were present\nbefore: %q\nafter:  %q", before, after)
	}
}

func TestInit_ConfigNotOverwritten(t *testing.T) {
	dir := t.TempDir()

	// First init creates config.
	if _, err := executeInit(t, dir); err != nil {
		t.Fatal(err)
	}

	// Overwrite config with custom content.
	cfgPath := filepath.Join(dir, ".wrapper-mems", "config.yaml")
	custom := "mirror_engram: true\nmirror_timeout_seconds: 60\n"
	if err := os.WriteFile(cfgPath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}

	// Second init — must not overwrite.
	if _, err := executeInit(t, dir); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(cfgPath)
	if string(data) != custom {
		t.Errorf("config.yaml overwritten on second init\ngot: %q\nwant: %q", data, custom)
	}
}
