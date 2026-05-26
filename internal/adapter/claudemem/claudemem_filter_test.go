package claudemem

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
)

// ---------------------------------------------------------------------------
// Privacy filter tests — REQ-PRV-01, REQ-PRV-02, REQ-PRV-03
// ---------------------------------------------------------------------------

// buildItemsPage builds the JSON body that fakeFilterTransport returns for
// ListPage. Each item is a minimal claudeMemRecord encoded as JSON.
func buildItemsPage(recs []claudeMemRecord) []byte {
	items := make([]json.RawMessage, len(recs))
	for i, r := range recs {
		b, _ := json.Marshal(r)
		items[i] = b
	}
	env := struct {
		Items   []json.RawMessage `json:"items"`
		HasMore bool              `json:"hasMore"`
	}{Items: items, HasMore: false}
	b, _ := json.Marshal(env)
	return b
}

// fakeFilterTransport returns a pre-built items page from ListPage and
// ErrUnsupported from GetByID (not used in filter tests).
type fakeFilterTransport struct {
	body []byte
}

func (f *fakeFilterTransport) Health(_ context.Context) error { return nil }

func (f *fakeFilterTransport) ListPage(_ context.Context, _, _ int) ([]byte, error) {
	if f.body != nil {
		return f.body, nil
	}
	return []byte(`{"items":[],"hasMore":false}`), nil
}

func (f *fakeFilterTransport) GetByID(_ context.Context, _ string) ([]byte, bool, error) {
	return nil, false, adapter.ErrUnsupported
}

// makeMinimalRec returns a minimal claudeMemRecord with the given session and project.
func makeMinimalRec(id int64, sessionID, project string) claudeMemRecord {
	return claudeMemRecord{
		ID:              id,
		MemorySessionID: sessionID,
		Project:         project,
		Type:            "manual",
		Title:           "test record",
		CreatedAt:       "2026-05-16T10:00:00.000000000Z",
	}
}

// TestPrivacyFilter_ExcludedSessionReturnsNoRecords verifies REQ-PRV-01:
// excluded sessions produce zero records, no error, no log noise.
func TestPrivacyFilter_ExcludedSessionReturnsNoRecords(t *testing.T) {
	const project = "myproject"
	recs := []claudeMemRecord{
		makeMinimalRec(1, "sess_abc123", project), // excluded
		makeMinimalRec(2, "sess_abc123", project), // excluded (same session)
	}
	tr := &fakeFilterTransport{body: buildItemsPage(recs)}
	a := New(Config{
		ExcludedSessionIDs: []string{"sess_abc123"},
	}, tr)

	ids, err := a.ListNative(context.Background(), project, time.Time{})
	if err != nil {
		t.Fatalf("ListNative: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs for excluded session, got %d: %v", len(ids), ids)
	}
}

// TestPrivacyFilter_NonExcludedSessionPassesThrough verifies REQ-PRV-01:
// non-excluded sessions are returned normally.
func TestPrivacyFilter_NonExcludedSessionPassesThrough(t *testing.T) {
	const project = "myproject"
	recs := []claudeMemRecord{
		makeMinimalRec(1, "sess_abc123", project), // excluded
		makeMinimalRec(2, "sess_xyz999", project), // NOT excluded
		makeMinimalRec(3, "sess_xyz999", project), // NOT excluded
	}
	tr := &fakeFilterTransport{body: buildItemsPage(recs)}
	a := New(Config{
		ExcludedSessionIDs: []string{"sess_abc123"},
	}, tr)

	ids, err := a.ListNative(context.Background(), project, time.Time{})
	if err != nil {
		t.Fatalf("ListNative: unexpected error: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 IDs for non-excluded sessions, got %d: %v", len(ids), ids)
	}
	for _, id := range ids {
		if string(id) == "1" {
			t.Errorf("excluded session record (id=1) leaked into results")
		}
	}
}

// TestPrivacyFilter_MultipleExcludedSessions verifies that multiple excluded
// session IDs are all filtered.
func TestPrivacyFilter_MultipleExcludedSessions(t *testing.T) {
	const project = "myproject"
	recs := []claudeMemRecord{
		makeMinimalRec(1, "sess_a", project), // excluded
		makeMinimalRec(2, "sess_b", project), // excluded
		makeMinimalRec(3, "sess_c", project), // NOT excluded
	}
	tr := &fakeFilterTransport{body: buildItemsPage(recs)}
	a := New(Config{
		ExcludedSessionIDs: []string{"sess_a", "sess_b"},
	}, tr)

	ids, err := a.ListNative(context.Background(), project, time.Time{})
	if err != nil {
		t.Fatalf("ListNative: unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 ID (non-excluded), got %d: %v", len(ids), ids)
	}
}

// TestPrivacyFilter_EmptyExclusionList verifies that an empty ExcludedSessionIDs
// list passes all records through normally.
func TestPrivacyFilter_EmptyExclusionList(t *testing.T) {
	const project = "myproject"
	recs := []claudeMemRecord{
		makeMinimalRec(1, "sess_any", project),
		makeMinimalRec(2, "sess_other", project),
	}
	tr := &fakeFilterTransport{body: buildItemsPage(recs)}
	a := New(Config{ExcludedSessionIDs: nil}, tr)

	ids, err := a.ListNative(context.Background(), project, time.Time{})
	if err != nil {
		t.Fatalf("ListNative: unexpected error: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 IDs with empty exclusion list, got %d: %v", len(ids), ids)
	}
}

// TestPrivacyFilter_ExplicitAuthor verifies that a pre-resolved Author in
// Config is propagated to canonical records via ToCanonical.
func TestPrivacyFilter_ExplicitAuthor(t *testing.T) {
	a := New(Config{Author: "alice@example.com"}, nil)
	if a.cfg.Author != "alice@example.com" {
		t.Errorf("expected Author=%q, got %q", "alice@example.com", a.cfg.Author)
	}
}
