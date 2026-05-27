package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/agustincastanol/wrapper-mems/internal/config"
	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// gitignoreEntries are the lines that init guarantees are present in .gitignore.
// NOTE: memory.jsonl is intentionally NOT in this list — it must be committed
// (PRD-5 §5, D2). Only runtime-generated files that should not be committed are
// listed here.
var gitignoreEntries = []string{
	".wrapper-mems/index.json",
	".wrapper-mems/.lock",
}

var initFlags struct {
	force     bool
	providers []string
	project   string
}

// probeFn is the function used to probe a provider CLI. It is a package-level
// variable so tests can replace it with a mock without spawning real subprocesses.
var probeFn = defaultProbe

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialise the wrapper-mems store in the current directory",
	Long: `init creates the .wrapper-mems/ store directory, writes a config.yaml
populated with auto-detected values, and ensures .gitignore contains the
required entries (REQ-INIT-01..07).

Use --force to re-initialise an existing store.
Use --project and --providers together for non-interactive (CI) mode.`,
	Args: cobra.NoArgs,
	RunE: runInit,
}

func init() {
	initCmd.Flags().BoolVar(&initFlags.force, "force", false,
		"overwrite existing .wrapper-mems/ directory")
	initCmd.Flags().StringSliceVar(&initFlags.providers, "providers", nil,
		"comma-separated list of providers to enable (e.g. engram,claude-mem); skips probing")
	initCmd.Flags().StringVar(&initFlags.project, "project", "",
		"project name; skips interactive detection when combined with --providers")
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, _ []string) error {
	dir, err := projectDir()
	if err != nil {
		return fmt.Errorf("init: resolve dir: %w", err)
	}

	storeDir := storePath(dir)

	// REQ-INIT-01: refuse if .wrapper-mems/ exists unless --force.
	if _, err := os.Stat(storeDir); err == nil {
		if !initFlags.force {
			fmt.Fprintf(os.Stderr,
				"init: .wrapper-mems/ already exists in %s (use --force to overwrite)\n", dir)
			return fmt.Errorf("store already initialised")
		}
		// --force: remove and recreate.
		if err := os.RemoveAll(storeDir); err != nil {
			return fmt.Errorf("init: remove existing store: %w", err)
		}
	}

	if err := os.MkdirAll(storeDir, 0755); err != nil {
		return fmt.Errorf("init: mkdir: %w", err)
	}

	// REQ-INIT-02: detect project name.
	projectName, err := detectProjectName(cmd, dir)
	if err != nil {
		return fmt.Errorf("init: project name: %w", err)
	}

	// REQ-INIT-03: detect providers.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	engramEnabled, claudeMemEnabled := detectProviders(ctx)

	// Step: write schema.json.
	if err := writeSchemaFile(storeDir); err != nil {
		return fmt.Errorf("init: schema.json: %w", err)
	}

	// Step: write config.yaml (REQ-INIT-04, PRD-5 §4.2).
	if err := writeConfigFile(storeDir, projectName, engramEnabled, claudeMemEnabled); err != nil {
		return fmt.Errorf("init: config.yaml: %w", err)
	}

	// REQ-INIT-04: create truly empty memory.jsonl (0 bytes, no header).
	memPath := filepath.Join(storeDir, "memory.jsonl")
	if err := os.WriteFile(memPath, []byte{}, 0644); err != nil {
		return fmt.Errorf("init: memory.jsonl: %w", err)
	}

	// REQ-INIT-05: update .gitignore.
	giPath := filepath.Join(dir, ".gitignore")
	if err := ensureGitignore(giPath, gitignoreEntries); err != nil {
		fmt.Fprintf(os.Stderr, "init: gitignore: %v\n", err)
		return err
	}

	// REQ-INIT-07: print next-steps block.
	fmt.Fprintf(cmd.OutOrStdout(), "wrapper-mems initialised in %s\n\n", storeDir)
	fmt.Fprintln(cmd.OutOrStdout(), "Next steps:")
	fmt.Fprintln(cmd.OutOrStdout(), "  wrapper-mems sync        # run first sync")
	fmt.Fprintln(cmd.OutOrStdout(), "  wrapper-mems status      # check provider health")
	fmt.Fprintln(cmd.OutOrStdout(), "  git add .wrapper-mems/   # commit the store")
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Share .wrapper-mems/ with teammates by committing it.")
	fmt.Fprintln(cmd.OutOrStdout(), "Docs: https://github.com/agustincastanol/wrapper-mems")

	return nil
}

