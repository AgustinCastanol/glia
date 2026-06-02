package tui

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

)

// obsItem wraps a LazyRecord for the bubbles/list widget.
type obsItem struct {
	lr LazyRecord
}

func (o obsItem) Title() string       { return o.lr.Title }
func (o obsItem) Description() string { return fmt.Sprintf("%s · %s · r%d", o.lr.Kind, o.lr.Type, o.lr.Revision) }
func (o obsItem) FilterValue() string { return o.lr.Title }

// obsReloadMsg is sent when observations have been re-loaded from disk.
type obsReloadMsg struct {
	records []LazyRecord
	err     error
}

// kindFilter sentinel values for cycling via [f].
var obsKindFilters = []string{"", "observation", "session_summary", "relation"}

// typeFilter sentinel values for cycling via [t].
var obsTypeFilters = []string{"", "bugfix", "decision", "architecture", "discovery", "pattern", "config", "learning"}

// obsModel implements the Observations tab. It shows a filterable list on the
// left and a detail pane on the right. Full-screen detail is toggled by enter.
// All receivers use pointer form so SetSize mutations are preserved. (REQ-TUI-06, T-15, T-24)
type obsModel struct {
	dir  string
	w, h int

	// all holds every live LazyRecord loaded from disk.
	all []LazyRecord
	// filtered is the current view after applying query + kind/type filters.
	filtered []LazyRecord

	list          list.Model
	filter        textinput.Model
	filterFocused bool

	detail     viewport.Model
	fullScreen bool // enter toggles full-screen glamour detail

	// renderer is rebuilt on resize to match pane width.
	renderer *glamour.TermRenderer

	filterKindIdx int
	filterTypeIdx int

	statusMsg string
}

func newObsModel(dir string) *obsModel {
	delegate := list.NewDefaultDelegate()
	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false) // filter is managed by obsModel

	fi := textinput.New()
	fi.Placeholder = "filter… (type: kind: provider:)"
	fi.CharLimit = 120

	vp := viewport.New(0, 0)

	return &obsModel{
		dir:    dir,
		list:   l,
		filter: fi,
		detail: vp,
	}
}

// Init kicks off the initial load from disk.
func (m *obsModel) Init() tea.Cmd {
	return m.reloadCmd()
}

func (m *obsModel) reloadCmd() tea.Cmd {
	dir := storeSubdir(m.dir)
	return func() tea.Msg {
		recs, err := loadRecords(dir)
		return obsReloadMsg{records: recs, err: err}
	}
}

// SetSize implements subModel. Rebuilds glamour renderer for the new detail pane width.
func (m *obsModel) SetSize(w, h int) {
	m.w, m.h = w, h
	listW := w / 2
	detailW := w - listW - 1 // -1 for border

	m.list.SetSize(listW, h-2) // reserve 2 lines: filter row + status row
	m.detail.Width = detailW
	m.detail.Height = h - 2

	if detailW > 4 {
		r, err := glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(detailW-4),
		)
		if err == nil {
			m.renderer = r
		}
	}
	m.renderDetail()
}

// Update handles all messages for the observations tab.
func (m *obsModel) Update(msg tea.Msg) (subModel, tea.Cmd) {
	switch msg := msg.(type) {
	case obsReloadMsg:
		if msg.err != nil {
			m.statusMsg = "load error: " + msg.err.Error()
		} else {
			m.all = msg.records
			m.applyFilter()
			m.statusMsg = fmt.Sprintf("%d records", len(m.all))
		}
		return m, nil

	case tea.KeyMsg:
		if m.filterFocused {
			return m.handleFilterKey(msg)
		}
		if m.fullScreen {
			return m.handleFullScreenKey(msg)
		}
		return m.handleListKey(msg)
	}

	// Non-key messages: propagate to active inner widget.
	if m.filterFocused {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.applyFilter()
		return m, cmd
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m.renderDetail()
	return m, cmd
}

func (m *obsModel) handleFilterKey(msg tea.KeyMsg) (subModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		m.filterFocused = false
		m.filter.Blur()
		m.applyFilter()
		return m, nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.applyFilter()
	return m, cmd
}

func (m *obsModel) handleFullScreenKey(msg tea.KeyMsg) (subModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		m.fullScreen = false
		return m, nil
	}
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	return m, cmd
}

