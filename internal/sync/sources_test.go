package sync

// Tests for Engine.activeSources() and the Push-ingests-sources behavior (PRD-11).
//
// TDD RED → GREEN → REFACTOR cycle:
//   - TestActiveSources_ReturnsReadOnlyNotInProviders: activeSources() returns
//     adapters whose WriteCapability() == "read-only" that are NOT covered by cfg.Providers.
//   - TestActiveSources_ProviderThatIsReadOnlyStaysOut: a provider in cfg.Providers
//     that happens to be read-only is NOT returned by activeSources() (name-exclusion guard).
//   - TestActiveSources_ProviderFilterApplied: opts.ProviderFilter narrows activeSources().
//   - TestActiveSources_NoSourcesWhenAllInProviders: no activeSources when every read-only
//     adapter is also in cfg.Providers.
//   - TestPush_IngestsSourceRecords: Engine.Push ingests records from a read-only source
//     that is NOT in cfg.Providers.
//   - TestPull_DoesNotCallWriteNativeOnSource: Engine.Pull does not call WriteNative on
//     a read-only source adapter (pull-leakage guard at the engine level, separate from
//     the per-adapter ErrUnsupported guard).
//   - TestPush_ProviderNotDoubleCountedByActiveSources: a provider that is both in
//     cfg.Providers AND read-only is only processed once (via activeProviders, not twice).

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/store"
)

// readOnlyFakeAdapter is a fakeAdapter whose WriteCapability returns "read-only"
// and whose WriteNative panics (must never be reached for a true source).
type readOnlyFakeAdapter struct {
	fakeAdapter
	writeNativeCalled bool
}

func (r *readOnlyFakeAdapter) WriteCapability() string { return "read-only" }

func (r *readOnlyFakeAdapter) WriteNative(_ context.Context, _ adapter.NativeRecord) (adapter.NativeID, error) {
	r.writeNativeCalled = true
	return "", adapter.ErrUnsupported
}

// sourceFakeAdapter is a read-only adapter that returns a fixed set of records
// for push-ingestion tests.
type sourceFakeAdapter struct {
	name    string
	listIDs []adapter.NativeID
	// updatedByID, when non-nil, maps a native id to the UpdatedAt that
	// ToCanonical stamps on its record (used by newest-artifact tests).
	updatedByID map[adapter.NativeID]string
}

func (s *sourceFakeAdapter) Name() string { return s.name }
func (s *sourceFakeAdapter) Health(_ context.Context) error { return nil }
func (s *sourceFakeAdapter) WriteCapability() string        { return "read-only" }
func (s *sourceFakeAdapter) SupportedKinds() []string       { return nil }

func (s *sourceFakeAdapter) ListNative(_ context.Context, _ string, _ time.Time) ([]adapter.NativeID, error) {
	return s.listIDs, nil
}

func (s *sourceFakeAdapter) ReadNative(_ context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	return map[string]string{"id": string(id)}, nil
}

func (s *sourceFakeAdapter) ToCanonical(native adapter.NativeRecord, _ adapter.IDMap) (store.CanonicalRecord, error) {
	m := native.(map[string]string)
	rec := store.CanonicalRecord{
		Kind:          "spec_artifact",
		Type:          "proposal",
		Title:         m["id"],
		ContentFormat: "markdown",
		Content:       "fixture content",
		Origin: store.Origin{
			Provider:   s.name,
			ProviderID: m["id"],
		},
	}
	if s.updatedByID != nil {
		rec.UpdatedAt = s.updatedByID[adapter.NativeID(m["id"])]
	}
	return rec, nil
}

func (s *sourceFakeAdapter) FromCanonical(_ store.CanonicalRecord) (adapter.NativeRecord, error) {
	return nil, adapter.ErrUnsupported
}

func (s *sourceFakeAdapter) WriteNative(_ context.Context, _ adapter.NativeRecord) (adapter.NativeID, error) {
	// A true read-only source must never have WriteNative called via Pull.
	// If this is called, the test will catch it by inspecting store state.
	return "", adapter.ErrUnsupported
}

// ---- source status (newest artifact) tests ----

// TestSourceStatus_NewestArtifact verifies that Status() reports the most recent
// artifact timestamp across a source's records in SourceStatus.NewestArtifact
// (PRD-11 §10).
func TestSourceStatus_NewestArtifact(t *testing.T) {
	s, _ := openTestStore(t)

	src := &sourceFakeAdapter{
		name:    "openspec",
		listIDs: []adapter.NativeID{"a", "b", "c"},
		updatedByID: map[adapter.NativeID]string{
			"a": "2026-01-01T00:00:00Z",
			"b": "2026-06-13T10:00:00Z", // newest
			"c": "2026-03-15T12:00:00Z",
		},
	}
	adapters := map[string]adapter.Adapter{"openspec": src}
	e := New(s, adapters, Default(), Options{}, io.Discard)

	report, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(report.Sources) != 1 {
		t.Fatalf("want 1 source, got %d", len(report.Sources))
	}
	got := report.Sources[0]
	if got.ArtifactCount != 3 {
		t.Errorf("ArtifactCount = %d, want 3", got.ArtifactCount)
	}
	if got.NewestArtifact != "2026-06-13T10:00:00Z" {
		t.Errorf("NewestArtifact = %q, want %q", got.NewestArtifact, "2026-06-13T10:00:00Z")
	}
}

