package sync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// pushFakeAdapter is a richer fake used specifically for push tests.
type pushFakeAdapter struct {
	name         string
	healthErr    error
	nativeIDs    []adapter.NativeID
	listErr      error
	records      map[adapter.NativeID]store.CanonicalRecord // records returned by ReadNative via ToCanonical
	readErr      error
	toCanonErr   error
	unsupported  bool // ToCanonical returns ErrUnsupported
}

func (f *pushFakeAdapter) Name() string { return f.name }
func (f *pushFakeAdapter) Health(_ context.Context) error { return f.healthErr }
func (f *pushFakeAdapter) SupportedKinds() []string { return nil }

func (f *pushFakeAdapter) ListNative(_ context.Context, _ string, _ time.Time) ([]adapter.NativeID, error) {
	return f.nativeIDs, f.listErr
}

func (f *pushFakeAdapter) ReadNative(_ context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return map[string]string{"id": string(id)}, nil
}

func (f *pushFakeAdapter) ToCanonical(native adapter.NativeRecord, _ adapter.IDMap) (store.CanonicalRecord, error) {
	if f.unsupported {
		return store.CanonicalRecord{}, adapter.ErrUnsupported
	}
	if f.toCanonErr != nil {
		return store.CanonicalRecord{}, f.toCanonErr
	}
	m := native.(map[string]string)
	id := adapter.NativeID(m["id"])
	if r, ok := f.records[id]; ok {
		return r, nil
	}
	return store.CanonicalRecord{
		Kind:          "observation",
		Title:         m["id"],
		ContentFormat: "markdown",
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		Origin: store.Origin{
			Provider:   f.name,
			ProviderID: m["id"],
		},
	}, nil
}

func (f *pushFakeAdapter) FromCanonical(r store.CanonicalRecord) (adapter.NativeRecord, error) {
	return map[string]string{"canonical_id": r.CanonicalID}, nil
}

func (f *pushFakeAdapter) WriteNative(_ context.Context, _ adapter.NativeRecord) (adapter.NativeID, error) {
	return "written-id", nil
}

func openPushStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, ".wrapper-mems")
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, storeDir
}

func TestPush_NewRecords(t *testing.T) {
	s, _ := openPushStore(t)

	a := &pushFakeAdapter{
		name:      "engram",
		nativeIDs: []adapter.NativeID{"id-1", "id-2"},
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)
	report, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	pr := report.PerProvider["engram"]
	if pr.Pushed != 2 {
		t.Errorf("pushed=%d, want 2", pr.Pushed)
	}
	if pr.Skipped != 0 {
		t.Errorf("skipped=%d, want 0", pr.Skipped)
	}
}

func TestPush_SkipsEqualRecord(t *testing.T) {
	s, _ := openPushStore(t)

	// First push to seed the canonical store.
	now := time.Now().UTC().Format(time.RFC3339)
	a := &pushFakeAdapter{
		name:      "engram",
		nativeIDs: []adapter.NativeID{"id-1"},
		records: map[adapter.NativeID]store.CanonicalRecord{
			"id-1": {
				Kind:          "observation",
				Title:         "same title",
				Content:       "same content",
				ContentFormat: "markdown",
				UpdatedAt:     now,
				Origin:        store.Origin{Provider: "engram", ProviderID: "id-1"},
			},
		},
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	// First push — seeds the record.
	if _, err := e.Push(context.Background()); err != nil {
		t.Fatalf("first Push: %v", err)
	}

	// Second push — same payload → should skip.
	report2, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}

	pr := report2.PerProvider["engram"]
	if pr.Pushed != 0 {
		t.Errorf("pushed=%d, want 0 (should be skipped)", pr.Pushed)
	}
	if pr.Skipped != 1 {
		t.Errorf("skipped=%d, want 1", pr.Skipped)
	}
}

func TestPush_DryRunWritesNothing(t *testing.T) {
	s, _ := openPushStore(t)

	a := &pushFakeAdapter{
		name:      "engram",
		nativeIDs: []adapter.NativeID{"id-1"},
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{DryRun: true}, nil)
	report, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push dry-run: %v", err)
	}

	// Dry-run still counts pushed in the report.
	if report.PerProvider["engram"].Pushed != 1 {
		t.Errorf("dry-run pushed=%d, want 1", report.PerProvider["engram"].Pushed)
	}

	// But the canonical store must have no records.
	records, err := s.ListLive()
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("dry-run wrote %d records, want 0", len(records))
	}
}

