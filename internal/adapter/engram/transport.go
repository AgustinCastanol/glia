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

// Transport abstracts the HTTP fallback for ReadNative so that unit tests can
// inject a fake without requiring an engram daemon to be running.
//
// The HTTP path is used only when the CLI path cannot find a record by sync_id.
// A nil Transport causes ReadNative to return adapter.ErrUnsupported at the
// HTTP fallback step.
type Transport interface {
	// GetByID fetches the engram observation with the given native ID via HTTP.
	// Returns adapter.ErrNotFound on 404, adapter.ErrUnavailable on connection
	// errors/timeouts/non-2xx server errors.
	GetByID(ctx context.Context, id adapter.NativeID) (EngramRecord, error)
}

// httpTransport is the production Transport that hits http://127.0.0.1:7437.
type httpTransport struct {
	client  *http.Client
	baseURL string
}

// NewHTTPTransport returns a Transport targeting the local engram HTTP daemon at
// http://127.0.0.1:7437. The caller's context deadline is forwarded to the HTTP
// request — no fixed internal timeout is added.
func NewHTTPTransport() Transport {
	return &httpTransport{
		client:  &http.Client{},
		baseURL: "http://127.0.0.1:7437",
	}
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
