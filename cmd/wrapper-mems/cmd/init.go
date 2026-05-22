package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/agustincastanol/wrapper-mems/internal/store"
	enginesync "github.com/agustincastanol/wrapper-mems/internal/sync"
)

// gitignoreEntries are the lines that init guarantees are present in .gitignore.
// REQ-SE-02.
var gitignoreEntries = []string{
	".wrapper-mems/memory.jsonl",
	".wrapper-mems/index.json",
	".wrapper-mems/.lock",
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialise the wrapper-mems store in the current directory",
	Long: `init creates the .wrapper-mems/ store directory, writes a default config.yaml,
and ensures .gitignore contains the required entries. It is idempotent: running
it multiple times produces the same result (REQ-SE-01..04).`,
	Args: cobra.NoArgs,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, _ []string) error {
	dir, err := projectDir()
	if err != nil {
		return fmt.Errorf("init: resolve dir: %w", err)
	}

	sp := storePath(dir)

	// Step 1: create store (store.Open calls MkdirAll internally). REQ-SE-01.
	s, err := store.Open(sp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: open store: %v\n", err)
		return err
	}
	s.Close()

	// Step 2: write config.yaml with defaults if absent. REQ-SE-03.
	cfgPath := configPath(sp)
	if err := writeConfigIfAbsent(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "init: config: %v\n", err)
		return err
	}

	// Step 3: ensure .gitignore contains required entries. REQ-SE-02.
	giPath := filepath.Join(dir, ".gitignore")
	if err := ensureGitignore(giPath, gitignoreEntries); err != nil {
		fmt.Fprintf(os.Stderr, "init: gitignore: %v\n", err)
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), "wrapper-mems initialised in", sp)
	return nil
}

// writeConfigIfAbsent writes config.yaml with Default() values only when the
// file does not already exist. REQ-SE-03.
func writeConfigIfAbsent(path string) error {
	if _, err := os.Stat(path); err == nil {
		// Already exists — leave it untouched (idempotent).
		return nil
	}

	cfg := enginesync.Default()

	// Serialise manually so we don't import yaml in the cmd layer.
	// The schema is small and stable; text templating is sufficient.
	content := fmt.Sprintf(
		"mirror_engram: %v\nmirror_timeout_seconds: %d\nproviders: [%s]\n",
		cfg.MirrorEngram,
		cfg.MirrorTimeoutSeconds,
		quotedList(cfg.Providers),
	)

	return os.WriteFile(path, []byte(content), 0644)
}

// quotedList formats a []string as YAML inline sequence items (unquoted).
func quotedList(ss []string) string {
	return strings.Join(ss, ", ")
}

// ensureGitignore appends any missing entries from required to the file at
// path. If the file does not exist it is created. The file is left unchanged
// (byte-for-byte) when all entries are already present. REQ-SE-02.
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
		// Nothing to add — file is unchanged. REQ-SE-02 idempotence.
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
