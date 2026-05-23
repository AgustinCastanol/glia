package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newSizedModel returns a Model sized to w×h for tests that need a rendered view.
func newSizedModel(t *testing.T, dir string, w, h int) Model {
	t.Helper()
	m := New(dir)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return updated.(Model)
}

func TestModel_TabSwitch_O(t *testing.T) {
	m := New(t.TempDir())
	m.activeTab = tabStatus // start somewhere else

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("O")})
	if cmd != nil {
		// O should not issue a Cmd (pure state mutation).
	}
	result := updated.(Model)
	if result.activeTab != tabObs {
		t.Errorf("expected tabObs after pressing O, got %d", result.activeTab)
	}
}

func TestModel_TabSwitch_C(t *testing.T) {
	m := New(t.TempDir())
	m.activeTab = tabObs

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	result := updated.(Model)
	if result.activeTab != tabConflicts {
		t.Errorf("expected tabConflicts after pressing C, got %d", result.activeTab)
	}
}

func TestModel_TabSwitch_S(t *testing.T) {
	m := New(t.TempDir())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	result := updated.(Model)
	if result.activeTab != tabStatus {
		t.Errorf("expected tabStatus after pressing S, got %d", result.activeTab)
	}
}

func TestModel_TabSwitch_QuestionMark(t *testing.T) {
	m := New(t.TempDir())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	result := updated.(Model)
	if result.activeTab != tabHelp {
		t.Errorf("expected tabHelp after pressing ?, got %d", result.activeTab)
	}
}

func TestModel_QuitOnQ(t *testing.T) {
	m := New(t.TempDir())

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("expected a Cmd from pressing q")
	}
	// Execute the Cmd and check it produces a QuitMsg.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestModel_QuitOnCtrlC(t *testing.T) {
	m := New(t.TempDir())

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected a Cmd from pressing ctrl+c")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestModel_WindowSizeStored(t *testing.T) {
	m := New(t.TempDir())

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	result := updated.(Model)
	if result.w != 120 || result.h != 40 {
		t.Errorf("expected w=120 h=40, got w=%d h=%d", result.w, result.h)
	}
}

func TestModel_OverlayInstalled(t *testing.T) {
	m := New(t.TempDir())
	// Manually install an overlay to simulate syncTrigger.
	sp := newSpinner()
	m.overlay = &overlayModel{
		spinner: sp,
		running: true,
	}

	// Key input should NOT switch tabs while overlay is active.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	result := updated.(Model)
	if result.activeTab != tabObs {
		t.Errorf("tab should not switch while overlay is active; got %d", result.activeTab)
	}
	if result.overlay == nil {
		t.Error("overlay should still be installed after non-enter key")
	}
}

func TestModel_OverlayDismissedOnEnter(t *testing.T) {
	m := New(t.TempDir())
	sp := newSpinner()
	m.overlay = &overlayModel{
		spinner: sp,
		running: false, // subprocess done
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(Model)
	if result.overlay != nil {
		t.Error("overlay should be nil after enter when subprocess finished")
	}
}

func TestModel_SyncDoneMsg_UpdatesOverlay(t *testing.T) {
	m := New(t.TempDir())
	sp := newSpinner()
	m.overlay = &overlayModel{
		spinner: sp,
		running: true,
	}

	updated, _ := m.Update(syncDoneMsg{output: "pushed 3 records", err: nil})
	result := updated.(Model)
	if result.overlay == nil {
		t.Fatal("overlay should still be present after syncDoneMsg")
	}
	if result.overlay.running {
		t.Error("overlay.running should be false after syncDoneMsg")
	}
	if result.overlay.err != nil {
		t.Errorf("expected nil error in overlay, got: %v", result.overlay.err)
	}
	found := false
	for _, l := range result.overlay.lines {
		if strings.Contains(l, "pushed 3 records") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("overlay.lines should contain 'pushed 3 records', got: %v", result.overlay.lines)
	}
}

func TestModel_View_ContainsTabLabels(t *testing.T) {
	m := newSizedModel(t, t.TempDir(), 80, 24)

	view := m.View()
	for _, want := range []string{"bservations", "onflicts", "tatus", "elp"} {
		if !strings.Contains(view, want) {
			t.Errorf("view should contain %q\nview:\n%s", want, view)
		}
	}
}

func TestModel_View_QuitHintInHeader(t *testing.T) {
	m := newSizedModel(t, t.TempDir(), 80, 24)

	view := m.View()
	if !strings.Contains(view, "quit") {
		t.Errorf("view should contain quit hint\nview:\n%s", view)
	}
}