func TestPush_HealthFailSkipsProvider(t *testing.T) {
	s, _ := openPushStore(t)

	a := &pushFakeAdapter{
		name:      "engram",
		healthErr: errors.New("unavailable"),
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)
	report, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if len(report.HardErrors) != 1 {
		t.Errorf("hard errors=%d, want 1", len(report.HardErrors))
	}
	if _, ok := report.PerProvider["engram"]; ok {
		t.Error("engram should not appear in PerProvider after Health failure")
	}
}

func TestPush_ReadNativeErrorSkipsRecord(t *testing.T) {
	s, _ := openPushStore(t)

	// Only the first call fails; use a counter-based adapter for precision.
	a2 := &countingReadAdapter{
		name:      "engram",
		nativeIDs: []adapter.NativeID{"id-bad", "id-good"},
		errOnCall: 0, // fail first ReadNative
	}

	e := New(s, map[string]adapter.Adapter{"engram": a2}, Default(), Options{}, nil)
	report, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	// id-good should still be pushed.
	pr := report.PerProvider["engram"]
	if pr.Pushed != 1 {
		t.Errorf("pushed=%d, want 1 (bad record skipped)", pr.Pushed)
	}
}

func TestPush_MaxCapRecords(t *testing.T) {
	s, _ := openPushStore(t)

	a := &pushFakeAdapter{
		name:      "engram",
		nativeIDs: []adapter.NativeID{"id-1", "id-2", "id-3", "id-4", "id-5"},
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{Max: 2}, nil)
	report, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	pr := report.PerProvider["engram"]
	if pr.Pushed != 2 {
		t.Errorf("pushed=%d, want 2 (max cap)", pr.Pushed)
	}
}

func TestPush_ErrUnsupportedSkippedSilently(t *testing.T) {
	s, _ := openPushStore(t)

	a := &pushFakeAdapter{
		name:        "engram",
		nativeIDs:   []adapter.NativeID{"rel-1"},
		unsupported: true, // ToCanonical returns ErrUnsupported
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)
	report, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	pr := report.PerProvider["engram"]
	if pr.Pushed != 0 || pr.Skipped != 0 {
		t.Errorf("pushed=%d skipped=%d, want 0/0 for ErrUnsupported", pr.Pushed, pr.Skipped)
	}
}

// countingReadAdapter returns an error only on a specific ReadNative call index.
type countingReadAdapter struct {
	name      string
	nativeIDs []adapter.NativeID
	callIdx   int
	errOnCall int
}

func (f *countingReadAdapter) Name() string  { return f.name }
func (f *countingReadAdapter) Health(_ context.Context) error { return nil }
func (f *countingReadAdapter) SupportedKinds() []string       { return nil }

func (f *countingReadAdapter) ListNative(_ context.Context, _ string, _ time.Time) ([]adapter.NativeID, error) {
	return f.nativeIDs, nil
}

func (f *countingReadAdapter) ReadNative(_ context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	idx := f.callIdx
	f.callIdx++
	if idx == f.errOnCall {
		return nil, errors.New("simulated read error")
	}
	return map[string]string{"id": string(id)}, nil
}

func (f *countingReadAdapter) ToCanonical(native adapter.NativeRecord, _ adapter.IDMap) (store.CanonicalRecord, error) {
	m := native.(map[string]string)
	return store.CanonicalRecord{
		Kind:          "observation",
		Title:         m["id"],
		ContentFormat: "markdown",
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		Origin:        store.Origin{Provider: f.name, ProviderID: m["id"]},
	}, nil
}

func (f *countingReadAdapter) FromCanonical(r store.CanonicalRecord) (adapter.NativeRecord, error) {
	return map[string]string{"canonical_id": r.CanonicalID}, nil
}

func (f *countingReadAdapter) WriteNative(_ context.Context, _ adapter.NativeRecord) (adapter.NativeID, error) {
	return "written-id", nil
}
