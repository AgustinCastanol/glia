package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

func sampleConflicts() []store.ConflictEntry {
	return []store.ConflictEntry{
		{
			CanonicalID: "c1",
			Revision:    2,
			DetectedAt:  "2026-05-22T10:00:00Z",
			Duplicates: []store.ConflictDuplicate{
				{LineOffset: 10, LineULID: "ulid-a", UpdatedAt: "2026-05-22T09:59:00Z", Provider: "engram"},
				{LineOffset: 20, LineULID: "ulid-b", UpdatedAt: "2026-05-22T09:58:00Z", Provider: "claude-mem"},
			},
		},
		{
			CanonicalID: "c2",
			Revision:    1,
			DetectedAt:  "2026-05-22T11:00:00Z",
			Duplicates: []store.ConflictDuplicate{
				{LineOffset: 30, LineULID: "ulid-c", UpdatedAt: "2026-05-22T10:59:00Z", Provider: "engram"},
			},
		},
	}
}

func TestConflictModel_ListDisplay(t *testing.T) {
	dir := t.TempDir()
	m := newConflictModel(dir)
	m.SetSize(80, 24)

	// Simulate reload message.
	next, _ := m.Update(conflictReloadMsg{entries: sampleConflicts()})
	cm, ok := next.(*conflictModel)
	if !ok {
		t.Fatalf("want *conflictModel, got %T", next)
	}
	if len(cm.entries) != 2 {
		t.Errorf("want 2 entries, got %d", len(cm.entries))
	}

	view := cm.View()
	if !strings.Contains(view, "c1") {
		t.Error("View() should contain first canonical_id 'c1'")
	}
}

func TestConflictModel_W_AcceptsWinner(t *testing.T) {
	dir := t.TempDir()
	m := newConflictModel(dir)
	m.SetSize(80, 24)
	m.entries = sampleConflicts()
	m.setListItems()

	// Inject a fake runner that succeeds.
	resolved := ""
	m.runner = func(name string, args []string) ([]byte, error) {
		// Verify --keep 1 is in args.
		for i, a := range args {
			if a == "--keep" && i+1 < len(args) && args[i+1] == "1" {
				resolved = "keep1"
			}
		}
		return []byte("ok\n"), nil
	}

	// Press w.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	cm, ok := next.(*conflictModel)
	if !ok {
		t.Fatalf("want *conflictModel, got %T", next)
	}
	if cmd == nil {
		t.Fatal("want a cmd from 'w', got nil")
	}

	// Execute the cmd to get conflictResolvedMsg.
	msg := cmd()
	next2, _ := cm.Update(msg)
	cm2, ok := next2.(*conflictModel)
	if !ok {
		t.Fatalf("want *conflictModel, got %T", next2)
	}

	if resolved != "keep1" {
		t.Error("runner was not called with --keep 1")
	}
	// Entry c1 should be removed.
	if len(cm2.entries) != 1 {
		t.Errorf("want 1 entry after resolution, got %d", len(cm2.entries))
	}
	if cm2.entries[0].CanonicalID != "c2" {
		t.Errorf("want remaining entry c2, got %s", cm2.entries[0].CanonicalID)
	}
}

func TestConflictModel_L_PromotesLoser(t *testing.T) {
	dir := t.TempDir()
	m := newConflictModel(dir)
	m.SetSize(80, 24)
	m.entries = sampleConflicts()
	m.setListItems()

	resolved := ""
	m.runner = func(name string, args []string) ([]byte, error) {
		for i, a := range args {
			if a == "--keep" && i+1 < len(args) && args[i+1] == "2" {
				resolved = "keep2"
			}
		}
		return []byte("ok\n"), nil
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	cm, ok := next.(*conflictModel)
	if !ok {
		t.Fatalf("want *conflictModel, got %T", next)
	}
	if cmd == nil {
		t.Fatal("want a cmd from 'l', got nil")
	}

	msg := cmd()
	next2, _ := cm.Update(msg)
	cm2, ok := next2.(*conflictModel)
	if !ok {
		t.Fatalf("want *conflictModel, got %T", next2)
	}

	if resolved != "keep2" {
		t.Error("runner was not called with --keep 2")
	}
	if len(cm2.entries) != 1 {
		t.Errorf("want 1 entry after resolution, got %d", len(cm2.entries))
	}
}

func TestConflictModel_S_SkipsWithoutRemoving(t *testing.T) {
	dir := t.TempDir()
	m := newConflictModel(dir)
	m.SetSize(80, 24)
	m.entries = sampleConflicts()
	m.setListItems()

	initialCount := len(m.entries)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	cm, ok := next.(*conflictModel)
	if !ok {
		t.Fatalf("want *conflictModel, got %T", next)
	}
	if cmd != nil {
		t.Error("skip should not return a Cmd")
	}

	// Entry count must be unchanged.
	if len(cm.entries) != initialCount {
		t.Errorf("skip should not remove entries: want %d, got %d", initialCount, len(cm.entries))
	}
	// Selection should advance.
	if cm.list.Index() != 1 {
		t.Errorf("want selection at index 1 after skip, got %d", cm.list.Index())
	}
}

func TestConflictModel_SubprocessFailure_ShowsOverlay(t *testing.T) {
	dir := t.TempDir()
	m := newConflictModel(dir)
	m.SetSize(80, 24)
	m.entries = sampleConflicts()
	m.setListItems()

	m.runner = func(name string, args []string) ([]byte, error) {
		return []byte("permission denied\n"), errors.New("exit status 1")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if cmd == nil {
		t.Fatal("want a cmd from 'w'")
	}

	msg := cmd()
	next, _ := m.Update(msg)
	cm, ok := next.(*conflictModel)
	if !ok {
		t.Fatalf("want *conflictModel, got %T", next)
	}

	if cm.errorMsg == "" {
		t.Error("want non-empty errorMsg on subprocess failure")
	}
	// Entry must remain.
	if len(cm.entries) != 2 {
		t.Errorf("want 2 entries after failure, got %d", len(cm.entries))
	}
}

func TestConflictModel_DetailView_WinnerLoser(t *testing.T) {
	dir := t.TempDir()
	m := newConflictModel(dir)
	m.SetSize(80, 24)
	m.entries = sampleConflicts()
	m.setListItems()

	detail := m.detailView()
	if !strings.Contains(detail, "winner") {
		t.Error("detailView() should contain 'winner' label")
	}
	if !strings.Contains(detail, "loser") {
		t.Error("detailView() should contain 'loser' label")
	}
}