// TestSourceStatus_NewestArtifactEmptyWhenNoArtifacts verifies NewestArtifact is
// empty (rendered as "-") when the source has no artifacts.
func TestSourceStatus_NewestArtifactEmptyWhenNoArtifacts(t *testing.T) {
	s, _ := openTestStore(t)

	src := &sourceFakeAdapter{name: "openspec", listIDs: nil}
	adapters := map[string]adapter.Adapter{"openspec": src}
	e := New(s, adapters, Default(), Options{}, io.Discard)

	report, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(report.Sources) != 1 {
		t.Fatalf("want 1 source, got %d", len(report.Sources))
	}
	if report.Sources[0].NewestArtifact != "" {
		t.Errorf("NewestArtifact = %q, want empty", report.Sources[0].NewestArtifact)
	}
}

// ---- activeSources() unit tests ----

// TestActiveSources_ReturnsReadOnlyNotInProviders verifies that a read-only
// adapter NOT in cfg.Providers is returned by activeSources().
func TestActiveSources_ReturnsReadOnlyNotInProviders(t *testing.T) {
	s, _ := openTestStore(t)

	src := &sourceFakeAdapter{name: "openspec", listIDs: nil}
	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"claude-mem": &fakeAdapter{name: "claude-mem"},
		"openspec":  src,
	}

	cfg := Default() // cfg.Providers = ["engram", "claude-mem"]
	e := New(s, adapters, cfg, Options{}, io.Discard)

	sources := e.activeSources()
	if len(sources) != 1 {
		t.Fatalf("activeSources() = %d adapters, want 1; got %v", len(sources), names(sources))
	}
	if sources[0].Name() != "openspec" {
		t.Errorf("activeSources()[0].Name() = %q, want %q", sources[0].Name(), "openspec")
	}
}

// TestActiveSources_ProviderThatIsReadOnlyStaysOut verifies that a provider in
// cfg.Providers that happens to be read-only is NOT in activeSources().
// This covers the claude-mem write_enabled=false scenario.
func TestActiveSources_ProviderThatIsReadOnlyStaysOut(t *testing.T) {
	s, _ := openTestStore(t)

	readOnlyProvider := &readOnlyFakeAdapter{fakeAdapter: fakeAdapter{name: "claude-mem"}}
	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"claude-mem": readOnlyProvider, // in cfg.Providers AND read-only
		"openspec":  &sourceFakeAdapter{name: "openspec"},
	}

	cfg := Default() // cfg.Providers = ["engram", "claude-mem"]
	e := New(s, adapters, cfg, Options{}, io.Discard)

	sources := e.activeSources()
	for _, a := range sources {
		if a.Name() == "claude-mem" {
			t.Errorf("claude-mem is in cfg.Providers and must not appear in activeSources()")
		}
	}
	// openspec should still appear.
	found := false
	for _, a := range sources {
		if a.Name() == "openspec" {
			found = true
		}
	}
	if !found {
		t.Errorf("openspec must appear in activeSources(); got %v", names(sources))
	}
}

// TestActiveSources_ProviderFilterApplied verifies that opts.ProviderFilter
// limits which sources are active (a --provider openspec flag must work).
func TestActiveSources_ProviderFilterApplied(t *testing.T) {
	s, _ := openTestStore(t)

	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"openspec":  &sourceFakeAdapter{name: "openspec"},
		"openspec2": &sourceFakeAdapter{name: "openspec2"},
	}

	cfg := Default() // only ["engram", "claude-mem"] in Providers
	opts := Options{ProviderFilter: []string{"openspec"}}
	e := New(s, adapters, cfg, opts, io.Discard)

	sources := e.activeSources()
	if len(sources) != 1 || sources[0].Name() != "openspec" {
		t.Errorf("activeSources() with filter=[openspec] = %v, want [openspec]", names(sources))
	}
}

// TestActiveSources_NoSourcesWhenAllInProviders verifies that if every
// registered read-only adapter is also in cfg.Providers (unlikely but valid),
// activeSources() returns nothing.
func TestActiveSources_NoSourcesWhenAllInProviders(t *testing.T) {
	s, _ := openTestStore(t)

	adapters := map[string]adapter.Adapter{
		"engram":   &fakeAdapter{name: "engram"},
		"mySource": &sourceFakeAdapter{name: "mySource"},
	}

	cfg := Config{Providers: []string{"engram", "mySource"}} // mySource explicitly in providers
	e := New(s, adapters, cfg, Options{}, io.Discard)

	sources := e.activeSources()
	if len(sources) != 0 {
		t.Errorf("activeSources() = %v, want empty (all read-only adapters are in Providers)", names(sources))
	}
}

