// Package tui implements the Bubble Tea terminal dashboard for wrapper-mems.
package tui

import "github.com/charmbracelet/lipgloss"

// Color palette — neutral theme (REQ-DEFER-05: single theme in v1).
var (
	colorPrimary  = lipgloss.Color("4")   // ANSI blue
	colorMuted    = lipgloss.Color("8")   // bright-black / dark grey
	colorActive   = lipgloss.Color("15")  // white
	colorBg       = lipgloss.Color("0")   // black
	colorError    = lipgloss.Color("1")   // red
	colorWarning  = lipgloss.Color("3")   // yellow
	colorSuccess  = lipgloss.Color("2")   // green
	colorConflict = lipgloss.Color("1")   // same as error
)

// Tab bar styles.
var (
	// tabInactive styles an unselected tab label.
	tabInactive = lipgloss.NewStyle().
		Foreground(colorMuted).
		Padding(0, 1)

	// tabActive styles the currently selected tab label.
	tabActive = lipgloss.NewStyle().
		Foreground(colorActive).
		Background(colorPrimary).
		Padding(0, 1).
		Bold(true)

	// tabBadgeConflict styles the conflict count badge when > 0.
	tabBadgeConflict = lipgloss.NewStyle().
		Foreground(colorConflict).
		Bold(true)

	// tabBadgeMuted styles the conflict count badge when = 0.
	tabBadgeMuted = lipgloss.NewStyle().
		Foreground(colorMuted)
)

// Header / status bar styles.
var (
	// headerBar styles the full-width header row.
	headerBar = lipgloss.NewStyle().
		Background(colorPrimary).
		Foreground(colorActive).
		Padding(0, 1).
		Bold(true)

	// statusBar styles the full-width bottom status row.
	statusBar = lipgloss.NewStyle().
		Background(colorBg).
		Foreground(colorMuted).
		Padding(0, 1)

	// statusBarHealthy styles a healthy provider glyph in the status bar.
	statusBarHealthy = lipgloss.NewStyle().
		Foreground(colorSuccess)

	// statusBarDegraded styles an unhealthy provider glyph in the status bar.
	statusBarDegraded = lipgloss.NewStyle().
		Foreground(colorError)
)

// Overlay / error styles.
var (
	// errorOverlay styles the full-screen error/sync output overlay border.
	errorOverlay = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorError).
		Padding(1, 2)

	// syncOverlay styles the sync output overlay border.
	syncOverlay = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(1, 2)
)

// Content area styles.
var (
	// contentArea styles the main tab content region.
	contentArea = lipgloss.NewStyle().
		Padding(0, 1)

	// detailPane styles the detail/preview pane in split views.
	detailPane = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colorMuted).
		Padding(0, 1)

	// errorText styles inline error text.
	errorText = lipgloss.NewStyle().
		Foreground(colorError)

	// mutedText styles secondary/muted text.
	mutedText = lipgloss.NewStyle().
		Foreground(colorMuted)

	// boldText styles emphasized text.
	boldText = lipgloss.NewStyle().
		Bold(true)

	// warningGlyph styles a warning/degraded glyph.
	warningGlyph = lipgloss.NewStyle().
		Foreground(colorWarning)
)
