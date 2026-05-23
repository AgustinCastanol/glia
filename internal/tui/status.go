package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// statusDataMsg carries the result of calling callStatusJSON + store.Stats.
type statusDataMsg struct {
	status *StatusJSON
	stats  store.StoreStats
	err    error
}

// statusModel implements the Status tab. Displays provider health, watermarks,
// and store stats. Keys s/p/P trigger sync subprocesses. (REQ-TUI-08, T-17)
type statusModel struct {
	dir    string
	w, h   int
	runner commandRunner // injectable for tests

	status *StatusJSON
	stats  store.StoreStats
	errMsg string
}

func newStatusModel(dir string) *statusModel {
	return &statusModel{
		dir:    dir,
		runner: defaultRunner,
	}
}

// Init triggers the initial data load.
func (m *statusModel) Init() tea.Cmd {
	return m.loadCmd()
}

func (m *statusModel) loadCmd() tea.Cmd {
	dir := m.dir
	dl := &dataLayer{runner: m.runner}
	return func() tea.Msg {
		st, err := dl.callStatusJSON(dir)
		if err != nil {
			// Non-fatal: return what we have.
			return statusDataMsg{err: err}
		}
		stats, _ := store.Stats(dir) // best-effort; zero value on error
		return statusDataMsg{status: st, stats: stats}
	}
}

// SetSize implements subModel.
func (m *statusModel) SetSize(w, h int) {
	m.w, m.h = w, h
}

// Update handles messages for the status tab.
func (m *statusModel) Update(msg tea.Msg) (subModel, tea.Cmd) {
	switch msg := msg.(type) {
	case statusDataMsg:
		m.status = msg.status
		m.stats = msg.stats
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		} else {
			m.errMsg = ""
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *statusModel) handleKey(msg tea.KeyMsg) (subModel, tea.Cmd) {
	switch msg.String() {
	case "s":
		return m, syncCmd(m.dir, runCommandExec, "sync")
	case "p":
		return m, syncCmd(m.dir, runCommandExec, "sync", "pull")
	case "P":
		return m, syncCmd(m.dir, runCommandExec, "sync", "push")
	case "r":
		return m, m.loadCmd()
	}
	return m, nil
}

// View renders the status tab content.
func (m *statusModel) View() string {
	if m.w == 0 {
		return ""
	}

	var sb strings.Builder

	// Project info.
	projectName := filepath.Base(m.dir)
	sb.WriteString(boldText.Render("Project: ") + projectName + "\n")
	sb.WriteString(boldText.Render("Path:    ") + m.dir + "\n")
	canonPath := filepath.Join(m.dir, ".wrapper-mems")
	sb.WriteString(boldText.Render("Store:   ") + canonPath + "\n")
	sb.WriteString("\n")

	// Store stats.
	sb.WriteString(boldText.Render("Store stats") + "\n")
	sb.WriteString(fmt.Sprintf("  Lines:   %d\n", m.stats.LineCount))
	sb.WriteString(fmt.Sprintf("  Size:    %s\n", formatBytes(m.stats.FileSizeBytes)))
	sb.WriteString(fmt.Sprintf("  Schema:  v%d\n", m.stats.SchemaVersion))
	sb.WriteString("\n")

	if m.status == nil {
		if m.errMsg != "" {
			sb.WriteString(errorText.Render("status error: "+m.errMsg) + "\n")
		} else {
			sb.WriteString(mutedText.Render("loading…") + "\n")
		}
		sb.WriteString("\n")
		sb.WriteString(m.hintRow())
		return sb.String()
	}

	// Provider health glyphs.
	sb.WriteString(boldText.Render("Providers") + "\n")
	if len(m.status.ProviderHealth) == 0 {
		sb.WriteString(mutedText.Render("  (no providers configured)") + "\n")
	}
	for name, health := range m.status.ProviderHealth {
		glyph := statusBarHealthy.Render("✓")
		if health != "ok" && health != "" {
			glyph = statusBarDegraded.Render("✗")
		}
		syncState := m.status.SyncState[name]
		watermarks := ""
		if syncState.LastPulledAt != "" {
			watermarks += fmt.Sprintf("  pulled %s", syncState.LastPulledAt)
		}
		if syncState.LastPushedAt != "" {
			watermarks += fmt.Sprintf("  pushed %s", syncState.LastPushedAt)
		}
		sb.WriteString(fmt.Sprintf("  %s  %s%s\n", glyph, name, mutedText.Render(watermarks)))
	}
	sb.WriteString("\n")

	// Conflict count.
	conflictCount := len(m.status.Conflicts)
	if conflictCount > 0 {
		sb.WriteString(warningGlyph.Render(fmt.Sprintf("  ⚠  %d unresolved conflict(s)", conflictCount)) + "\n\n")
	}

	sb.WriteString(m.hintRow())
	return sb.String()
}

func (m *statusModel) hintRow() string {
	return mutedText.Render("s:sync  p:pull  P:push  r:refresh") + "\n"
}

// formatBytes converts byte count to a human-readable string.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// osArgs0 returns os.Args[0] for subprocess invocation.
// Extracted so tests can verify call sites without running the binary.
func osArgs0() string {
	if len(os.Args) > 0 {
		return os.Args[0]
	}
	return "wrapper-mems"
}

// glyphFor returns a styled health glyph for a provider health string.
// Exported for use in the root status bar.
func glyphFor(health string) string {
	if health == "ok" || health == "" {
		return statusBarHealthy.Render("✓")
	}
	return statusBarDegraded.Render("✗")
}

// renderProviderGlyphs returns a compact glyph row for the root status bar.
func renderProviderGlyphs(providerHealth map[string]string) string {
	if len(providerHealth) == 0 {
		return ""
	}
	var parts []string
	for name, h := range providerHealth {
		parts = append(parts, glyphFor(h)+" "+name)
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, parts...)
}
