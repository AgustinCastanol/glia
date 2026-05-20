//go:build integration

// PRE-MERGE GATE: provisional mapping in claudeMemRecord is WRONG vs verified
// shape (see engram obs #82). The chain feat/claude-mem-adapter-pr1..pr4 MUST
// NOT merge until claudeMemRecord+ToCanonical+spec REQ-CM-07/08/11+design
// §5/§6/§7 are reworked against the verified shape documented here.
// This test pins the verified shape as the contract for that rework.
//
// GATE VERDICT (2026-05-19): FAILED / NOT DISCHARGED.
// Real worker probed at http://localhost:37701 (supervisor.json has no port
// field; ADR-3 fallback held). Verified shape contradicts every provisional
// field name except title and created_at.
//
// Required rework before merge:
//   - Rewrite claudeMemRecord (PR#1) against verified shape (obs #82)
//   - Rewrite ToCanonical (PR#2) against verified shape
//   - Revise spec #76 REQ-CM-07/08/11 (drop updated_at premise; type vocab
//     exists; project is name not path)
//   - Revise design #77 §5/§6/§7; add ADRs (no updated_at → simpler revision;
//     supervisor.json has no port field → ADR-3 fallback is the real path)

package claudemem

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// verifiedShape documents the confirmed real field names from obs #82.
// Any deviation between this set and what the live worker returns is a
// regression. Use this as the contract for the claudeMemRecord rework.
var verifiedEnvelopeKeys = []string{"items", "hasMore", "offset", "limit"}
var verifiedItemKeys = []string{
	"id",
	"memory_session_id",
	"project",
	"type",
	"title",
	"subtitle",
	"narrative",
	"created_at",
	"created_at_epoch",
	"prompt_number",
}

// knownAbsentKeys are fields present in the provisional claudeMemRecord that
// do NOT exist in the real API response. Their absence is what trips the gate.
var knownAbsentKeys = []string{
	"updated_at",
	"session_id",
	"project_id",
	"summary",
	"tags",
}

func workerBaseURL() string {
	return resolveBaseURL("")
}

// probeWorker issues a GET to the given URL and returns the response body.
// Returns (nil, false) when the server is unreachable (test should t.Skip).
func probeWorker(t *testing.T, url string) ([]byte, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Logf("pre-merge gate: could not build request: %v", err)
		return nil, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("pre-merge gate: worker unreachable at %s (%v) — skipping", url, err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Logf("pre-merge gate: worker returned non-2xx %d at %s — skipping", resp.StatusCode, url)
		return nil, false
	}
	var buf []byte
	buf = make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if readErr != nil {
			break
		}
	}
	return buf, true
}

// TestPreMergeGate_Health confirms the worker is reachable and returns 2xx on
// GET /health. If unreachable the test is skipped gracefully.
func TestPreMergeGate_Health(t *testing.T) {
	base := workerBaseURL()
	body, ok := probeWorker(t, base+"/health")
	if !ok {
		t.Skipf("claude-mem worker unreachable at %s — pre-merge gate skipped", base)
	}
	t.Logf("pre-merge gate: GET /health → %s", string(body))
}