func (m *obsModel) handleListKey(msg tea.KeyMsg) (subModel, tea.Cmd) {
	switch msg.String() {
	case "/":
		m.filterFocused = true
		m.filter.Focus()
		return m, textinput.Blink
	case "esc":
		m.filter.SetValue("")
		m.filterFocused = false
		m.filter.Blur()
		m.applyFilter()
		return m, nil
	case "enter":
		if len(m.filtered) > 0 {
			m.fullScreen = true
			m.detail.GotoTop()
		}
		return m, nil
	case "c":
		// Copy canonical_id to clipboard via OSC 52 escape sequence. (T-24)
		// OSC 52 is supported by most modern terminals (iTerm2, kitty, WezTerm,
		// tmux with set-clipboard on). Terminals that don't support it silently
		// ignore the sequence — no external library required.
		if lr := m.selectedRecord(); lr != nil {
			writeOSC52(lr.CanonicalID)
			m.statusMsg = "copied: " + lr.CanonicalID
		}
		return m, nil
	case "f":
		m.filterKindIdx = (m.filterKindIdx + 1) % len(obsKindFilters)
		m.applyFilter()
		return m, nil
	case "t":
		m.filterTypeIdx = (m.filterTypeIdx + 1) % len(obsTypeFilters)
		m.applyFilter()
		return m, nil
	case "g":
		m.list.Select(0)
		m.renderDetail()
		return m, nil
	case "G":
		if len(m.filtered) > 0 {
			m.list.Select(len(m.filtered) - 1)
			m.renderDetail()
		}
		return m, nil
	case "r":
		return m, m.reloadCmd()
	}

	// Arrow keys, j/k: delegate to list, then sync detail pane.
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m.renderDetail()
	return m, cmd
}

// writeOSC52 emits an OSC 52 clipboard escape sequence to stdout.
// Terminals that do not support OSC 52 ignore the sequence silently.
// This requires no external library — only base64 from the standard library.
func writeOSC52(text string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	// OSC 52: ESC ] 52 ; c ; <base64data> BEL
	fmt.Printf("\x1b]52;c;%s\x07", encoded)
}

// applyFilter rebuilds filtered from all using the current query + kind/type indices.
func (m *obsModel) applyFilter() {
	query := m.filter.Value()
	kindFilter := obsKindFilters[m.filterKindIdx]
	typeFilter := obsTypeFilters[m.filterTypeIdx]

	m.filtered = filterRecords(m.all, query, kindFilter, typeFilter)

	items := make([]list.Item, len(m.filtered))
	for i, lr := range m.filtered {
		items[i] = obsItem{lr: lr}
	}
	m.list.SetItems(items)
	m.renderDetail()
}

// filterRecords applies query + prefix filters to all and returns matching LazyRecords.
// Exported as a pure function so unit tests can call it directly.
func filterRecords(all []LazyRecord, query, kindFilter, typeFilter string) []LazyRecord {
	var out []LazyRecord
	for _, lr := range all {
		if kindFilter != "" && lr.Kind != kindFilter {
			continue
		}
		if typeFilter != "" && lr.Type != typeFilter {
			continue
		}
		if query != "" && !matchQuery(lr, query) {
			continue
		}
		out = append(out, lr)
	}
	if out == nil {
		return []LazyRecord{}
	}
	return out
}

