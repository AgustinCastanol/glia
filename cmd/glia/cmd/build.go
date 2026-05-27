package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/agustincastanol/glia/internal/store"
	enginesync "github.com/agustincastanol/glia/internal/sync"
)

// openStore opens the store at storePath (the .glia/ subdirectory).
// It does NOT validate whether the parent dir is initialised — use requireStore
// for commands that need a pre-existing store.
func openStore(storeDir string) (*store.Store, error) {
	return store.Open(storeDir)
}

// buildEngine constructs a sync Engine from an open store, using adapters
// registered at the call site, the config loaded from configPath, and opts.
// w is the writer for engine progress output (typically os.Stderr).
func buildEngine(
	s *store.Store,
	adapters map[string]interface{}, // placeholder until adapter registry is wired (PR-D)
	configPath string,
	opts enginesync.Options,
	w io.Writer,
) (*enginesync.Engine, error) {
	cfg, err := enginesync.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	// adapter.Adapter map — cast from placeholder. PR-D fills this properly.
	_ = adapters
	return enginesync.New(s, nil, cfg, opts, w), nil
}

// resolveExitCode maps a *enginesync.RunReport to the correct exit code.
// 0 = success, 1 = hard errors, 2 = conflicts present.
func resolveExitCode(r *enginesync.RunReport) int {
	if r == nil {
		return 0
	}
	if r.Conflicts > 0 {
		return 2
	}
	if len(r.HardErrors) > 0 {
		return 1
	}
	return 0
}

// dieHard prints msg to stderr and calls os.Exit(1).
func dieHard(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

// dieHardErr prints err to stderr and calls os.Exit(1).
func dieHardErr(err error) {
	dieHard(err.Error())
}

// configPath returns the path to config.yaml under storeDir.
func configPath(storeDir string) string {
	return filepath.Join(storeDir, store.ConfigFilename)
}

// newTabWriter creates a tab-delimited writer suitable for table output.
func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// isStoreError returns true when err signals a store-level problem that should
// map to exit 1 or 2 (ErrLocked, ErrCorrupt, ErrSchemaMismatch, errNoStore).
func isStoreError(err error) bool {
	if errors.Is(err, errNoStore) {
		return true
	}
	var target interface{ Error() string }
	_ = target
	return false
}
