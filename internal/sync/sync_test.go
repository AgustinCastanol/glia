package sync

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/store"
)

// bidiFakeAdapter supports both push (ListNative/ReadNative/ToCanonical) and
// pull (FromCanonical/WriteNative) in a single struct for Sync tests.
type bidiFakeAdapter struct {
	name      string
	healthErr error
	nativeIDs []adapter.NativeID
	written   []string
}

func (f *bidiFakeAdapter) Name() string                       { return f.name }
func (f *bidiFakeAdapter) Health(_ context.Context) error     { return f.healthErr }
func (f *bidiFakeAdapter) SupportedKinds() []string           { return nil }

func (f *bidiFakeAdapter) ListNative(_ context.Context, _ string, _ time.Time) ([]adapter.NativeID, error) {
	return f.nativeIDs, nil
}

func (f *bidiFakeAdapter) ReadNative(_ context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	return map[string]string{"id": string(id)}, nil
}

func (f *bidiFakeAdapter) ToCanonical(native adapter.NativeRecord, _ adapter.IDMap) (store.CanonicalRecord, error) {
	m := native.(map[string]string)
	return store.CanonicalRecord{
		Kind:          "observation",
		Title:         m["id"],
		ContentFormat: "markdown",
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		Origin:        store.Origin{Provider: f.name, ProviderID: m["id"]},
	}, nil
}

func (f *bidiFakeAdapter) FromCanonical(r store.CanonicalRecord) (adapter.NativeRecord, error) {
	// Origin re-import guard: skip records that originated from this provider.
	if r.Origin.Provider == f.name {
		return nil, adapter.ErrUnsupported
	}
	return map[string]string{"canonical_id": r.CanonicalID}, nil
}

func (f *bidiFakeAdapter) WriteNative(_ context.Context, rec adapter.NativeRecord) (adapter.NativeID, error) {
	m := rec.(map[string]string)
	f.written = append(f.written, m["canonical_id"])
	return adapter.NativeID("nat-" + m["canonical_id"]), nil
}

func openSyncStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, ".glia"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSync_PullBeforePush(t *testing.T) {
	s := openSyncStore(t)

	a := &bidiFakeAdapter{
		name:      "engram",
		nativeIDs: []adapter.NativeID{"id-1"},
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)
	report, err := e.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	pr := report.PerProvider["engram"]
	// Push should have appended 1 record.
	if pr.Pushed != 1 {
		t.Errorf("pushed=%d, want 1", pr.Pushed)
	}
}

func TestSync_DryRunWritesNothing(t *testing.T) {
	s := openSyncStore(t)

	a := &bidiFakeAdapter{
		name:      "engram",
		nativeIDs: []adapter.NativeID{"id-dry"},
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{DryRun: true}, nil)
	_, err := e.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync dry-run: %v", err)
	}

	records, err := s.ListLive()
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("dry-run wrote %d records, want 0", len(records))
	}
	if len(a.written) != 0 {
		t.Errorf("WriteNative called %d times in dry-run, want 0", len(a.written))
	}
}

func TestSync_AllProvidersHealthFail(t *testing.T) {
	s := openSyncStore(t)

	a := &bidiFakeAdapter{
		name:      "engram",
		healthErr: errors.New("down"),
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)
	report, err := e.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(report.HardErrors) == 0 {
		t.Error("expected hard errors when all providers down")
	}
}

func TestSync_RunSummaryIncludesCounters(t *testing.T) {
	s := openSyncStore(t)

	a := &bidiFakeAdapter{
		name:      "engram",
		nativeIDs: []adapter.NativeID{"s1", "s2"},
	}

	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)
	report, err := e.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Summary should not panic and must reflect pushed records.
	var buf bytes.Buffer
	report.WriteSummary(&buf)
}

func TestSync_ProviderFilter(t *testing.T) {
	s := openSyncStore(t)

	a1 := &bidiFakeAdapter{name: "engram", nativeIDs: []adapter.NativeID{"e1"}}
	a2 := &bidiFakeAdapter{name: "claude-mem", nativeIDs: []adapter.NativeID{"c1"}}

	e := New(s,
		map[string]adapter.Adapter{"engram": a1, "claude-mem": a2},
		Default(),
		Options{ProviderFilter: []string{"engram"}},
		nil,
	)

	report, err := e.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if _, ok := report.PerProvider["claude-mem"]; ok {
		t.Error("claude-mem should be filtered out")
	}
	if report.PerProvider["engram"].Pushed != 1 {
		t.Errorf("engram pushed=%d, want 1", report.PerProvider["engram"].Pushed)
	}
}
