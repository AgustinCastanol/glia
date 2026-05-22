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

// pullFakeAdapter controls pull-side behaviour independently of push.
type pullFakeAdapter struct {
	name           string
	healthErr      error
	writeErr       error
	fromCanonErr   error
	writeUnsupport bool   // WriteNative returns ErrUnsupported
	fromUnsupport  bool   // FromCanonical returns ErrUnsupported
	written        []string // canonical IDs written
}

func (f *pullFakeAdapter) Name() string                       { return f.name }
func (f *pullFakeAdapter) Health(_ context.Context) error     { return f.healthErr }
func (f *pullFakeAdapter) SupportedKinds() []string           { return nil }

func (f *pullFakeAdapter) ListNative(_ context.Context, _ string, _ time.Time) ([]adapter.NativeID, error) {
	return nil, nil
}

func (f *pullFakeAdapter) ReadNative(_ context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	return map[string]string{"id": string(id)}, nil
}

func (f *pullFakeAdapter) ToCanonical(native adapter.NativeRecord, _ adapter.IDMap) (store.CanonicalRecord, error) {
	return store.CanonicalRecord{}, adapter.ErrUnsupported
}

func (f *pullFakeAdapter) FromCanonical(r store.CanonicalRecord) (adapter.NativeRecord, error) {
	if f.fromUnsupport {
		return nil, adapter.ErrUnsupported
	}
	if f.fromCanonErr != nil {
		return nil, f.fromCanonErr
	}
	return map[string]string{"canonical_id": r.CanonicalID, "title": r.Title}, nil
}

func (f *pullFakeAdapter) WriteNative(_ context.Context, rec adapter.NativeRecord) (adapter.NativeID, error) {
	if f.writeUnsupport {
		return "", adapter.ErrUnsupported
	}
	if f.writeErr != nil {
		return "", f.writeErr
	}
	m := rec.(map[string]string)
	f.written = append(f.written, m["canonical_id"])
	return adapter.NativeID("native-" + m["canonical_id"]), nil
}

func seedCanonical(t *testing.T, s *store.Store, records []store.CanonicalRecord) []store.CanonicalRecord {
	t.Helper()
	out, err := s.AppendBatch(records)
	if err != nil {
		t.Fatalf("seed AppendBatch: %v", err)
	}
	return out
}

func openPullStore(t *testing.T) (*store.Store, string) {
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

func TestPull_WritesCanonicalToProvider(t *testing.T) {
	s, _ := openPullStore(t)

	seeded := seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "rec-1", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "x1"}},
		{Kind: "observation", Title: "rec-2", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "x2"}},
	})
	_ = seeded

	a := &pullFakeAdapter{name: "engram"}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	pr := report.PerProvider["engram"]
	if pr.Pulled != 2 {
		t.Errorf("pulled=%d, want 2", pr.Pulled)
	}
	if len(a.written) != 2 {
		t.Errorf("written=%d, want 2", len(a.written))
	}
}

func TestPull_OriginReImportGuard(t *testing.T) {
	s, _ := openPullStore(t)

	// Seed a record that ORIGINATES from "engram".
	now := time.Now().UTC().Format(time.RFC3339)
	seeded := seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "engram-own", ContentFormat: "markdown",
			UpdatedAt: now,
			Origin:    store.Origin{Provider: "engram", ProviderID: "eng-1"}},
	})

	// Bind the native ID so the guard sees an existing mapping.
	if err := s.BindProvider("engram", "eng-1", seeded[0].CanonicalID); err != nil {
		t.Fatalf("BindProvider: %v", err)
	}

	a := &pullFakeAdapter{name: "engram"}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	pr := report.PerProvider["engram"]
	if pr.Pulled != 0 {
		t.Errorf("pulled=%d, want 0 (re-import guard)", pr.Pulled)
	}
	if len(a.written) != 0 {
		t.Errorf("WriteNative called %d times, want 0", len(a.written))
	}
}

func TestPull_ErrUnsupportedSkippedSilently(t *testing.T) {
	s, _ := openPullStore(t)

	seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "some-rec", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "y1"}},
	})

	a := &pullFakeAdapter{name: "engram", writeUnsupport: true}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	// ErrUnsupported → skip silently, no hard error, pulled=0.
	if len(report.HardErrors) != 0 {
		t.Errorf("hard errors=%d, want 0", len(report.HardErrors))
	}
	pr := report.PerProvider["engram"]
	if pr.Pulled != 0 {
		t.Errorf("pulled=%d, want 0", pr.Pulled)
	}
}

func TestPull_DryRunWritesNothing(t *testing.T) {
	s, _ := openPullStore(t)

	seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "t1", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "z1"}},
	})

	a := &pullFakeAdapter{name: "engram"}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{DryRun: true}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull dry-run: %v", err)
	}

	// Dry-run counts the record.
	if report.PerProvider["engram"].Pulled != 1 {
		t.Errorf("dry-run pulled=%d, want 1", report.PerProvider["engram"].Pulled)
	}

	// But WriteNative was never called.
	if len(a.written) != 0 {
		t.Errorf("WriteNative called %d times in dry-run, want 0", len(a.written))
	}
}

func TestPull_HealthFailSkipsProvider(t *testing.T) {
	s, _ := openPullStore(t)

	a := &pullFakeAdapter{name: "engram", healthErr: errors.New("down")}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if len(report.HardErrors) != 1 {
		t.Errorf("hard errors=%d, want 1", len(report.HardErrors))
	}
}

func TestPull_FromCanonicalErrUnsupportedSkips(t *testing.T) {
	s, _ := openPullStore(t)

	seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "relation", Title: "rel", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "r1"}},
	})

	a := &pullFakeAdapter{name: "engram", fromUnsupport: true}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if len(report.HardErrors) != 0 {
		t.Errorf("hard errors=%d, want 0", len(report.HardErrors))
	}
}