// matchQuery tests whether a LazyRecord matches a filter query.
// Structured prefixes type:, kind:, provider: trigger exact-match.
// Anything else is a case-insensitive substring match on title.
// Content is NOT searched during filter — it's not decoded yet. (REQ-TUI-04)
// Full-body search is a v1.1 feature.
func matchQuery(lr LazyRecord, query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return true
	}
	if v, ok := strings.CutPrefix(q, "type:"); ok {
		return strings.EqualFold(lr.Type, strings.TrimSpace(v))
	}
	if v, ok := strings.CutPrefix(q, "kind:"); ok {
		return strings.EqualFold(lr.Kind, strings.TrimSpace(v))
	}
	if v, ok := strings.CutPrefix(q, "provider:"); ok {
		// Provider is not in LazyRecord index fields; decode to check. This is
		// an uncommon filter so the allocation is acceptable.
		rec, err := lr.Decode()
		if err != nil {
			return false
		}
		return strings.EqualFold(rec.Origin.Provider, strings.TrimSpace(v))
	}
	// Plain substring: case-insensitive on title only (content is lazy).
	lower := strings.ToLower(q)
	return strings.Contains(strings.ToLower(lr.Title), lower)
}

// selectedRecord returns a pointer into filtered for the current list cursor, or nil.
func (m *obsModel) selectedRecord() *LazyRecord {
	i := m.list.Index()
	if i < 0 || i >= len(m.filtered) {
		return nil
	}
	return &m.filtered[i]
}

// renderDetail updates the viewport content for the currently selected record.
// The full CanonicalRecord is decoded here (lazy — only on selection). (REQ-TUI-04)
func (m *obsModel) renderDetail() {
	lr := m.selectedRecord()
	if lr == nil {
		m.detail.SetContent(mutedText.Render("(no selection)"))
		return
	}

	// Decode the full record for the detail pane. This is the only place where
	// content is decoded — keeping loadRecords fast. (REQ-TUI-04)
	rec, err := lr.Decode()
	if err != nil {
		m.detail.SetContent(errorText.Render("decode error: " + err.Error()))
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("ID:"), rec.CanonicalID)
	fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("Kind:"), rec.Kind)
	fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("Type:"), rec.Type)
	if rec.TopicKey != "" {
		fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("Topic:"), rec.TopicKey)
	}
	if rec.Origin.Provider != "" {
		fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("Provider:"), rec.Origin.Provider)
	}
	fmt.Fprintf(&sb, "%s  r%d\n", boldText.Render("Revision:"), rec.Revision)
	fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("Created:"), rec.CreatedAt)
	fmt.Fprintf(&sb, "%s  %s\n", boldText.Render("Updated:"), rec.UpdatedAt)
	sb.WriteString("\n")

	if m.renderer != nil && (rec.ContentFormat == "markdown" || rec.ContentFormat == "") {
		rendered, err := m.renderer.Render(rec.Content)
		if err == nil {
			sb.WriteString(rendered)
		} else {
			sb.WriteString(rec.Content)
		}
	} else {
		sb.WriteString(rec.Content)
	}

	m.detail.SetContent(sb.String())
}

// View renders the observations tab as a two-pane layout or full-screen detail.
func (m *obsModel) View() string {
	if m.w == 0 {
		return ""
	}
	if m.fullScreen {
		return m.fullScreenView()
	}

	listW := m.w / 2
	detailW := m.w - listW - 1

	left := lipgloss.JoinVertical(lipgloss.Left,
		m.filter.View(),
		m.list.View(),
	)
	right := detailPane.Width(detailW).Height(m.h-2).Render(m.detail.View())

	top := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(listW).Render(left),
		right,
	)
	statusLine := mutedText.Render(m.statusMsg + m.hintSuffix())
	return lipgloss.JoinVertical(lipgloss.Left, top, statusLine)
}

func (m *obsModel) fullScreenView() string {
	lr := m.selectedRecord()
	if lr == nil {
		return mutedText.Render("(no selection)")
	}
	header := boldText.Render(lr.Title) + "  " + mutedText.Render("[esc] back")
	return lipgloss.JoinVertical(lipgloss.Left, header, m.detail.View())
}

func (m *obsModel) hintSuffix() string {
	var filters []string
	if k := obsKindFilters[m.filterKindIdx]; k != "" {
		filters = append(filters, "kind:"+k)
	}
	if t := obsTypeFilters[m.filterTypeIdx]; t != "" {
		filters = append(filters, "type:"+t)
	}
	hint := "  /filter f:kind t:type r:reload c:copy-id enter:detail"
	if len(filters) > 0 {
		hint = "  [" + strings.Join(filters, " ") + "]" + hint
	}
	return hint
}

