package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tab identifies the active tab in the TUI. (REQ-TUI-05)
type tab int

const (
	tabObs       tab = iota // Observations tab
	tabConflicts            // Conflicts tab
	tabStatus               // Status tab
	tabHelp                 // Help tab
)

// overlayModel holds the sync output overlay shown while a subprocess runs.
// Nil when no sync is active. (REQ-TUI-10)
type overlayModel struct {
	lines   []string
	spinner spinner.Model
	running bool // false once subprocess has exited
	err     error
}

// overlayView renders the sync overlay into a box sized w×h.
func (o *overlayModel) view(w, h int) string {
	var sb strings.Builder
	if o.running {
		sb.WriteString(o.spinner.View())
		sb.WriteString("  Running sync…\n")
	} else {
		sb.WriteString("Sync complete. Press enter to dismiss.\n")
		if o.err != nil {
			sb.WriteString(errorText.Render("error: " + o.err.Error()) + "\n")
		}
	}
	sb.WriteString("\n")
	for _, l := range o.lines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	inner := sb.String()

	// Clamp to overlay box.
	available := w - 4 // border+padding
	if available < 10 {
		available = 10
	}
	return syncOverlay.Width(available).Render(inner)
}

// subModel is the common interface all tab sub-models must satisfy.
// Stub implementations are in this file; full implementations are in PR-3.
type subModel interface {
	Init() tea.Cmd
	Update(tea.Msg) (subModel, tea.Cmd)
	View() string
	SetSize(w, h int)
}

// Sub-model stubs removed — real implementations live in:
//   observations.go  (obsModel)
//   conflicts.go     (conflictModel)
//   status.go        (statusModel)
//   help.go          (helpModel)

// Model is the root Bubble Tea model. It owns tab routing, window sizing,
// global key dispatch, and the sync overlay. (REQ-TUI-05, REQ-TUI-11)
type Model struct {
	dir       string
	activeTab tab
	w, h      int

	obs      subModel
	conflict subModel
	status   subModel
	help     subModel

	overlay *overlayModel
	err     error

	conflictCount int // number of unresolved conflicts for the badge
}

// newSpinner builds a preconfigured spinner for use in overlay models.
func newSpinner() spinner.Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return sp
}

// New creates a root Model for the project at dir. Sub-models are stubbed;
// they load their data on their first Init() or Update() call. (REQ-TUI-02)
func New(dir string) Model {
	return Model{
		dir:      dir,
		activeTab: tabObs,
		obs:      newObsModel(dir),
		conflict: newConflictModel(dir),
		status:   newStatusModel(dir),
		help:     newHelpModel(),
	}
}

// Init satisfies tea.Model. It starts init Cmds for all sub-models.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.obs.Init(),
		m.conflict.Init(),
		m.status.Init(),
		m.help.Init(),
	}
	return tea.Batch(cmds...)
}

// Update satisfies tea.Model. Routing order:
//  1. tea.WindowSizeMsg → store w/h, propagate to all sub-models.
//  2. overlay modal (if active) gets all messages.
//  3. syncDoneMsg → update overlay state.
//  4. Global keys: q/ctrl+c quit; O/C/S/? switch tabs.
//  5. Delegate to active sub-model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.propagateSize()
		return m, nil

	case tea.KeyMsg:
		// 2. Overlay intercepts all keys when active.
		if m.overlay != nil {
			if !m.overlay.running {
				// Only enter dismisses the overlay.
				if msg.String() == "enter" {
					m.overlay = nil
				}
			} else {
				// q/ctrl+c still quits even inside overlay.
				switch msg.String() {
				case "q", "ctrl+c":
					return m, tea.Quit
				}
			}
			return m, nil
		}

		// 4. Global key dispatch.
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "O":
			m.activeTab = tabObs
			return m, nil
		case "C":
			m.activeTab = tabConflicts
			return m, nil
		case "S":
			m.activeTab = tabStatus
			return m, nil
		case "?":
			m.activeTab = tabHelp
			return m, nil
		}

		// 5. Delegate to active sub-model.
		return m.delegateKey(msg)

	case syncDoneMsg:
		if m.overlay != nil {
			m.overlay.running = false
			m.overlay.err = msg.err
			if msg.output != "" {
				m.overlay.lines = strings.Split(msg.output, "\n")
			}
		}
		return m, nil

	case spinner.TickMsg:
		if m.overlay != nil && m.overlay.running {
			sp := m.overlay.spinner
			var cmd tea.Cmd
			sp, cmd = sp.Update(msg)
			m.overlay.spinner = sp
			return m, cmd
		}
		return m, nil
	}

	// Pass all other messages to the active sub-model.
	return m.delegateMsg(msg)
}

