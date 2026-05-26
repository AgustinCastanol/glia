package sync

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/store"
)

// fakeAdapter is a minimal adapter.Adapter implementation for testing.
type fakeAdapter struct {
	name        string
	healthErr   error
	listIDs     []adapter.NativeID
	listErr     error
	readErr     error
	writeErr    error
	unsupported bool // WriteNative returns ErrUnsupported when true
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) Health(_ context.Context) error { return f.healthErr }

func (f *fakeAdapter) ListNative(_ context.Context, _ string, _ time.Time) ([]adapter.NativeID, error) {
	return f.listIDs, f.listErr
}

func (f *fakeAdapter) ReadNative(_ context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return map[string]string{"id": string(id)}, nil
}

func (f *fakeAdapter) ToCanonical(native adapter.NativeRecord, _ adapter.IDMap) (store.CanonicalRecord, error) {
	m, ok := native.(map[string]string)
	if !ok {
		return store.CanonicalRecord{}, adapter.ErrUnsupported
	}
	return store.CanonicalRecord{
		Kind:          "observation",
		Title:         m["id"],
		ContentFormat: "markdown",
		Origin: store.Origin{
			Provider:   f.name,
			ProviderID: m["id"],
		},
	}, nil
}

func (f *fakeAdapter) FromCanonical(r store.CanonicalRecord) (adapter.NativeRecord, error) {
	if f.unsupported {
		return nil, adapter.ErrUnsupported
	}
	return map[string]string{"canonical_id": r.CanonicalID, "title": r.Title}, nil
}

func (f *fakeAdapter) WriteNative(_ context.Context, _ adapter.NativeRecord) (adapter.NativeID, error) {
	if f.unsupported {
		return "", adapter.ErrUnsupported
	}
	if f.writeErr != nil {
		return "", f.writeErr
	}
	return adapter.NativeID("written-id"), nil
}

func (f *fakeAdapter) SupportedKinds() []string { return nil }

// openTestStore opens a fresh store in a temp dir.
func openTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".glia")
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, storeDir
}

// TestNew verifies that New returns a non-nil Engine.
func TestNew(t *testing.T) {
	s, _ := openTestStore(t)
	adapters := map[string]adapter.Adapter{
		"engram": &fakeAdapter{name: "engram"},
	}
	cfg := Default()
	opts := Options{}
	e := New(s, adapters, cfg, opts, io.Discard)
	if e == nil {
		t.Fatal("New() returned nil")
	}
}

// TestNew_NilWriter uses a nil writer (should be treated as Discard).
func TestNew_NilWriter(t *testing.T) {
	s, _ := openTestStore(t)
	e := New(s, nil, Default(), Options{}, nil)
	if e == nil {
		t.Fatal("New() with nil writer returned nil")
	}
}

// TestStatus_AllHealthy verifies ProviderHealth entries are nil when healthy.
func TestStatus_AllHealthy(t *testing.T) {
	s, _ := openTestStore(t)
	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"claude-mem": &fakeAdapter{name: "claude-mem"},
	}
	e := New(s, adapters, Default(), Options{}, io.Discard)

	report, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	for name, healthErr := range report.ProviderHealth {
		if healthErr != nil {
			t.Errorf("provider %s: expected healthy, got %v", name, healthErr)
		}
	}
}

// TestStatus_OneUnhealthy verifies a provider Health error is surfaced.
func TestStatus_OneUnhealthy(t *testing.T) {
	s, _ := openTestStore(t)
	wantErr := errors.New("connection refused")
	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram", healthErr: wantErr},
		"claude-mem": &fakeAdapter{name: "claude-mem"},
	}
	e := New(s, adapters, Default(), Options{}, io.Discard)

	report, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	got := report.ProviderHealth["engram"]
	if got == nil {
		t.Fatal("engram should be unhealthy")
	}
	if !errors.Is(got, wantErr) {
		t.Errorf("ProviderHealth[engram] = %v, want %v", got, wantErr)
	}
	if report.ProviderHealth["claude-mem"] != nil {
		t.Error("claude-mem should be healthy")
	}
}

