package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/agustincastanol/glia/internal/tui"
)

// tuiCmd is the primary TUI subcommand. (REQ-TUI-01)
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Open the terminal dashboard",
	Long: `tui opens a full-screen Bubble Tea terminal dashboard for browsing
observations, inspecting provider health, and resolving conflicts.

The dashboard is read-only: mutations (sync, conflict resolve) are delegated
to glia subprocesses. (D1, D8)`,
	Args: cobra.NoArgs,
	RunE: runTUI,
}

// uiCmd is an alias for tuiCmd so users can type `glia ui`. (REQ-TUI-01)
var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Alias for tui — open the terminal dashboard",
	Args:  cobra.NoArgs,
	RunE:  runTUI,
}

func init() {
	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(uiCmd)
}

// runTUI resolves the project directory and launches the Bubble Tea program.
func runTUI(_ *cobra.Command, _ []string) error {
	dir, err := projectDir()
	if err != nil {
		return fmt.Errorf("tui: resolve dir: %w", err)
	}

	// Verify a store exists at dir before launching the TUI so the user gets a
	// clear error instead of an empty dashboard.
	sp := storePath(dir)
	if _, err := os.Stat(sp); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, errNoStore.Error())
		return errNoStore
	}

	p := tea.NewProgram(tui.New(dir), tea.WithAltScreen())
	_, err = p.Run()
	return err
}
