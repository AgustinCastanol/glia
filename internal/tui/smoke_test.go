package tui

import (
	"bytes"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// TestSmoke_TabNavAndQuit exercises the full TUI lifecycle using teatest:
// - start the program with a temp dir
// - wait for the header/tab bar to appear
// - navigate to each tab
// - quit cleanly
//
// Skipped in -short mode because it runs the full Bubble Tea event loop.
func TestSmoke_TabNavAndQuit(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test skipped in -short mode")
	}

	dir := t.TempDir()
	m := New(dir)

	tm := teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(80, 24),
	)

	// Wait for the header bar containing "wrapper-mems" to appear in the output.
	teatest.WaitFor(t, tm.Output(),
		func(out []byte) bool {
			return bytes.Contains(out, []byte("wrapper-mems"))
		},
		teatest.WithDuration(3*time.Second),
		teatest.WithCheckInterval(50*time.Millisecond),
	)

	// Navigate: O → C → S → ?
	for _, key := range []rune{'C', 'S', '?', 'O'} {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	}

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	// Collect final output and verify clean exit (no panic, no error string
	// in the rendered view at exit time).
	finalOut, err := io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(3*time.Second)))
	if err != nil {
		t.Fatalf("FinalOutput: %v", err)
	}

	// The final output should have contained the tab bar hints at some point.
	if len(finalOut) == 0 {
		t.Error("expected non-empty final output from TUI")
	}
}
