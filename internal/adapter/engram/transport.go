package engram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/agustincastanol/wrapper-mems/internal/adapter"
)

// Transport abstracts the HTTP calls to the engram daemon so that unit tests
// can inject a fake without requiring a real daemon to be running.
//
// GetByID is used by ReadNative (HTTP fallback when CLI path cannot find a record).
// Export is used by ListNative (GET /export for deterministic enumeration —
// DEFECT-LN-01 fix; engram CLI v1.15 rejects empty search queries).
//
// A nil Transport causes ReadNative to return adapter.ErrUnsupported at the
// HTTP fallback step. ListNative requires a non-nil Transport.
type Transport interface {
	// GetByID fetches the engram observation with the given native ID via HTTP.
	// Returns adapter.ErrNotFound on 404, adapter.ErrUnavailable on connection
	// errors/timeouts/non-2xx server errors.
	GetByID(ctx context.Context, id adapter.NativeID) (EngramRecord, error)

	// Export fetches the full export dump via GET /export and returns the raw
	// JSON body. Returns adapter.ErrUnavailable when the daemon is unreachable.
	// Used by ListNative for deterministic project-scoped enumeration.
	Export(ctx context.Context) ([]byte, error)
}

// httpTransport is the production Transport that hits http://127.0.0.1:7437.
type httpTransport struct {
	client  *http.Client
	baseURL string
}

// NewHTTPTransport returns a Transport targeting the engram HTTP daemon at
// baseURL. If baseURL is empty, the default "http://127.0.0.1:7437" is used.
// The caller's context deadline is forwarded to the HTTP request — no fixed
// internal timeout is added.
func NewHTTPTransport(baseURL string) Transport {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:7437"
	}
	return &httpTransport{
		client:  &http.Client{},
		baseURL: baseURL,
	}
}

// Export performs GET /export against the engram daemon and returns the raw JSON
// body. Maps connection/daemon-down errors to adapter.ErrUnavailable.
// The export response has the shape:
//
//	{ "version": "...", "exported_at": "...", "sessions": [...], "observations": [...] }
//
// where observations is a flat array of all observations across all projects.
// Each observation has fields: id (int), sync_id (string), session_id, type, title,
// content, project, scope, topic_key, revision_count, duplicate_count, last_seen_at,
// created_at, updated_at. Timestamps are in "2006-01-02 15:04:05" UTC format (no T/Z).
func (t *httpTransport) Export(ctx context.Context) ([]byte, error) {
	url := t.baseURL + "/export"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build export request: %v", adapter.ErrUnavailable, err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: http get /export: %v", adapter.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: /export unexpected status %d", adapter.ErrUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read /export body: %v", adapter.ErrUnavailable, err)
	}
	return body, nil
}

// GetByID performs GET /observations/:id against the engram daemon and maps
// HTTP/network errors to the canonical sentinel errors.
func (t *httpTransport) GetByID(ctx context.Context, id adapter.NativeID) (EngramRecord, error) {
	url := fmt.Sprintf("%s/observations/%s", t.baseURL, string(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return EngramRecord{}, fmt.Errorf("%w: build request: %v", adapter.ErrUnavailable, err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		// Connection refused, timeout, or context cancellation all land here.
		// Propagate the underlying error wrapped under ErrUnavailable so the caller
		// can still inspect errors.Is(err, context.DeadlineExceeded) etc.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return EngramRecord{}, err
		}
		return EngramRecord{}, fmt.Errorf("%w: http get: %v", adapter.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return EngramRecord{}, adapter.ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return EngramRecord{}, fmt.Errorf("%w: unexpected status %d", adapter.ErrUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return EngramRecord{}, fmt.Errorf("%w: read body: %v", adapter.ErrUnavailable, err)
	}

	var rec EngramRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return EngramRecord{}, fmt.Errorf("%w: decode body: %v", adapter.ErrUnavailable, err)
	}
	return rec, nil
}