// TestPreMergeGate_DumpFirstObservation connects to the real claude-mem worker,
// fetches the first two observations, and:
//
//  1. Asserts the envelope shape matches the verified contract (obs #82).
//  2. Asserts the first item contains all verified field names.
//  3. Asserts that "id" decodes as a JSON number (NOT a string) — this is the
//     primary hard-break against the provisional claudeMemRecord.ID string field.
//  4. Asserts that the known-absent fields are NOT present in the response.
//  5. Logs the first item's raw JSON for human inspection.
//
// If the worker is unreachable the test skips gracefully.
func TestPreMergeGate_DumpFirstObservation(t *testing.T) {
	base := workerBaseURL()
	obsURL := base + "/api/observations?limit=2&offset=0"

	body, ok := probeWorker(t, obsURL)
	if !ok {
		t.Skipf("claude-mem worker unreachable at %s — pre-merge gate skipped", base)
	}

	// --- 1. Decode envelope ---
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("pre-merge gate: envelope is not a JSON object: %v\nbody: %s", err, body)
	}

	// Assert all verified envelope keys are present.
	for _, key := range verifiedEnvelopeKeys {
		if _, present := envelope[key]; !present {
			t.Errorf("pre-merge gate: envelope missing verified key %q", key)
		}
	}

	// Assert hasMore is a boolean (camelCase confirmed).
	if raw, ok := envelope["hasMore"]; ok {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Errorf("pre-merge gate: hasMore is not a boolean: %v (raw: %s)", err, raw)
		}
	}

	// --- 2. Decode items array ---
	var items []json.RawMessage
	if err := json.Unmarshal(envelope["items"], &items); err != nil {
		t.Fatalf("pre-merge gate: items is not a JSON array: %v", err)
	}
	if len(items) == 0 {
		t.Skip("pre-merge gate: worker returned 0 observations — need ≥1 to discharge gate")
	}

	firstRaw := items[0]
	t.Logf("pre-merge gate: first observation raw JSON:\n%s", string(firstRaw))

	// --- 3. Decode first item as a generic map for key-level inspection ---
	var firstItem map[string]json.RawMessage
	if err := json.Unmarshal(firstRaw, &firstItem); err != nil {
		t.Fatalf("pre-merge gate: first item is not a JSON object: %v", err)
	}

	// --- 4. Assert all verified item keys are present ---
	for _, key := range verifiedItemKeys {
		if _, present := firstItem[key]; !present {
			t.Errorf("pre-merge gate: first item missing verified key %q", key)
		}
	}

	// --- 5. Assert id is a NUMBER, not a string ---
	// This is the primary hard-break: claudeMemRecord.ID is declared as string
	// with json:"id,omitempty". json.Unmarshal into a string field silently
	// produces "" when the JSON value is a number, causing every ID lookup to
	// produce a zero-value string and breaking ListNative/ReadNative entirely.
	if idRaw, present := firstItem["id"]; present {
		var idAsNumber json.Number
		if err := json.Unmarshal(idRaw, &idAsNumber); err != nil {
			t.Errorf("pre-merge gate: id field is not numeric — unexpected shape: raw=%s err=%v", idRaw, err)
		} else {
			t.Logf("pre-merge gate: CONFIRMED id is numeric (json.Number=%s) — claudeMemRecord.ID string BREAKS here", idAsNumber)
		}

		// Also confirm it cannot decode as a string — the provisional struct type.
		var idAsString string
		if err := json.Unmarshal(idRaw, &idAsString); err == nil {
			// If this succeeds the value was quoted (string) — gate would still
			// fail on type mismatch for numbers that look like strings in some
			// JSON encoders.  Log for human inspection regardless.
			t.Logf("pre-merge gate: id also decoded as string=%q (may be string-encoded number)", idAsString)
		} else {
			t.Logf("pre-merge gate: CONFIRMED id cannot decode as string — provisional ID string field is WRONG")
		}
	}

	// --- 6. Assert known-absent fields are NOT present ---
	// These are fields in the provisional struct that do not exist in the real API.
	// Their absence is what makes the provisional mapping wrong.
	for _, key := range knownAbsentKeys {
		if val, present := firstItem[key]; present {
			t.Errorf("pre-merge gate: key %q was expected ABSENT (obs #82) but found with value %s", key, val)
		} else {
			t.Logf("pre-merge gate: CONFIRMED %q absent from real shape (provisional struct field UNUSED)", key)
		}
	}

	// --- 7. Spot-check field type correctness for key verified fields ---
	checkStringField := func(key string) {
		t.Helper()
		raw, present := firstItem[key]
		if !present {
			return // already reported above
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Errorf("pre-merge gate: field %q expected string, got: %s", key, raw)
		} else {
			t.Logf("pre-merge gate: %s=%q", key, s)
		}
	}

	checkStringField("memory_session_id")
	checkStringField("project")
	checkStringField("type")
	checkStringField("title")
	checkStringField("narrative")
	checkStringField("created_at")

	// created_at_epoch must be a number
	if epochRaw, present := firstItem["created_at_epoch"]; present {
		var epoch json.Number
		if err := json.Unmarshal(epochRaw, &epoch); err != nil {
			t.Errorf("pre-merge gate: created_at_epoch is not numeric: %v", err)
		} else {
			t.Logf("pre-merge gate: created_at_epoch=%s (ms)", epoch)
		}
	}

	t.Logf("pre-merge gate: GATE VERDICT — FAILED. provisional claudeMemRecord is wrong vs verified shape (obs #82). Chain MUST NOT merge until rework is complete.")
}
