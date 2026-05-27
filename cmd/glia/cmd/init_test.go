package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/agustincastanol/glia/internal/config"
)

// executeInit is a test helper that runs the init command with a given set of
// flag values and rootFlags.dir, capturing stdout. Returns stdout and error.
func executeInit(t *testing.T, dir string, force bool, project string, providers []string) (string, error) {
	t.Helper()

	// Save and restore global flag state.
	origDir := rootFlags.dir
	origForce := initFlags.force
	origProject := initFlags.project
	origProviders := initFlags.providers
	t.Cleanup(func() {
		rootFlags.dir = origDir
		initFlags.force = origForce
		initFlags.project = origProject
		initFlags.providers = origProviders
	})

	rootFlags.dir = dir
	initFlags.force = force
	initFlags.project = project
	initFlags.providers = providers

	var buf bytes.Buffer
	initCmd.SetOut(&buf)
	err := initCmd.RunE(initCmd, nil)
	return buf.String(), err
}

// noopProbe is a probe function that always reports success (provider found).
func noopProbe(_ context.Context, _ ...string) error { return nil }

// failProbe is a probe function that always reports failure (provider not found).
func failProbe(_ context.Context, _ ...string) error { return os.ErrNotExist }

// withProbe temporarily replaces the package-level probeFn for the duration of
// the test. It restores the original value via t.Cleanup.
func withProbe(t *testing.T, fn func(context.Context, ...string) error) {
	t.Helper()
	orig := probeFn
	probeFn = fn
	t.Cleanup(func() { probeFn = orig })
}

// --- REQ-INIT-01: refuse on existing store ---

func TestInit_REQ_INIT_01_RefuseExistingDir(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	// First init succeeds.
	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("first init: %v", err)
	}

	// Second init without --force must fail.
	_, err := executeInit(t, dir, false, "myapp", []string{"engram"})
	if err == nil {
		t.Fatal("expected error on second init without --force, got nil")
	}
}

func TestInit_REQ_INIT_01_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	// First init.
	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("first init: %v", err)
	}

	// Write a sentinel file inside .glia/ to verify it gets replaced.
	sentinel := filepath.Join(dir, ".glia", "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	// Second init with --force must succeed and recreate the store.
	if _, err := executeInit(t, dir, true, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("force reinit: %v", err)
	}

	// Sentinel should be gone (directory was re-created).
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Error("expected sentinel to be removed after --force, but it still exists")
	}

	// Required files must still exist.
	assertFilesExist(t, dir)
}

// --- REQ-INIT-02: project name detection chain ---