// propagateSize forwards the current window dimensions to all sub-models.
// Content height accounts for header (1 line), tab bar (1 line), status bar (1 line).
func (m *Model) propagateSize() {
	contentH := m.h - 3
	if contentH < 0 {
		contentH = 0
	}
	m.obs.SetSize(m.w, contentH)
	m.conflict.SetSize(m.w, contentH)
	m.status.SetSize(m.w, contentH)
	m.help.SetSize(m.w, contentH)
}

// delegateKey sends a key message to the active sub-model and re-assigns it.
func (m Model) delegateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	return m.delegateMsg(msg)
}

// delegateMsg sends any message to the active sub-model and re-assigns it.
func (m Model) delegateMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.activeTab {
	case tabObs:
		m.obs, cmd = m.obs.Update(msg)
	case tabConflicts:
		m.conflict, cmd = m.conflict.Update(msg)
	case tabStatus:
		m.status, cmd = m.status.Update(msg)
	case tabHelp:
		m.help, cmd = m.help.Update(msg)
	}
	return m, cmd
}

// View satisfies tea.Model. It renders header + tab bar + content + status bar.
func (m Model) View() string {
	if m.w == 0 {
		return "" // not sized yet
	}

	header := m.renderHeader()
	tabBar := m.renderTabBar()
	content := m.renderContent()
	statusB := m.renderStatusBar()

	view := strings.Join([]string{header, tabBar, content, statusB}, "\n")

	// Overlay draws on top (full-screen modal).
	if m.overlay != nil {
		return view + "\n\n" + m.overlay.view(m.w, m.h)
	}
	return view
}

// renderHeader returns the full-width header bar.
func (m Model) renderHeader() string {
	title := "wrapper-mems"
	hint := "q quit"
	gap := m.w - lipgloss.Width(title) - lipgloss.Width(hint) - 2
	if gap < 1 {
		gap = 1
	}
	return headerBar.Width(m.w).Render(
		title + strings.Repeat(" ", gap) + hint,
	)
}

// renderTabBar returns the tab bar with conflict badge.
func (m Model) renderTabBar() string {
	tabs := []struct {
		id    tab
		label string
	}{
		{tabObs, "[O]bservations"},
		{tabConflicts, m.conflictLabel()},
		{tabStatus, "[S]tatus"},
		{tabHelp, "[?]Help"},
	}

	parts := make([]string, len(tabs))
	for i, t := range tabs {
		if t.id == m.activeTab {
			parts[i] = tabActive.Render(t.label)
		} else {
			parts[i] = tabInactive.Render(t.label)
		}
	}
	return strings.Join(parts, " ")
}

// conflictLabel builds the Conflicts tab label with optional count badge.
func (m Model) conflictLabel() string {
	if m.conflictCount > 0 {
		badge := tabBadgeConflict.Render(fmt.Sprintf("(%d)", m.conflictCount))
		return "[C]onflicts " + badge
	}
	return tabBadgeMuted.Render("[C]onflicts (0)")
}

// renderContent returns the active tab's content, bounded by available height.
func (m Model) renderContent() string {
	contentH := m.h - 3
	if contentH < 0 {
		contentH = 0
	}

	var content string
	switch m.activeTab {
	case tabObs:
		content = m.obs.View()
	case tabConflicts:
		content = m.conflict.View()
	case tabStatus:
		content = m.status.View()
	case tabHelp:
		content = m.help.View()
	}

	// Pad content to fill the available height so status bar stays at bottom.
	lines := strings.Split(content, "\n")
	for len(lines) < contentH {
		lines = append(lines, "")
	}
	if len(lines) > contentH {
		lines = lines[:contentH]
	}
	return strings.Join(lines, "\n")
}

// renderStatusBar returns the bottom status row.
func (m Model) renderStatusBar() string {
	hint := "O:obs  C:conflicts  S:status  ?:help  q:quit"
	if m.err != nil {
		hint = errorText.Render(m.err.Error())
	}
	return statusBar.Width(m.w).Render(hint)
}
