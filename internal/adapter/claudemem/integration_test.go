//go:build integration

// PRE-MERGE GATE: DISCHARGED 2026-05-20.
//
// Original gate (obs #82, 2026-05-19) failed because the provisional
// claudeMemRecord struct did not match the live worker's /api/observations
// response. The rework on branch fix/claude-mem-adapter-mapping reconfirmed the
// shape live, rewrote claudeMemRecord + ToCanonical + ListNative + FromCanonical
// against the verified field names, and replaced this test with positive
// assertions that exercise the adapter end-to-end against the live worker.
//
// This test now DISCHARGES the gate: it asserts the adapter actually decodes
// real worker data, produces a valid CanonicalRecord, and surfaces non-empty
// content. If the worker is unreachable the test skips gracefully.

package claudemem

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
)

// verifiedItemKeys are the keys the live worker MUST return on each item. Any
// regression here re-trips the gate.
var verifiedItemKeys = []string{
	"id",
	"memory_session_id",
	"project",
	"type",
	"title",
	"narrative",
	"created_at",
}

// knownAbsentKeys are the legacy provisional field names that do NOT exist in
// the real API. Their reappearance would indicate a schema change.
var knownAbsentKeys = []string{
	"updated_at",
	"session_id",
	"project_id",
	"summary",
	"tags",
}

func workerBaseURL() string { return resolveBaseURL("") }

// liveAdapter constructs a real adapter against the live worker, or skips the
// test if the worker is unreachable.
func liveAdapter(t *testing.T) *ClaudeMemAdapter {
	t.Helper()
	a := New(Config{}, NewHTTPTransport(""))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Health(ctx); err != nil {
		t.Skipf("claude-mem worker unreachable at %s — gate discharge test skipped (%v)", workerBaseURL(), err)
	}
	return a
}

// TestPreMergeGate_HealthOK confirms the adapter's Health() succeeds against the
// live worker. Skipped if the worker is unreachable.
func TestPreMergeGate_HealthOK(t *testing.T) {
	_ = liveAdapter(t)
	t.Logf("gate discharge: GET /health OK at %s", workerBaseURL())
}

