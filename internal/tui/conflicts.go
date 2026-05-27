package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/agustincastanol/glia/internal/store"
)

// conflictItem wraps a ConflictEntry for the bubbles/list widget.
type conflictItem struct {
	entry store.ConflictEntry
}

func (c conflictItem) Title() string {
	return c.entry.CanonicalID
}

func (c conflictItem) Description() string {
	return fmt.Sprintf("r%d · detected %s · %d duplicates",
		c.entry.Revision, c.entry.DetectedAt, len(c.entry.Duplicates))
}

func (c conflictItem) FilterValue() string { return c.entry.CanonicalID }

// conflictReloadMsg is sent when the conflicts list has been refreshed.
type conflictReloadMsg struct {
	entries []store.ConflictEntry
	err     error
}

// conflictResolvedMsg is emitted by a resolution subprocess.
type conflictResolvedMsg struct {
	canonicalID string
	err         error
	output      string
}

// conflictModel implements the Conflicts tab. Shows a list of unresolved
// conflicts; [w]/[l] resolve, [s] skips. Resolution spawns a subprocess via
// syncCmd (D8). Uses pointer receivers. (REQ-TUI-07, T-16)
type conflictModel struct {
	dir    string
	w, h   int
	runner execRunner

	entries  []store.ConflictEntry
	list     list.Model
	errorMsg string
}

func newConflictModel(dir string) *conflictModel {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)

	return &conflictModel{
		dir:    dir,
		list:   l,
		runner: runCommandExec, // production: real subprocess
	}
}

func (m *conflictModel) Init() tea.Cmd {
	return m.reloadCmd()
}

func (m *conflictModel) reloadCmd() tea.Cmd {
	dir := m.dir
	return func() tea.Msg {
		snap, err := loadIndexFile(dir)
		if err != nil {
			return conflictReloadMsg{err: err}
		}
		return conflictReloadMsg{entries: snap.Conflicts}
	}
}

// SetSize implements subModel.
func (m *conflictModel) SetSize(w, h int) {
	m.w, m.h = w, h
	// List takes the left half; detail pane takes the right half.
	listW := w / 2
	listH := h - 1 // reserve 1 for hint row
	if listH < 0 {
		listH = 0
	}
	m.list.SetSize(listW, listH)
}

// Update handles messages for the conflicts tab.
func (m *conflictModel) Update(msg tea.Msg) (subModel, tea.Cmd) {
	switch msg := msg.(type) {
	case conflictReloadMsg:
		if msg.err != nil {
			m.errorMsg = "load error: " + msg.err.Error()
		} else {
			m.entries = msg.entries
			m.setListItems()
			m.errorMsg = ""
		}
		return m, nil

	case conflictResolvedMsg:
		if msg.err != nil {
			m.errorMsg = fmt.Sprintf("resolve %s failed: %v", msg.canonicalID, msg.err)
			if msg.output != "" {
				m.errorMsg += "\n" + msg.output
			}
		} else {
			// Remove the resolved entry from the list.
			m.removeEntry(msg.canonicalID)
			m.errorMsg = ""
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *conflictModel) handleKey(msg tea.KeyMsg) (subModel, tea.Cmd) {
	switch msg.String() {
	case "w":
		// Accept winner: --keep 1
		if e := m.selectedEntry(); e != nil {
			return m, m.resolveCmd(e.CanonicalID, 1)
		}
		return m, nil
	case "l":
		// Promote loser: --keep 2
		if e := m.selectedEntry(); e != nil {
			return m, m.resolveCmd(e.CanonicalID, 2)
		}
		return m, nil
	case "s":
		// Skip: advance selection without removing.
		next := m.list.Index() + 1
		if next < len(m.entries) {
			m.list.Select(next)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// resolveCmd spawns `glia --dir <dir> sync resolve <id> --keep <n>`.
func (m *conflictModel) resolveCmd(canonicalID string, keep int) tea.Cmd {
	runner := m.runner
	dir := m.dir
	return func() tea.Msg {
		fullArgs := []string{"--dir", dir, "sync", "resolve", canonicalID, "--keep", fmt.Sprintf("%d", keep)}
		out, err := runner(osArgs0(), fullArgs)
		return conflictResolvedMsg{
			canonicalID: canonicalID,
			output:      strings.TrimRight(string(out), "\n"),
			err:         err,
		}
	}
}

func (m *conflictModel) selectedEntry() *store.ConflictEntry {
	i := m.list.Index()
	if i < 0 || i >= len(m.entries) {
		return nil
	}
	return &m.entries[i]
}

func (m *conflictModel) setListItems() {
	items := make([]list.Item, len(m.entries))
	for i, e := range m.entries {
		items[i] = conflictItem{entry: e}
	}
	m.list.SetItems(items)
}

func (m *conflictModel) removeEntry(canonicalID string) {
	updated := m.entries[:0]
	for _, e := range m.entries {
		if e.CanonicalID != canonicalID {
			updated = append(updated, e)
		}
	}
	m.entries = updated
	m.setListItems()
}

// View renders the conflicts tab.
func (m *conflictModel) View() string {
	if m.w == 0 {
		return ""
	}

	listW := m.w / 2
	detailW := m.w - listW - 1

	listView := lipgloss.NewStyle().Width(listW).Render(m.list.View())
	detailView := detailPane.Width(detailW).Height(m.h - 1).Render(m.detailView())

	top := lipgloss.JoinHorizontal(lipgloss.Top, listView, detailView)

	hint := mutedText.Render("w:accept-winner  l:promote-loser  s:skip")
	if m.errorMsg != "" {
		hint = errorText.Render(m.errorMsg)
	}
	if len(m.entries) == 0 {
		hint = mutedText.Render("No conflicts — all clear.")
	}

	return lipgloss.JoinVertical(lipgloss.Left, top, hint)
}

// detailView renders the winner/loser pane for the selected conflict.
func (m *conflictModel) detailView() string {
	e := m.selectedEntry()
	if e == nil {
		if len(m.entries) == 0 {
			return mutedText.Render("No unresolved conflicts.")
		}
		return mutedText.Render("(no selection)")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("ID:"), e.CanonicalID)
	fmt.Fprintf(&sb, "%s  r%d\n", boldText.Render("Rev:"), e.Revision)
	fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("Detected:"), e.DetectedAt)
	sb.WriteString("\n")

	for i, dup := range e.Duplicates {
		label := "loser"
		if i == 0 {
			label = "winner"
		}
		sb.WriteString(boldText.Render(fmt.Sprintf("[%s]", label)) + "\n")
		fmt.Fprintf(&sb, "  offset %d · %s · %s\n", dup.LineOffset, dup.LineULID, dup.UpdatedAt)
		if dup.Provider != "" {
			fmt.Fprintf(&sb, "  provider: %s\n", dup.Provider)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
