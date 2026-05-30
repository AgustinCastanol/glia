package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// helpModel implements the Help tab. It renders a static keybinding cheatsheet
// that fits within 80 columns. (REQ-TUI-09, T-18)
// Uses pointer receivers to satisfy the subModel interface contract.
type helpModel struct {
	w, h int
}

func newHelpModel() *helpModel {
	return &helpModel{}
}

func (m *helpModel) Init() tea.Cmd { return nil }

func (m *helpModel) SetSize(w, h int) {
	m.w, m.h = w, h
}

func (m *helpModel) Update(_ tea.Msg) (subModel, tea.Cmd) {
	return m, nil
}

// helpSection renders a titled keybinding block. Each row is [key, description].
// Lines are kept under 38 chars per column so two columns fit in 80 cols.
func helpSection(title string, rows [][2]string) string {
	var sb strings.Builder
	sb.WriteString(boldText.Render(title) + "\n")
	for _, r := range rows {
		sb.WriteString(fmt.Sprintf("  %-14s %s\n", r[0], r[1]))
	}
	return sb.String()
}

// View renders the static cheatsheet. Two-column layout; each column ≤38 chars
// so the total fits comfortably within 80 columns. (REQ-TUI-09)
func (m *helpModel) View() string {
	col := lipgloss.NewStyle().Width(38)

	globalSec := helpSection("Global", [][2]string{
		{"tab / ⇧tab", "Next / previous tab"},
		{"1 / 2 / 3 / 4", "Jump to a tab"},
		{"q / ctrl+c", "Quit"},
	})

	obsSec := helpSection("Observations", [][2]string{
		{"/", "Focus filter input"},
		{"↑ / k", "Move selection up"},
		{"↓ / j", "Move selection down"},
		{"enter", "Full-screen detail"},
		{"esc", "Clear filter / exit detail"},
		{"c", "Copy canonical_id (OSC52)"},
		{"f", "Cycle kind filter"},
		{"t", "Cycle type filter"},
		{"g / G", "Top / bottom"},
		{"r", "Reload from disk"},
	})

	conflictsSec := helpSection("Conflicts", [][2]string{
		{"w", "Accept winner (--keep 1)"},
		{"l", "Promote loser  (--keep 2)"},
		{"s", "Skip conflict"},
	})

	statusSec := helpSection("Status", [][2]string{
		{"s", "Run sync (full)"},
		{"p", "Run sync pull"},
		{"P", "Run sync push"},
		{"r", "Refresh status data"},
	})

	left := lipgloss.JoinVertical(lipgloss.Left,
		col.Render(globalSec),
		col.Render(conflictsSec),
	)
	right := lipgloss.JoinVertical(lipgloss.Left,
		col.Render(obsSec),
		col.Render(statusSec),
	)

	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}