// TestStatus_ProviderFilterApplied verifies that ProviderFilter restricts Status.
func TestStatus_ProviderFilterApplied(t *testing.T) {
	s, _ := openTestStore(t)
	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"claude-mem": &fakeAdapter{name: "claude-mem"},
	}
	cfg := Default()
	opts := Options{ProviderFilter: []string{"engram"}}
	e := New(s, adapters, cfg, opts, io.Discard)

	report, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if _, ok := report.ProviderHealth["claude-mem"]; ok {
		t.Error("claude-mem should not appear when filtered out")
	}
	if _, ok := report.ProviderHealth["engram"]; !ok {
		t.Error("engram should appear when included in filter")
	}
}

// TestStatus_NoConflicts verifies Conflicts is empty when store is clean.
func TestStatus_NoConflicts(t *testing.T) {
	s, _ := openTestStore(t)
	e := New(s, map[string]adapter.Adapter{}, Default(), Options{}, io.Discard)

	report, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if len(report.Conflicts) != 0 {
		t.Errorf("expected no conflicts, got %d", len(report.Conflicts))
	}
}

// TestResolve_NonExistentConflict verifies the "no conflict found" error.
func TestResolve_NonExistentConflict(t *testing.T) {
	s, _ := openTestStore(t)
	e := New(s, nil, Default(), Options{}, io.Discard)

	err := e.Resolve("nonexistent-id", 1)
	if err == nil {
		t.Fatal("Resolve() should error for non-existent conflict")
	}
	if err.Error() != "no conflict found for nonexistent-id" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

// TestResolve_InvalidDupIndex verifies the out-of-range dup_index error.
func TestResolve_InvalidDupIndex(t *testing.T) {
	s, _ := openTestStore(t)

	// Inject a conflict with 2 duplicates directly into the store.
	conflict := store.ConflictEntry{
		CanonicalID: "test-cid",
		Revision:    1,
		DetectedAt:  time.Now().UTC().Format(time.RFC3339),
		Duplicates: []store.ConflictDuplicate{
			{LineOffset: 0, LineULID: "ulid1", UpdatedAt: "2024-01-01T00:00:00Z", Provider: "engram"},
			{LineOffset: 0, LineULID: "ulid2", UpdatedAt: "2024-01-02T00:00:00Z", Provider: "engram"},
		},
	}
	if err := s.AppendConflict(conflict); err != nil {
		t.Fatalf("AppendConflict: %v", err)
	}

	e := New(s, nil, Default(), Options{}, io.Discard)

	// dupIndex 5 is out of range for a 2-duplicate conflict.
	err := e.Resolve("test-cid", 5)
	if err == nil {
		t.Fatal("Resolve() should error for invalid dup_index")
	}
	want := "invalid dup_index: 5 (conflict has 2 duplicates)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// TestResolve_InvalidDupIndex_Zero verifies dup_index=0 (below 1-based minimum).
func TestResolve_InvalidDupIndex_Zero(t *testing.T) {
	s, _ := openTestStore(t)

	conflict := store.ConflictEntry{
		CanonicalID: "test-cid",
		Revision:    1,
		DetectedAt:  time.Now().UTC().Format(time.RFC3339),
		Duplicates: []store.ConflictDuplicate{
			{LineOffset: 0, LineULID: "ulid1", UpdatedAt: "2024-01-01T00:00:00Z", Provider: "engram"},
		},
	}
	if err := s.AppendConflict(conflict); err != nil {
		t.Fatalf("AppendConflict: %v", err)
	}

	e := New(s, nil, Default(), Options{}, io.Discard)

	err := e.Resolve("test-cid", 0)
	if err == nil {
		t.Fatal("Resolve() should error for dup_index=0")
	}
	want := "invalid dup_index: 0 (conflict has 1 duplicates)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// TestActiveProviders_FilterApplied verifies activeProviders respects ProviderFilter.
func TestActiveProviders_FilterApplied(t *testing.T) {
	s, _ := openTestStore(t)
	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"claude-mem": &fakeAdapter{name: "claude-mem"},
	}
	cfg := Default()
	cfg.Providers = []string{"engram", "claude-mem"}
	opts := Options{ProviderFilter: []string{"claude-mem"}}
	e := New(s, adapters, cfg, opts, io.Discard)

	active := e.activeProviders()
	if len(active) != 1 || active[0].Name() != "claude-mem" {
		t.Errorf("activeProviders() = %v, want [claude-mem]", active)
	}
}
