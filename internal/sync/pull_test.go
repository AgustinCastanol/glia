package sync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/store"
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

func (f *pullFakeAdapter) WriteCapability() string { return "read+write" }

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
	storeDir := filepath.Join(dir, ".glia")
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

// ---------------------------------------------------------------------------
// Phase 7 — ClaudeMemWriteErrors counter (REQ-CMW-08)
// ---------------------------------------------------------------------------

// TestPull_WriteErrorIncrements_WriteErrors verifies that when WriteNative
// returns a non-ErrUnsupported error, the ProviderResult.WriteErrors counter
// is incremented and no HardError is added (REQ-CMW-08).
func TestPull_WriteErrorIncrements_WriteErrors(t *testing.T) {
	s, _ := openPullStore(t)

	seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "write-fail", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "wf1"}},
	})

	a := &pullFakeAdapter{name: "claude-mem", writeErr: errors.New("connection reset")}
	e := New(s, map[string]adapter.Adapter{"claude-mem": a}, Default(), Options{}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	pr := report.PerProvider["claude-mem"]
	if pr.WriteErrors != 1 {
		t.Errorf("WriteErrors=%d, want 1", pr.WriteErrors)
	}
	// Non-fatal: no hard errors.
	if len(report.HardErrors) != 0 {
		t.Errorf("HardErrors=%d, want 0 (write errors are non-fatal)", len(report.HardErrors))
	}
}

// TestPull_WriteErrors_SummedInReport verifies that RunReport.WriteErrors
// aggregates the per-provider WriteErrors counters.
func TestPull_WriteErrors_SummedInReport(t *testing.T) {
	s, _ := openPullStore(t)

	seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "a", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "z1"}},
		{Kind: "observation", Title: "b", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "z2"}},
	})

	a := &pullFakeAdapter{name: "claude-mem", writeErr: errors.New("timeout")}
	e := New(s, map[string]adapter.Adapter{"claude-mem": a}, Default(), Options{}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if report.WriteErrors != 2 {
		t.Errorf("RunReport.WriteErrors=%d, want 2", report.WriteErrors)
	}
}

// ---------------------------------------------------------------------------
// Phase 6 — drift detection and BindProviderWithRevision (REQ-CMW-06/07)
// ---------------------------------------------------------------------------

// TestPull_DriftDetection_SkipsAlreadyPushedRevision verifies that a canonical
// record whose revision was already pushed to the provider (ProviderRevision
// returns the same revision) is skipped without calling WriteNative again
// (REQ-CMW-07).
func TestPull_DriftDetection_SkipsAlreadyPushedRevision(t *testing.T) {
	s, _ := openPullStore(t)

	seeded := seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "already-pushed", ContentFormat: "markdown",
			Revision:  2,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "x1"}},
	})

	// Simulate that revision 2 was already pushed to "engram".
	if err := s.BindProviderWithRevision("engram", "native-already", seeded[0].CanonicalID, 2); err != nil {
		t.Fatalf("BindProviderWithRevision: %v", err)
	}

	a := &pullFakeAdapter{name: "engram"}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	report, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	// Already pushed at revision 2 → skip, pulled=0.
	pr := report.PerProvider["engram"]
	if pr.Pulled != 0 {
		t.Errorf("pulled=%d, want 0 (drift detection: already at revision 2)", pr.Pulled)
	}
	if len(a.written) != 0 {
		t.Errorf("WriteNative called %d times, want 0 (already pushed at revision 2)", len(a.written))
	}
}

// TestPull_DriftDetection_PushesWhenRevisionAdvanced verifies that a record
// at a higher revision than what was previously pushed IS written (REQ-CMW-07).
// Seeds a two-revision chain so the live record is revision 2; previously
// pushed at revision 1 → engine must write again.
func TestPull_DriftDetection_PushesWhenRevisionAdvanced(t *testing.T) {
	s, _ := openPullStore(t)
	now := time.Now().UTC().Format(time.RFC3339)

	// Seed revision 1.
	seeded := seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "needs-update", ContentFormat: "markdown",
			UpdatedAt: now,
			Origin:    store.Origin{Provider: "other", ProviderID: "x2"}},
	})

	// Append revision 2 to the same canonical chain.
	seeded2, err := s.AppendBatch([]store.CanonicalRecord{
		{CanonicalID: seeded[0].CanonicalID,
			Kind: "observation", Title: "needs-update-v2", ContentFormat: "markdown",
			UpdatedAt: now,
			Origin:    store.Origin{Provider: "other", ProviderID: "x2"}},
	})
	if err != nil {
		t.Fatalf("AppendBatch revision 2: %v", err)
	}

	// Simulate: previously pushed at revision 1, live record is now revision 2.
	if err := s.BindProviderWithRevision("engram", "native-old", seeded2[0].CanonicalID, 1); err != nil {
		t.Fatalf("BindProviderWithRevision: %v", err)
	}

	a := &pullFakeAdapter{name: "engram"}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	report, err2 := e.Pull(context.Background())
	if err2 != nil {
		t.Fatalf("Pull: %v", err2)
	}

	// Revision advanced (1 → 2) → should write.
	pr := report.PerProvider["engram"]
	if pr.Pulled != 1 {
		t.Errorf("pulled=%d, want 1 (revision advanced from 1 to 2)", pr.Pulled)
	}
}

// TestPull_BindsWithRevisionAfterWrite verifies that after a successful
// WriteNative, the engine calls BindProviderWithRevision (not just BindProvider)
// so the revision watermark is recorded (REQ-CMW-05).
// The store assigns revision=1 to any new record, so the expected watermark is 1.
func TestPull_BindsWithRevisionAfterWrite(t *testing.T) {
	s, _ := openPullStore(t)

	seeded := seedCanonical(t, s, []store.CanonicalRecord{
		{Kind: "observation", Title: "bind-test", ContentFormat: "markdown",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Origin:    store.Origin{Provider: "other", ProviderID: "x3"}},
	})

	a := &pullFakeAdapter{name: "engram"}
	e := New(s, map[string]adapter.Adapter{"engram": a}, Default(), Options{}, nil)

	_, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	// After a successful write, ProviderRevision should record the canonical
	// record's actual revision (1 for a freshly seeded record).
	rev, ok := s.ProviderRevision("engram", seeded[0].CanonicalID)
	if !ok {
		t.Fatal("expected ProviderRevision to be set after pull write")
	}
	if rev != 1 {
		t.Errorf("ProviderRevision: got %d, want 1", rev)
	}
}
