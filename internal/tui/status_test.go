package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// fakeStatusRunner returns a commandRunner that always produces the given StatusJSON.
func fakeStatusRunner(s StatusJSON) commandRunner {
	return func(name string, args ...string) ([]byte, error) {
		b, err := json.Marshal(s)
		return b, err
	}
}

func TestStatusModel_HealthyProvider(t *testing.T) {
	dir := t.TempDir()
	m := newStatusModel(dir)
	m.SetSize(80, 24)

	// Inject data directly via statusDataMsg.
	msg := statusDataMsg{
		status: &StatusJSON{
			ProviderHealth: map[string]string{"engram": "ok"},
			SyncState:      map[string]store.ProviderSyncState{},
			LineCount:      42,
			FileSizeBytes:  1024,
		},
		stats: store.StoreStats{LineCount: 42, FileSizeBytes: 1024, SchemaVersion: 1},
	}
	next, _ := m.Update(msg)
	sm, ok := next.(*statusModel)
	if !ok {
		t.Fatalf("want *statusModel, got %T", next)
	}

	view := sm.View()
	if !strings.Contains(view, "✓") {
		t.Error("healthy provider should show ✓ glyph")
	}
}

func TestStatusModel_UnhealthyProvider(t *testing.T) {
	dir := t.TempDir()
	m := newStatusModel(dir)
	m.SetSize(80, 24)

	msg := statusDataMsg{
		status: &StatusJSON{
			ProviderHealth: map[string]string{"engram": "connection refused"},
			SyncState:      map[string]store.ProviderSyncState{},
		},
	}
	next, _ := m.Update(msg)
	sm, ok := next.(*statusModel)
	if !ok {
		t.Fatalf("want *statusModel, got %T", next)
	}

	view := sm.View()
	if !strings.Contains(view, "✗") {
		t.Error("unhealthy provider should show ✗ glyph")
	}
}

func TestStatusModel_WatermarksDisplayed(t *testing.T) {
	dir := t.TempDir()
	m := newStatusModel(dir)
	m.SetSize(80, 24)

	msg := statusDataMsg{
		status: &StatusJSON{
			ProviderHealth: map[string]string{"engram": "ok"},
			SyncState: map[string]store.ProviderSyncState{
				"engram": {
					LastPulledAt: "2026-05-22T10:00:00Z",
					LastPushedAt: "2026-05-22T09:00:00Z",
				},
			},
		},
	}
	next, _ := m.Update(msg)
	sm, ok := next.(*statusModel)
	if !ok {
		t.Fatalf("want *statusModel, got %T", next)
	}

	view := sm.View()
	if !strings.Contains(view, "pulled") {
		t.Error("view should contain 'pulled' watermark")
	}
	if !strings.Contains(view, "pushed") {
		t.Error("view should contain 'pushed' watermark")
	}
}

func TestStatusModel_StatsFields(t *testing.T) {
	dir := t.TempDir()
	m := newStatusModel(dir)
	m.SetSize(80, 24)

	msg := statusDataMsg{
		status: &StatusJSON{ProviderHealth: map[string]string{}, SyncState: map[string]store.ProviderSyncState{}},
		stats:  store.StoreStats{LineCount: 12345, FileSizeBytes: 2048, SchemaVersion: 1},
	}
	next, _ := m.Update(msg)
	sm, ok := next.(*statusModel)
	if !ok {
		t.Fatalf("want *statusModel, got %T", next)
	}

	view := sm.View()
	if !strings.Contains(view, "12345") {
		t.Error("view should contain line count 12345")
	}
}

func TestStatusModel_SyncKey_ReturnsCmd(t *testing.T) {
	dir := t.TempDir()
	m := newStatusModel(dir)
	m.SetSize(80, 24)

	// Press s — should return a Cmd (the sync subprocess).
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil {
		t.Error("pressing s should return a non-nil Cmd to launch sync subprocess")
	}
}

func TestStatusModel_PullKey_ReturnsCmd(t *testing.T) {
	dir := t.TempDir()
	m := newStatusModel(dir)
	m.SetSize(80, 24)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if cmd == nil {
		t.Error("pressing p should return a non-nil Cmd to launch sync pull subprocess")
	}
}

func TestStatusModel_PushKey_ReturnsCmd(t *testing.T) {
	dir := t.TempDir()
	m := newStatusModel(dir)
	m.SetSize(80, 24)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	if cmd == nil {
		t.Error("pressing P should return a non-nil Cmd to launch sync push subprocess")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.input)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