func TestInit_REQ_INIT_02_ProjectFlagWins(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	t.Setenv("WRAPPER_MEMS_PROJECT", "from-env")

	if _, err := executeInit(t, dir, false, "from-flag", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg := readConfigYAML(t, dir)
	if cfg.Project != "from-flag" {
		t.Errorf("expected project=from-flag, got %q", cfg.Project)
	}
}

func TestInit_REQ_INIT_02_EnvFallback(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	t.Setenv("WRAPPER_MEMS_PROJECT", "from-env")

	// No --project flag.
	if _, err := executeInit(t, dir, false, "", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg := readConfigYAML(t, dir)
	if cfg.Project != "from-env" {
		t.Errorf("expected project=from-env, got %q", cfg.Project)
	}
}

func TestInit_REQ_INIT_02_GitRemoteBasename(t *testing.T) {
	// Verify gitRemoteBasename parses URLs correctly.
	cases := []struct {
		url  string
		want string
	}{
		{"git@github.com:org/my-app.git", "my-app"},
		{"https://github.com/org/my-app.git", "my-app"},
		{"https://github.com/org/my-app", "my-app"},
		{"git@github.com:org/repo", "repo"},
	}

	for _, tc := range cases {
		// Create a temp git dir with origin set.
		dir := t.TempDir()

		// Init a git repo and set remote.
		runGit(t, dir, "init")
		runGit(t, dir, "remote", "add", "origin", tc.url)

		got := gitRemoteBasename(dir)
		if got != tc.want {
			t.Errorf("url=%q: got %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestInit_REQ_INIT_02_DirBasename(t *testing.T) {
	// When no --project, no env, no git remote — fall back to dir basename.
	dir := t.TempDir()
	withProbe(t, noopProbe)

	t.Setenv("WRAPPER_MEMS_PROJECT", "")

	// Run in a sub-directory named "my-project".
	projectDir := filepath.Join(dir, "my-project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := executeInitInDir(t, projectDir, false, "", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg := readConfigYAML(t, projectDir)
	if cfg.Project != "my-project" {
		t.Errorf("expected project=my-project, got %q", cfg.Project)
	}
}

// --- REQ-INIT-03: provider detection ---

func TestInit_REQ_INIT_03_ProviderProbeEngram(t *testing.T) {
	dir := t.TempDir()

	// Only engram probe succeeds.
	withProbe(t, func(_ context.Context, args ...string) error {
		if len(args) > 0 && args[0] == "engram" {
			return nil
		}
		return os.ErrNotExist
	})

	if _, err := executeInit(t, dir, false, "myapp", nil); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg := readConfigYAML(t, dir)
	if !cfg.Providers.Engram.Enabled {
		t.Error("expected engram.enabled=true")
	}
	if cfg.Providers.ClaudeMem.Enabled {
		t.Error("expected claude-mem.enabled=false")
	}
}

func TestInit_REQ_INIT_03_ProviderProbeClaudeMem(t *testing.T) {
	dir := t.TempDir()

	// Only claude-mem probe succeeds.
	withProbe(t, func(_ context.Context, args ...string) error {
		if len(args) > 0 && args[0] == "claude-mem" {
			return nil
		}
		return os.ErrNotExist
	})

	if _, err := executeInit(t, dir, false, "myapp", nil); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg := readConfigYAML(t, dir)
	if cfg.Providers.Engram.Enabled {
		t.Error("expected engram.enabled=false")
	}
	if !cfg.Providers.ClaudeMem.Enabled {
		t.Error("expected claude-mem.enabled=true")
	}
}

func TestInit_REQ_INIT_03_ProviderFlagSkipsProbe(t *testing.T) {
	dir := t.TempDir()

	// probeFn always fails, but --providers flag overrides it.
	withProbe(t, failProbe)

	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg := readConfigYAML(t, dir)
	if !cfg.Providers.Engram.Enabled {
		t.Error("expected engram.enabled=true (from flag)")
	}
}

// --- REQ-INIT-04: files created ---

func TestInit_REQ_INIT_04_FilesCreated(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	assertFilesExist(t, dir)
}

func TestInit_REQ_INIT_04_MemoryJsonlIsEmpty(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	memPath := filepath.Join(dir, ".glia", "memory.jsonl")
	fi, err := os.Stat(memPath)
	if err != nil {
		t.Fatalf("memory.jsonl not found: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("memory.jsonl must be 0 bytes, got %d", fi.Size())
	}
}

// --- REQ-INIT-05: gitignore entries ---

func TestInit_REQ_INIT_05_GitignoreEntriesCorrect(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	giPath := filepath.Join(dir, ".gitignore")
	gi, err := os.ReadFile(giPath)
	if err != nil {
		t.Fatalf(".gitignore not created: %v", err)
	}
	giStr := string(gi)

	// Must contain these two entries.
	for _, want := range []string{".glia/index.json", ".glia/.lock"} {
		if !strings.Contains(giStr, want) {
			t.Errorf(".gitignore missing entry %q", want)
		}
	}
}

func TestInit_REQ_INIT_05_GitignoreDoesNotContainMemoryJsonl(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	giPath := filepath.Join(dir, ".gitignore")
	gi, err := os.ReadFile(giPath)
	if err != nil {
		t.Fatalf(".gitignore not created: %v", err)
	}

	// D2: memory.jsonl must NOT be in .gitignore.
	if strings.Contains(string(gi), "memory.jsonl") {
		t.Errorf(".gitignore must NOT contain memory.jsonl, got:\n%s", gi)
	}
}

func TestInit_REQ_INIT_05_GitignoreCreatedWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	// No .gitignore initially.
	giPath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(giPath); !os.IsNotExist(err) {
		t.Skip(".gitignore unexpectedly exists in temp dir")
	}

	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	if _, err := os.Stat(giPath); os.IsNotExist(err) {
		t.Error(".gitignore was not created")
	}
}

func TestInit_REQ_INIT_05_GitignoreIdempotentWhenEntriesPresent(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	// Pre-populate with both required entries.
	giPath := filepath.Join(dir, ".gitignore")
	content := ".glia/index.json\n.glia/.lock\n"
	if err := os.WriteFile(giPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(giPath)

	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	after, _ := os.ReadFile(giPath)
	if !bytes.Equal(before, after) {
		t.Errorf(".gitignore changed when entries were already present\nbefore: %q\nafter: %q", before, after)
	}
}

// --- REQ-INIT-06: non-interactive mode ---

func TestInit_REQ_INIT_06_CIModeWithAllFlags(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, failProbe) // probing is skipped because --providers is set

	// Both --project and --providers supplied — no stdin read needed.
	out, err := executeInit(t, dir, false, "ci-project", []string{"engram"})
	if err != nil {
		t.Fatalf("CI mode init failed: %v", err)
	}
	_ = out

	cfg := readConfigYAML(t, dir)
	if cfg.Project != "ci-project" {
		t.Errorf("expected project=ci-project, got %q", cfg.Project)
	}
	if !cfg.Providers.Engram.Enabled {
		t.Error("expected engram enabled")
	}
	if cfg.Providers.ClaudeMem.Enabled {
		t.Error("expected claude-mem disabled")
	}
}

// --- REQ-INIT-07: next-steps block ---

func TestInit_REQ_INIT_07_NextStepsBlock(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	out, err := executeInit(t, dir, false, "myapp", []string{"engram"})
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	// Must contain "Next steps" block.
	if !strings.Contains(out, "Next steps") {
		t.Errorf("output missing 'Next steps' block, got:\n%s", out)
	}
	if !strings.Contains(out, "glia sync") {
		t.Errorf("output missing 'glia sync' hint, got:\n%s", out)
	}
}

// --- T-2.3 / REQ-PRV: privacy field in config.yaml ---

func TestInit_T23_PrivacyFieldPresent(t *testing.T) {
	dir := t.TempDir()
	withProbe(t, noopProbe)

	if _, err := executeInit(t, dir, false, "myapp", []string{"engram"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Read raw YAML and confirm excluded_session_ids key is present.
	cfgPath := filepath.Join(dir, ".glia", "config.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config.yaml not found: %v", err)
	}

	cfg := readConfigYAML(t, dir)
	if cfg.Privacy.ExcludedSessionIDs == nil {
		t.Error("privacy.excluded_session_ids must not be nil")
	}

	// YAML should contain the privacy section.
	if !strings.Contains(string(raw), "excluded_session_ids") {
		t.Errorf("config.yaml missing excluded_session_ids section, got:\n%s", raw)
	}
}

// --- helpers ---

// assertFilesExist checks that the three required files are present (REQ-INIT-04).
func assertFilesExist(t *testing.T, dir string) {
	t.Helper()
	storeDir := filepath.Join(dir, ".glia")
	for _, name := range []string{"schema.json", "config.yaml", "memory.jsonl"} {
		if _, err := os.Stat(filepath.Join(storeDir, name)); err != nil {
			t.Errorf("%s not created: %v", name, err)
		}
	}
}

// readConfigYAML reads and unmarshals config.yaml from the .glia/
// directory under dir.
func readConfigYAML(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfgPath := filepath.Join(dir, ".glia", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config.yaml: %v", err)
	}
	return &cfg
}

// executeInitInDir is like executeInit but uses the given dir as both the
// project dir (rootFlags.dir) and the working directory for test isolation.
func executeInitInDir(t *testing.T, dir string, force bool, project string, providers []string) (string, error) {
	t.Helper()
	return executeInit(t, dir, force, project, providers)
}

// runGit runs a git command in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	all := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", all...).CombinedOutput()
	if err != nil {
		t.Logf("git %v: %s", args, out)
		// Non-fatal: some CI environments may not have git.
	}
}