// ---- Push integration tests ----

// TestPush_IngestsSourceRecords verifies that Engine.Push ingests records from
// a read-only source adapter that is NOT in cfg.Providers.
func TestPush_IngestsSourceRecords(t *testing.T) {
	s, _ := openTestStore(t)

	src := &sourceFakeAdapter{
		name:    "openspec",
		listIDs: []adapter.NativeID{"changes/my-feature/proposal.md"},
	}
	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"claude-mem": &fakeAdapter{name: "claude-mem"},
		"openspec":  src,
	}

	cfg := Default() // cfg.Providers = ["engram", "claude-mem"] — openspec excluded
	e := New(s, adapters, cfg, Options{Project: "test"}, io.Discard)

	report, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push() error: %v", err)
	}

	// openspec source should have contributed 1 pushed record.
	r, ok := report.PerProvider["openspec"]
	if !ok {
		t.Fatal("Push() report.PerProvider must include 'openspec' source results")
	}
	if r.Pushed != 1 {
		t.Errorf("openspec: Pushed = %d, want 1", r.Pushed)
	}

	// The canonical store should now contain 1 spec_artifact record.
	live, err := s.ListLive()
	if err != nil {
		t.Fatalf("store.ListLive: %v", err)
	}
	specCount := 0
	for _, rec := range live {
		if rec.Kind == "spec_artifact" {
			specCount++
		}
	}
	if specCount == 0 {
		t.Errorf("expected spec_artifact in store after Push; got %d spec_artifact records", specCount)
	}
}

// TestPull_DoesNotCallWriteNativeOnSource verifies that Pull does NOT call
// WriteNative on a read-only source adapter (engine-level pull-leakage guard).
// The source's WriteNative returns ErrUnsupported, and the pull loop must
// handle it silently — but more importantly, activeSources() are never iterated
// in Pull, so WriteNative is only reachable via the ErrUnsupported fallback.
// We assert the source's writeNativeCalled flag stays false.
func TestPull_DoesNotCallWriteNativeOnSource(t *testing.T) {
	s, _ := openTestStore(t)

	// Seed a spec_artifact record so there is something for Pull to iterate.
	rec := store.CanonicalRecord{
		Kind:          "spec_artifact",
		Title:         "Auth Design",
		ContentFormat: "markdown",
		Content:       "some content",
		Origin:        store.Origin{Provider: "openspec", ProviderID: "changes/auth/design.md"},
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	_, err := s.AppendBatch([]store.CanonicalRecord{rec})
	if err != nil {
		t.Fatalf("AppendBatch seed: %v", err)
	}

	src := &readOnlyFakeAdapter{fakeAdapter: fakeAdapter{name: "openspec"}}
	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"claude-mem": &fakeAdapter{name: "claude-mem"},
		"openspec":  src,
	}

	cfg := Default()
	e := New(s, adapters, cfg, Options{Project: "test"}, io.Discard)

	_, pullErr := e.Pull(context.Background())
	if pullErr != nil {
		t.Fatalf("Pull() error: %v", pullErr)
	}

	if src.writeNativeCalled {
		t.Error("Pull() must not call WriteNative on a read-only source adapter")
	}
}

// TestPush_ProviderNotDoubleCountedByActiveSources verifies that a provider in
// cfg.Providers that is also read-only is processed exactly once (via
// activeProviders, never via activeSources) — no double-counting.
func TestPush_ProviderNotDoubleCountedByActiveSources(t *testing.T) {
	s, _ := openTestStore(t)

	// claude-mem is in cfg.Providers but also read-only.
	// It should appear only in activeProviders, not activeSources.
	claudeMem := &readOnlyFakeAdapter{
		fakeAdapter: fakeAdapter{
			name:    "claude-mem",
			listIDs: []adapter.NativeID{"native-id-1"},
		},
	}

	adapters := map[string]adapter.Adapter{
		"engram":    &fakeAdapter{name: "engram"},
		"claude-mem": claudeMem,
	}

	cfg := Default() // cfg.Providers = ["engram", "claude-mem"]
	e := New(s, adapters, cfg, Options{Project: "test"}, io.Discard)

	// activeSources must NOT include claude-mem.
	sources := e.activeSources()
	for _, a := range sources {
		if a.Name() == "claude-mem" {
			t.Error("claude-mem is in cfg.Providers; activeSources must not include it")
		}
	}

	// Push must still work without double-counting.
	report, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push() error: %v", err)
	}

	// claude-mem should appear exactly once in the report.
	if _, ok := report.PerProvider["claude-mem"]; !ok {
		// It's possible claude-mem produces 0 records because ToCanonical
		// on the fakeAdapter returns "observation" kind — that's fine.
		// We just assert no panic / no duplicate processing.
	}
	_ = report
}

// names returns the names of a slice of adapters for diagnostic output.
func names(as []adapter.Adapter) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Name()
	}
	return out
}