// TestPreMergeGate_ShapeContract reconfirms the verified field-name contract
// against the live worker by fetching one page and inspecting one item.
func TestPreMergeGate_ShapeContract(t *testing.T) {
	_ = liveAdapter(t)

	tr := NewHTTPTransport("")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	body, err := tr.ListPage(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListPage failed: %v", err)
	}

	var envelope struct {
		Items   []json.RawMessage `json:"items"`
		HasMore bool              `json:"hasMore"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("envelope decode: %v\nbody: %s", err, body)
	}
	if len(envelope.Items) == 0 {
		t.Skip("worker has 0 observations — cannot exercise shape contract")
	}

	var firstItem map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Items[0], &firstItem); err != nil {
		t.Fatalf("first item decode: %v", err)
	}

	for _, key := range verifiedItemKeys {
		if _, present := firstItem[key]; !present {
			t.Errorf("gate REGRESSION: verified key %q missing — schema drift?", key)
		}
	}
	for _, key := range knownAbsentKeys {
		if _, present := firstItem[key]; present {
			t.Errorf("gate REGRESSION: legacy key %q reappeared — adapter mapping may need rework", key)
		}
	}

	// id must decode as a JSON number into int64 (this is the primary hard-break
	// fixed by the rework — the provisional ID string field is now int64).
	if idRaw, ok := firstItem["id"]; ok {
		var idAsInt int64
		if err := json.Unmarshal(idRaw, &idAsInt); err != nil {
			t.Errorf("gate REGRESSION: id is not numeric int64: raw=%s err=%v", idRaw, err)
		} else {
			t.Logf("gate discharge: id decodes as int64=%d", idAsInt)
		}
	}
}

// TestPreMergeGate_AdapterEndToEnd exercises ListNative + ReadNative + ToCanonical
// against the live worker. This is the positive proof that the adapter actually
// works against real data — replaces the previous "gate FAILED" assertions.
func TestPreMergeGate_AdapterEndToEnd(t *testing.T) {
	a := liveAdapter(t)

	tr := NewHTTPTransport("")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Sniff one item from the live worker to pick a real (project, id) pair
	// instead of hard-coding test data.
	body, err := tr.ListPage(ctx, 1, 0)
	if err != nil {
		t.Fatalf("sniff page: %v", err)
	}
	var sniffEnv struct {
		Items []claudeMemRecord `json:"items"`
	}
	if err := json.Unmarshal(body, &sniffEnv); err != nil {
		t.Fatalf("sniff decode: %v\nbody: %s", err, body)
	}
	if len(sniffEnv.Items) == 0 {
		t.Skip("worker has 0 observations — cannot exercise end-to-end path")
	}
	probe := sniffEnv.Items[0]
	if probe.Project == "" || probe.ID == 0 {
		t.Fatalf("gate REGRESSION: sniffed item has empty project=%q or id=%d", probe.Project, probe.ID)
	}
	t.Logf("gate discharge: sniffed probe project=%q id=%d type=%q", probe.Project, probe.ID, probe.Type)

	// ListNative — filter on the probe's own project, since=zero (accept all).
	ids, err := a.ListNative(ctx, probe.Project, time.Time{})
	if err != nil {
		t.Fatalf("ListNative failed against live worker: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("gate REGRESSION: ListNative returned 0 IDs for a project we know has at least one record")
	}
	t.Logf("gate discharge: ListNative(%q) returned %d IDs", probe.Project, len(ids))

	// ReadNative on the first ID — uses per-ID or degrade-to-scan transparently.
	first := ids[0]
	rec, err := a.ReadNative(ctx, first)
	if err != nil {
		t.Fatalf("ReadNative(%q) failed: %v", first, err)
	}
	cmRec, ok := rec.(claudeMemRecord)
	if !ok {
		t.Fatalf("ReadNative returned wrong type: %T", rec)
	}
	if cmRec.ID == 0 {
		t.Errorf("ReadNative returned record with zero ID")
	}
	if cmRec.Raw == nil {
		t.Error("ReadNative must populate Raw")
	}

	// ToCanonical end-to-end — empty IDMap (treat every record as new).
	canonical, err := a.ToCanonical(cmRec, emptyIDMap{})
	if err != nil {
		t.Fatalf("ToCanonical failed on live record: %v", err)
	}
	if canonical.Kind != "session_summary" {
		t.Errorf("Kind: got %q, want %q (ADR-9)", canonical.Kind, "session_summary")
	}
	if canonical.Origin.Provider != "claude-mem" {
		t.Errorf("Origin.Provider: got %q, want %q", canonical.Origin.Provider, "claude-mem")
	}
	if canonical.Origin.ProviderID == "" {
		t.Error("Origin.ProviderID must be non-empty")
	}
	if canonical.Title == "" && canonical.Content == "" {
		t.Error("at least one of Title/Content must be non-empty for a real record")
	}
	if canonical.CreatedAt == "" {
		t.Error("CreatedAt must be non-empty after normalization")
	}
	if canonical.UpdatedAt != canonical.CreatedAt {
		t.Errorf("UpdatedAt must mirror CreatedAt (D7); created=%q updated=%q",
			canonical.CreatedAt, canonical.UpdatedAt)
	}

	t.Logf("gate discharge: end-to-end PASS — adapter decoded live record id=%d title=%q type=%q",
		cmRec.ID, cmRec.Title, cmRec.Type)
	t.Logf("gate discharge: GATE VERDICT — DISCHARGED. Adapter mapping matches the verified live shape.")
}

// emptyIDMap is a trivial adapter.IDMap used by the end-to-end test.
type emptyIDMap struct{}

func (emptyIDMap) CanonicalFromNative(adapter.NativeID) (adapter.CanonicalID, bool) { return "", false }
func (emptyIDMap) NativeFromCanonical(adapter.CanonicalID) (adapter.NativeID, bool) { return "", false }
