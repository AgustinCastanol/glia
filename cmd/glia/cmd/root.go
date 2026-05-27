// Package cmd implements the cobra command tree for the glia CLI.
package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/agustincastanol/glia/internal/store"
)

// rootFlags holds the persistent flags shared across all subcommands.
var rootFlags struct {
	// dir is the project root directory; defaults to the current working directory.
	dir string

	// project is an optional project name override (forwarded to engine/adapters).
	project string

	// verbose enables detailed progress output.
	verbose bool
}

// rootCmd is the cobra root command for glia.
var rootCmd = &cobra.Command{
	Use:   "glia",
	Short: "Bidirectional sync between canonical memory store and providers",
	Long: `glia synchronises observations between a local append-only canonical
store (.glia/) and registered memory providers such as engram and claude-mem.

Run 'glia init' first to initialise the store in the current directory.

No telemetry is collected. Ever. glia never transmits usage data, crash
reports, or analytics to any external endpoint. (REQ-TEL-01)`,
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&rootFlags.dir, "dir", "",
		"project root directory (default: current working directory)")
	rootCmd.PersistentFlags().StringVar(&rootFlags.project, "project", "",
		"project name override (forwarded to providers)")
	rootCmd.PersistentFlags().BoolVar(&rootFlags.verbose, "verbose", false,
		"enable detailed progress output")
}

// Execute runs the root command and returns any error. main() maps the
// returned error to an exit code via ExitCode.
func Execute() error {
	return rootCmd.Execute()
}

// ExitCode maps a run error returned by Execute to a CLI exit code
// per D6 / REQ-SE-51..53. It is the exported entry point for main().
func ExitCode(err error) int {
	return exitCode(err)
}

// projectDir resolves the effective project root: --dir flag, then cwd.
func projectDir() (string, error) {
	if rootFlags.dir != "" {
		return rootFlags.dir, nil
	}
	return os.Getwd()
}

// storePath returns the .glia/ directory path under the project root.
func storePath(dir string) string {
	return dir + "/.glia"
}

// exitCode maps a run error to a CLI exit code per D6 / REQ-SE-51..53.
//
//	0  — success (nil, or warning-only path)
//	1  — hard error (Health fail for all providers, ErrLocked, ErrCorrupt, ErrSchemaMismatch, ErrNoStore)
//	2  — unresolved conflicts after run
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, errConflicts) {
		return 2
	}
	return 1
}

// exitWith calls os.Exit with the appropriate code for err.
// Use this (not os.Exit directly) so the mapping stays central.
func exitWith(err error) {
	os.Exit(exitCode(err))
}

// errConflicts is a sentinel returned by commands that detect unresolved
// conflicts so exitCode() can map it to exit 2.
var errConflicts = errors.New("unresolved conflicts")

// errNoStore is returned when the .glia/ directory does not exist
// and the command requires an initialised store (REQ-SE-05).
var errNoStore = errors.New("no canonical store found — run `glia init` first")

// requireStore opens the store at dir or writes the "no store" message and
// returns errNoStore. The caller must defer s.Close() on success.
func requireStore(dir string) (*store.Store, error) {
	sp := storePath(dir)
	if _, err := os.Stat(sp); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, errNoStore.Error())
		return nil, errNoStore
	}

	s, err := store.Open(sp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store: %v\n", err)
		return nil, err
	}
	return s, nil
}