// detectProjectName resolves the project name using the precedence chain
// defined in REQ-INIT-02:
//  1. --project flag
//  2. WRAPPER_MEMS_PROJECT env
//  3. git remote basename
//  4. directory basename
//  5. interactive prompt (TTY only); error if not a TTY
func detectProjectName(cmd *cobra.Command, dir string) (string, error) {
	// 1. --project flag.
	if initFlags.project != "" {
		return initFlags.project, nil
	}

	// 2. WRAPPER_MEMS_PROJECT env.
	if v := os.Getenv("WRAPPER_MEMS_PROJECT"); v != "" {
		return v, nil
	}

	// 3. git remote basename.
	if name := gitRemoteBasename(dir); name != "" {
		return name, nil
	}

	// 4. directory basename.
	if base := filepath.Base(dir); base != "" && base != "." {
		// If providers flag is also set we're in non-interactive mode — use dir
		// basename immediately (CI mode guard REQ-INIT-06).
		if len(initFlags.providers) > 0 {
			return base, nil
		}
		// For interactive mode we could still prompt, but directory basename is a
		// perfectly valid silent default that covers the common case. Only fall
		// through to prompt when there is no reasonable default (e.g. root "/" or ".").
		if base != "/" {
			return base, nil
		}
	}

	// 5. Interactive prompt (REQ-INIT-06: error when non-interactive).
	if len(initFlags.providers) > 0 {
		// In CI mode (--providers supplied) we cannot prompt.
		return "", fmt.Errorf("--project is required when stdin is not a TTY")
	}

	fmt.Fprint(cmd.OutOrStdout(), "Project name: ")
	sc := bufio.NewScanner(os.Stdin)
	if sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("project name is required")
}

// gitRemoteBasename runs `git remote get-url origin` and strips the trailing
// .git suffix and path components to return just the repo name.
// Returns "" if git is unavailable or no origin remote is configured.
func gitRemoteBasename(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return ""
	}
	// Strip trailing .git, then take the last path component.
	raw = strings.TrimSuffix(raw, ".git")
	// Handle both slashes and colons (SSH URLs like git@github.com:org/repo).
	raw = strings.ReplaceAll(raw, ":", "/")
	parts := strings.Split(raw, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" {
			return p
		}
	}
	return ""
}

// ProviderProbe holds the result of probing available providers.
type ProviderProbe struct {
	EngramFound    bool
	ClaudeMemFound bool
}

// detectProviders determines which providers to enable.
// If --providers flag was passed, it is used directly (CI mode, REQ-INIT-06).
// Otherwise each provider CLI is probed via probeFn.
func detectProviders(ctx context.Context) (engramEnabled, claudeMemEnabled bool) {
	if len(initFlags.providers) > 0 {
		for _, p := range initFlags.providers {
			switch strings.TrimSpace(strings.ToLower(p)) {
			case "engram":
				engramEnabled = true
			case "claude-mem", "claudemem":
				claudeMemEnabled = true
			}
		}
		return
	}

	// Probe each provider CLI with a 2-second timeout.
	probe := func(args ...string) bool {
		pCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return probeFn(pCtx, args...) == nil
	}

	engramEnabled = probe("engram", "version")
	claudeMemEnabled = probe("claude-mem", "status")
	return
}

// defaultProbe executes the given command and returns any error.
// Stdout and stderr are discarded. Used as the default probeFn.
func defaultProbe(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("no command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// schemaFileJSON is the JSON shape written to schema.json by init.
type schemaFileJSON struct {
	SchemaVersion         int    `json:"schema_version"`
	CreatedAt             string `json:"created_at"`
	WrapperMemsMinVersion string `json:"wrapper_mems_min_version,omitempty"`
}

// writeSchemaFile writes a fresh schema.json to storeDir.
// WrapperMemsMinVersion is intentionally empty (permissive default, D6).
func writeSchemaFile(storeDir string) error {
	sf := schemaFileJSON{
		SchemaVersion: store.StoreSupportedVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(sf)
	if err != nil {
		return fmt.Errorf("marshal schema: %w", err)
	}
	return os.WriteFile(filepath.Join(storeDir, "schema.json"), data, 0644)
}

// writeConfigFile marshals a config.Default() with detected values applied and
// writes it to storeDir/config.yaml (REQ-INIT-04).
// T-2.3 (REQ-PRV): the privacy.excluded_session_ids field is written as an
// empty list so the section is present in the YAML even when empty.
func writeConfigFile(storeDir, projectName string, engramEnabled, claudeMemEnabled bool) error {
	cfg := config.Default()
	cfg.Project = projectName
	cfg.Providers.Engram.Enabled = engramEnabled
	cfg.Providers.ClaudeMem.Enabled = claudeMemEnabled
	// privacy.excluded_session_ids is already []string{} from Default(), which
	// yaml.v3 marshals as an empty sequence — satisfies T-2.3/REQ-PRV.

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(filepath.Join(storeDir, store.ConfigFilename), data, 0644)
}

// ensureGitignore appends any missing entries from required to the file at
// path. If the file does not exist it is created. The file is left unchanged
// (byte-for-byte) when all entries are already present. REQ-INIT-05.
func ensureGitignore(path string, required []string) error {
	existing, err := readLines(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	existingSet := make(map[string]struct{}, len(existing))
	for _, l := range existing {
		existingSet[strings.TrimSpace(l)] = struct{}{}
	}

	var missing []string
	for _, entry := range required {
		if _, found := existingSet[entry]; !found {
			missing = append(missing, entry)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()

	// Ensure we start on a new line if the file is non-empty and doesn't end
	// with a newline.
	if len(existing) > 0 {
		last := existing[len(existing)-1]
		if !strings.HasSuffix(last, "\n") {
			fmt.Fprintln(f)
		}
	}

	for _, entry := range missing {
		fmt.Fprintln(f, entry)
	}
	return nil
}

// readLines reads all lines from path (preserving trailing newlines) or
// returns os.ErrNotExist when the file does not exist.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}
