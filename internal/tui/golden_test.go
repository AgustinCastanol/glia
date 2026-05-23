package tui

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// updateGolden is set to true via -update-golden to regenerate golden files.
// Named distinctly to avoid clashing with charmbracelet/x/exp/golden which also
// registers a global -update flag in the same test binary.
var updateGolden = flag.Bool("update-golden", false, "update golden files in testdata/")

func goldenPath(name string) string {
	return filepath.Join("testdata", "golden", name)
}

// readGolden reads a golden file, returning empty string if it doesn't exist yet.
func readGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(goldenPath(name))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("readGolden: %v", err)
	}
	return string(data)
}

// writeGolden writes content to a golden file.
func writeGolden(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(goldenPath(name), []byte(content), 0o644); err != nil {
		t.Fatalf("writeGolden: %v", err)
	}
}

// buildModelAt80x24 constructs a sized root Model with fixture data injected.
func buildModelAt80x24() Model {
	m := New(t80x24Dir())
	// Fake window size message to size the model.
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m3, ok := m2.(Model); ok {
		return m3
	}
	return m
}

// t80x24Dir returns a temp-like path; golden tests don't need real disk reads.
func t80x24Dir() string { return "/dev/null/tui-golden" }

// TestGoldenLayout_80x24 captures the full View() at 80×24 terminal.
// Run with -update to regenerate the golden file.
func TestGoldenLayout_80x24(t *testing.T) {
	m := buildModelAt80x24()
	got := m.View()

	if *updateGolden {
		writeGolden(t, "layout_80x24.txt", got)
		t.Log("golden file updated")
		return
	}

	want := readGolden(t, "layout_80x24.txt")
	if want == "" {
		// Golden file doesn't exist yet — write it on first run.
		writeGolden(t, "layout_80x24.txt", got)
		t.Log("golden file created on first run")
		return
	}
	if got != want {
		t.Errorf("layout_80x24 mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// TestGoldenLayout_HelpTab captures the Help tab view at 80×24.
func TestGoldenLayout_HelpTab(t *testing.T) {
	m := buildModelAt80x24()
	// Switch to help tab.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	root, ok := m2.(Model)
	if !ok {
		t.Fatalf("want Model, got %T", m2)
	}
	got := root.View()

	if *updateGolden {
		writeGolden(t, "layout_help_80x24.txt", got)
		t.Log("golden file updated")
		return
	}

	want := readGolden(t, "layout_help_80x24.txt")
	if want == "" {
		writeGolden(t, "layout_help_80x24.txt", got)
		t.Log("golden file created on first run")
		return
	}
	if got != want {
		t.Errorf("layout_help_80x24 mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}
